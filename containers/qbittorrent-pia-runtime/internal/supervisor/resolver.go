package supervisor

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

const maxResolverConfigBytes = 64 << 10

// SnapshotResolver captures the pod resolver before Gluetun replaces it and
// returns an idempotent in-place restorer. In-place writes are required because
// Kubernetes mounts /etc/resolv.conf as a file and rejects rename replacement.
func SnapshotResolver(path string) (func() error, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
		return nil, errors.New("resolver path must be a safe absolute path")
	}
	contents, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 || len(contents) > maxResolverConfigBytes {
		return nil, errors.New("resolver configuration size is invalid")
	}
	return func() error {
		file, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			return err
		}
		written, writeErr := file.Write(contents)
		if writeErr == nil && written != len(contents) {
			writeErr = io.ErrShortWrite
		}
		syncErr := file.Sync()
		closeErr := file.Close()
		return errors.Join(writeErr, syncErr, closeErr)
	}, nil
}
