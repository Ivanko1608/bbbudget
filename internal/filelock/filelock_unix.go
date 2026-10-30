//go:build unix

// Package filelock serialises access to files across processes.
package filelock

import (
	"os"
	"syscall"
)

// Lock locks the file at path, creating it if needed, and returns the unlock
// function. An exclusive lock waits for every other holder; a shared one only
// for an exclusive holder.
func Lock(path string, exclusive bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// SyncDir flushes a directory, so a rename inside it survives a crash.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
