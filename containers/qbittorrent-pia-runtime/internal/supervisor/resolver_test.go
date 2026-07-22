package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotResolverRestoresInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	original := []byte("nameserver 10.43.0.10\nsearch default.svc.cluster.local\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	restore, err := SnapshotResolver(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("nameserver 127.0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("resolver=%q", got)
	}
	if err := restore(); err != nil {
		t.Fatalf("idempotent restore: %v", err)
	}
}

func TestSnapshotResolverRejectsUnsafeInput(t *testing.T) {
	for _, path := range []string{"relative", "/"} {
		if _, err := SnapshotResolver(path); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
	path := filepath.Join(t.TempDir(), "resolv.conf")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SnapshotResolver(path); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("error=%v", err)
	}
}
