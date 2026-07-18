//go:build linux

package health

import (
	"syscall"
)

func bindToDevice(name string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, raw syscall.RawConn) error {
		var bindErr error
		if err := raw.Control(func(fd uintptr) { bindErr = syscall.SetsockoptString(int(fd), syscall.SOL_SOCKET, 25, name) }); err != nil {
			return err
		}
		return bindErr
	}
}
