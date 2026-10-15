//go:build !unix

package filelock

// Without flock, writes are serialised within one process only. Run a single
// bbbudget process per directory on these platforms.
func Lock(path string, exclusive bool) (func(), error) { return func() {}, nil }

func SyncDir(dir string) error { return nil }
