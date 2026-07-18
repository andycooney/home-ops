package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const ReaderID = 65532

type Ownership interface {
	Chown(path string, uid, gid int) error
}
type OSOwnership struct{}

func (OSOwnership) Chown(path string, uid, gid int) error { return os.Chown(path, uid, gid) }

type Generation struct {
	ID, Region, Endpoint, TLSHostname, WGGateway, PFGateway string
	Token, WGConfig                                         string
}

type Publisher struct {
	Root      string
	ReaderGID int
	Owner     Ownership
}

func (p Publisher) PublishCurrent(g Generation) (string, error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	if p.ReaderGID == 0 {
		p.ReaderGID = ReaderID
	}
	if p.Owner == nil {
		p.Owner = OSOwnership{}
	}
	if err := p.ensureDirectories(g.ID); err != nil {
		return "", err
	}
	dir := filepath.Join(p.Root, "sessions", g.ID)
	files := []struct {
		name, value string
		mode        os.FileMode
		uid, gid    int
	}{
		{"generation", g.ID + "\n", 0o640, 0, p.ReaderGID}, {"region", g.Region + "\n", 0o640, 0, p.ReaderGID},
		{"endpoint", g.Endpoint + "\n", 0o640, 0, p.ReaderGID}, {"tls-hostname", g.TLSHostname + "\n", 0o640, 0, p.ReaderGID},
		{"wg-gateway", g.WGGateway + "\n", 0o640, 0, p.ReaderGID}, {"pf-gateway", g.PFGateway + "\n", 0o640, 0, p.ReaderGID},
		{"pia.token", g.Token + "\n", 0o640, 0, p.ReaderGID}, {"wg0.conf", g.WGConfig, 0o600, 0, 0},
	}
	for _, file := range files {
		if err := p.atomicFile(dir, file.name, []byte(file.value), file.mode, file.uid, file.gid); err != nil {
			return "", err
		}
	}
	for _, name := range []string{"payload", "signature", "expires-at"} {
		if err := p.atomicFile(filepath.Join(dir, "pf"), name, nil, 0o600, p.ReaderGID, p.ReaderGID); err != nil {
			return "", err
		}
	}
	if err := syncDir(dir); err != nil {
		return "", err
	}
	if err := p.atomicLink("current", filepath.Join("sessions", g.ID)); err != nil {
		return "", err
	}
	return dir, nil
}

func (p Publisher) PublishReady(id string) error {
	if !validID(id) {
		return errors.New("invalid generation ID")
	}
	if _, err := os.Stat(filepath.Join(p.Root, "sessions", id, "wg0.conf")); err != nil {
		return errors.New("generation is incomplete")
	}
	return p.atomicLink("ready", filepath.Join("sessions", id))
}

func (p Publisher) InvalidateReady() error {
	return p.invalidateLink("ready")
}

func (p Publisher) InvalidateCurrent() error { return p.invalidateLink("current") }

func (p Publisher) invalidateLink(name string) error {
	path := filepath.Join(p.Root, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDir(p.Root)
}

func (p Publisher) Remove(id string) error {
	if !validID(id) {
		return errors.New("invalid generation ID")
	}
	return os.RemoveAll(filepath.Join(p.Root, "sessions", id))
}

func (p Publisher) ensureDirectories(id string) error {
	if !validID(id) {
		return errors.New("invalid generation ID")
	}
	dirs := []struct {
		path     string
		mode     os.FileMode
		uid, gid int
	}{
		{p.Root, 0o750, 0, p.ReaderGID},
		{filepath.Join(p.Root, "sessions"), 0o710, 0, p.ReaderGID},
		{filepath.Join(p.Root, "sessions", id), 0o710, 0, p.ReaderGID},
		{filepath.Join(p.Root, "sessions", id, "pf"), 0o730, p.ReaderGID, p.ReaderGID},
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return err
		}
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return err
		}
		if err := p.Owner.Chown(dir.path, dir.uid, dir.gid); err != nil {
			return err
		}
	}
	return nil
}

func (p Publisher) atomicFile(dir, name string, data []byte, mode os.FileMode, uid, gid int) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := p.Owner.Chown(tmpName, uid, gid); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		return err
	}
	return syncDir(dir)
}

func (p Publisher) atomicLink(name, target string) error {
	tmp := filepath.Join(p.Root, "."+name+".tmp-"+strconv.Itoa(os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(p.Root, name)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(p.Root)
}

func (g Generation) Validate() error {
	values := map[string]string{"generation": g.ID, "region": g.Region, "endpoint": g.Endpoint, "tls-hostname": g.TLSHostname, "wg-gateway": g.WGGateway, "pf-gateway": g.PFGateway, "token": g.Token, "wg config": g.WGConfig}
	for key, value := range values {
		if value == "" || strings.ContainsAny(value, "\x00\r") {
			return fmt.Errorf("invalid %s", key)
		}
	}
	return nil
}
func validID(id string) bool {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\\x00") {
		return false
	}
	return true
}
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
