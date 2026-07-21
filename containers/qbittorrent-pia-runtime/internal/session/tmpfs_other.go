//go:build !linux

package session

func RequireTmpfs(string) error { return nil }
