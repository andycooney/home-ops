package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type fakeOwner struct {
	mu    sync.Mutex
	calls map[string][2]int
}

func (f *fakeOwner) Chown(path string, uid, gid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[path] = [2]int{uid, gid}
	return nil
}

func generation(id string) Generation {
	return Generation{ID: id, Region: "ca", Endpoint: "192.0.2.1:1337", TLSHostname: "ca.example.invalid", WGGateway: "10.0.0.1", PFGateway: "192.0.2.1", Token: "token-fixture", WGConfig: "[Interface]\nPrivateKey = private-fixture\n"}
}

func TestAtomicPublicationPermissionsAndIsolation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pia")
	owner := &fakeOwner{calls: map[string][2]int{}}
	publisher := Publisher{Root: root, ReaderGID: ReaderID, Owner: owner}
	dir, err := publisher.PublishCurrent(generation("gen-one"))
	if err != nil {
		t.Fatal(err)
	}
	checks := map[string]os.FileMode{"generation": 0o640, "region": 0o640, "endpoint": 0o640, "tls-hostname": 0o640, "wg-gateway": 0o640, "pf-gateway": 0o640, "pia.token": 0o640, "wg0.conf": 0o600, "pf/payload": 0o600, "pf/signature": 0o600, "pf/expires-at": 0o600, "pf/port": 0o600}
	for name, want := range checks {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%o want=%o", name, info.Mode().Perm(), want)
		}
	}
	for name, want := range map[string]os.FileMode{"": 0o750, "sessions": 0o710, "sessions/gen-one": 0o710, "sessions/gen-one/pf": 0o730} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%o want=%o", name, info.Mode().Perm(), want)
		}
	}
	if target, err := os.Readlink(filepath.Join(root, "current")); err != nil || target != "sessions/gen-one" {
		t.Fatalf("current=%q err=%v", target, err)
	}
	if err := publisher.PublishReady("gen-one"); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(filepath.Join(root, "ready")); target != "sessions/gen-one" {
		t.Fatalf("ready=%q", target)
	}
	if err := publisher.InvalidateReady(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "ready")); !os.IsNotExist(err) {
		t.Fatal("ready was not invalidated")
	}
	if err := publisher.InvalidateCurrent(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "current")); !os.IsNotExist(err) {
		t.Fatal("current was not invalidated")
	}
	if info, _ := os.Stat(filepath.Join(dir, "wg0.conf")); info.Mode().Perm()&0o077 != 0 {
		t.Fatal("PF identity could read wg0.conf")
	}
	foundPFWriteOwner := false
	for path, ids := range owner.calls {
		if strings.Contains(path, string(filepath.Separator)+"pf"+string(filepath.Separator)+".") && ids == [2]int{ReaderID, ReaderID} {
			foundPFWriteOwner = true
		}
	}
	if !foundPFWriteOwner {
		t.Fatal("PF files were not owned by helper identity before publication")
	}
}

func TestForwardedPortPublicationValidation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pia")
	publisher := Publisher{Root: root, ReaderGID: ReaderID, Owner: &fakeOwner{calls: map[string][2]int{}}}
	if _, err := publisher.PublishCurrent(generation("gen-one")); err != nil {
		t.Fatal(err)
	}
	portPath := filepath.Join(root, "sessions", "gen-one", "pf", "port")
	if _, err := publisher.ReadForwardedPort("gen-one"); !errors.Is(err, ErrPFPortPending) {
		t.Fatalf("empty PF port error=%v", err)
	}
	write := func(value string) {
		t.Helper()
		if err := os.WriteFile(portPath, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"generation":"gen-one","port":49152}`)
	if port, err := publisher.ReadForwardedPort("gen-one"); err != nil || port != 49152 {
		t.Fatalf("port=%d error=%v", port, err)
	}
	if err := os.Chmod(portPath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.ReadForwardedPort("gen-one"); !errors.Is(err, ErrPFPortInvalid) {
		t.Fatalf("wrong-mode PF port error=%v", err)
	}
	if err := os.Chmod(portPath, 0o600); err != nil {
		t.Fatal(err)
	}
	write(`{"generation":"gen-old","port":49152}`)
	if _, err := publisher.ReadForwardedPort("gen-one"); !errors.Is(err, ErrPFPortStale) {
		t.Fatalf("stale PF port error=%v", err)
	}
	for _, value := range []string{
		`{"generation":"gen-one","port":0}`,
		`{"generation":"gen-one","port":65536}`,
		`{"generation":"gen-one","port":49152,"command":"iptables"}`,
		`not-json`,
	} {
		write(value)
		if _, err := publisher.ReadForwardedPort("gen-one"); !errors.Is(err, ErrPFPortInvalid) {
			t.Fatalf("invalid PF port %q error=%v", value, err)
		}
	}
}

func TestConfiguredPFHelperOwnsOnlyPFWritablePaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pia")
	owner := &fakeOwner{calls: map[string][2]int{}}
	publisher := Publisher{Root: root, ReaderGID: ReaderID, PFHelperUID: 60000, Owner: owner}
	dir, err := publisher.PublishCurrent(generation("gen-one"))
	if err != nil {
		t.Fatal(err)
	}
	for path, ids := range owner.calls {
		isPFPath := path == filepath.Join(dir, "pf") || strings.Contains(path, string(filepath.Separator)+"pf"+string(filepath.Separator))
		if isPFPath && ids[0] != 60000 {
			t.Fatalf("PF path %s owner=%v", path, ids)
		}
		if !isPFPath && ids[0] == 60000 {
			t.Fatalf("PF helper owns non-PF path %s", path)
		}
	}
}

func TestAtomicReplacementAndStaleRemoval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pia")
	publisher := Publisher{Root: root, ReaderGID: ReaderID, Owner: &fakeOwner{calls: map[string][2]int{}}}
	if _, err := publisher.PublishCurrent(generation("gen-one")); err != nil {
		t.Fatal(err)
	}
	if err := publisher.PublishReady("gen-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := publisher.PublishCurrent(generation("gen-two")); err != nil {
		t.Fatal(err)
	}
	if err := publisher.PublishReady("gen-two"); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(filepath.Join(root, "current")); target != "sessions/gen-two" {
		t.Fatalf("current=%q", target)
	}
	if target, _ := os.Readlink(filepath.Join(root, "ready")); target != "sessions/gen-two" {
		t.Fatalf("ready=%q", target)
	}
	if err := publisher.Remove("gen-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "sessions", "gen-one")); !os.IsNotExist(err) {
		t.Fatal("stale generation remains")
	}
}

func TestRejectsUnsafeGenerationID(t *testing.T) {
	p := Publisher{Root: t.TempDir(), Owner: &fakeOwner{calls: map[string][2]int{}}}
	g := generation("../escape")
	if _, err := p.PublishCurrent(g); err == nil {
		t.Fatal("unsafe generation accepted")
	}
}
