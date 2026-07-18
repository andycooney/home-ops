//go:build linux

package session

import (
	"errors"
	"os"
	"syscall"
)

const tmpfsMagic = 0x01021994

func RequireTmpfs(path string) error {
	if err := os.MkdirAll(path, 0o750); err != nil {
		return err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return err
	}
	if uint64(stat.Type) != tmpfsMagic {
		return errors.New("PIA runtime directory must be a tmpfs")
	}
	return nil
}
