//go:build !unix

package filelock

// Lock does not lock across processes on this platform: writes are serialised
// within one process only. Run a single bbbudget process per directory.
func Lock(path string, exclusive bool) (func(), error) { return func() {}, nil }

// SyncDir does nothing on this platform.
func SyncDir(dir string) error { return nil }
