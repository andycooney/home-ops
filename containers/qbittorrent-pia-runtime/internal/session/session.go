package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const ReaderID = 65532

var (
	ErrPFPortPending = errors.New("PF port is not published")
	ErrPFPortInvalid = errors.New("PF port publication is invalid")
	ErrPFPortStale   = errors.New("PF port publication is stale")
)

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
	Root        string
	ReaderGID   int
	PFHelperUID int
	Owner       Ownership
	before      func(string, string) error
	sync        func(string) error
}

func (p Publisher) PublishCurrent(g Generation) (publishedDir string, resultErr error) {
	if err := g.Validate(); err != nil {
		return "", err
	}
	if !validID(g.ID) {
		return "", errors.New("invalid generation ID")
	}
	if p.ReaderGID == 0 {
		p.ReaderGID = ReaderID
	}
	if p.PFHelperUID == 0 {
		p.PFHelperUID = ReaderID
	}
	if p.Owner == nil {
		p.Owner = OSOwnership{}
	}
	if err := p.ensureBaseDirectories(); err != nil {
		return "", err
	}
	dir := filepath.Join(p.Root, "sessions", g.ID)
	if _, err := os.Lstat(dir); err == nil {
		return "", errors.New("generation already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	committed := false
	defer func() {
		if !committed {
			removeErr := os.RemoveAll(dir)
			syncErr := p.syncDir(filepath.Join(p.Root, "sessions"))
			if removeErr != nil {
				removeErr = fmt.Errorf("remove partial generation: %w", removeErr)
			}
			if syncErr != nil {
				syncErr = fmt.Errorf("sync generation cleanup: %w", syncErr)
			}
			resultErr = errors.Join(resultErr, removeErr, syncErr)
		}
	}()
	if err := p.createGenerationDirectories(dir); err != nil {
		return "", err
	}
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
	for _, name := range []string{"payload", "signature", "expires-at", "port"} {
		if err := p.atomicFile(filepath.Join(dir, "pf"), name, nil, 0o600, p.PFHelperUID, p.ReaderGID); err != nil {
			return "", err
		}
	}
	if err := p.checkpoint("generation-sync", dir); err != nil {
		return "", err
	}
	if err := p.syncDir(dir); err != nil {
		return "", err
	}
	if err := p.atomicLink("current", filepath.Join("sessions", g.ID)); err != nil {
		return "", err
	}
	committed = true
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

func (p Publisher) ReadForwardedPort(id string) (uint16, error) {
	if !validID(id) {
		return 0, ErrPFPortInvalid
	}
	path := filepath.Join(p.Root, "sessions", id, "pf", "port")
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrPFPortPending
		}
		return 0, fmt.Errorf("read PF port metadata: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return 0, ErrPFPortInvalid
	}
	if info.Size() == 0 {
		return 0, ErrPFPortPending
	}
	if info.Size() > 256 {
		return 0, ErrPFPortInvalid
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read PF port metadata: %w", err)
	}
	var record struct {
		Generation string `json:"generation"`
		Port       int    `json:"port"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return 0, ErrPFPortInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return 0, ErrPFPortInvalid
	}
	if record.Generation != id {
		return 0, ErrPFPortStale
	}
	if record.Port < 1 || record.Port > 65535 {
		return 0, ErrPFPortInvalid
	}
	return uint16(record.Port), nil
}

func (p Publisher) invalidateLink(name string) error {
	path := filepath.Join(p.Root, name)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return p.syncDir(p.Root)
}

func (p Publisher) Remove(id string) error {
	if !validID(id) {
		return errors.New("invalid generation ID")
	}
	if err := os.RemoveAll(filepath.Join(p.Root, "sessions", id)); err != nil {
		return err
	}
	return p.syncDir(filepath.Join(p.Root, "sessions"))
}

func (p Publisher) ensureBaseDirectories() error {
	dirs := []struct {
		path     string
		mode     os.FileMode
		uid, gid int
	}{
		{p.Root, 0o750, 0, p.ReaderGID},
		{filepath.Join(p.Root, "sessions"), 0o710, 0, p.ReaderGID},
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

func (p Publisher) createGenerationDirectories(dir string) error {
	if err := os.Mkdir(dir, 0o710); err != nil {
		return err
	}
	if err := p.checkpoint("generation-directory", dir); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o710); err != nil {
		return err
	}
	if err := p.Owner.Chown(dir, 0, p.ReaderGID); err != nil {
		return err
	}
	pfDir := filepath.Join(dir, "pf")
	if err := os.Mkdir(pfDir, 0o730); err != nil {
		return err
	}
	if err := p.checkpoint("pf-directory", pfDir); err != nil {
		return err
	}
	if err := os.Chmod(pfDir, 0o730); err != nil {
		return err
	}
	return p.Owner.Chown(pfDir, p.PFHelperUID, p.ReaderGID)
}

func (p Publisher) atomicFile(dir, name string, data []byte, mode os.FileMode, uid, gid int) error {
	tmp, err := os.CreateTemp(dir, "."+name+".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := p.checkpoint("file:"+name, tmpName); err != nil {
		tmp.Close()
		return err
	}
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
	return p.syncDir(dir)
}

func (p Publisher) atomicLink(name, target string) error {
	path := filepath.Join(p.Root, name)
	oldTarget, err := os.Readlink(path)
	hadOld := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmp := filepath.Join(p.Root, "."+name+".tmp-"+strconv.Itoa(os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := p.checkpoint("link:"+name, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := p.checkpoint("link-sync:"+name, path); err != nil {
		return errors.Join(err, p.rollbackLink(path, oldTarget, hadOld))
	}
	if err := p.syncDir(p.Root); err != nil {
		return errors.Join(err, p.rollbackLink(path, oldTarget, hadOld))
	}
	return nil
}

func (p Publisher) rollbackLink(path, oldTarget string, hadOld bool) error {
	if !hadOld {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return p.syncDir(p.Root)
	}
	tmp := path + ".rollback-" + strconv.Itoa(os.Getpid())
	_ = os.Remove(tmp)
	if err := os.Symlink(oldTarget, tmp); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return p.syncDir(p.Root)
}

func (p Publisher) checkpoint(step, path string) error {
	if p.before == nil {
		return nil
	}
	return p.before(step, path)
}

func (p Publisher) syncDir(path string) error {
	if p.sync != nil {
		return p.sync(path)
	}
	return syncDir(path)
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
