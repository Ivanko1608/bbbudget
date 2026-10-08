//go:build !unix

package store

// Without flock, writes are serialised within one process only. Run a single
// bbbudget process per data directory on these platforms.
func lockFile(path string, exclusive bool) (func(), error) { return func() {}, nil }

func syncDir(dir string) error { return nil }
