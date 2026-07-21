//go:build !linux

package health

import "syscall"

func bindToDevice(_ string) func(string, string, syscall.RawConn) error {
	return func(_, _ string, _ syscall.RawConn) error { return nil }
}
