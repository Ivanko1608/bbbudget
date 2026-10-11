// Package accounts keeps several people's budgets apart on one server. Each
// account gets its own data directory and settings file, and its own App, so
// nothing below the transports needs to know that accounts exist.
package accounts

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"

	"bbbudget/internal/app"
	"bbbudget/internal/store"
	"bbbudget/internal/templates"
)

// ErrBadID means an account id is not safe to use as a directory name.
var ErrBadID = errors.New("account id must be 1-64 lowercase letters, digits or '-', starting with a letter or digit")

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidID reports whether id is a safe account id (also safe as a directory name).
func ValidID(id string) bool { return idRe.MatchString(id) }

const (
	budgetsDir   = "budgets"
	settingsFile = "settings.json"
)

// Registry opens the App of each account under one root directory:
// <root>/<account>/budgets/ holds its budgets and
// <root>/<account>/settings.json its settings.
type Registry struct {
	root      string
	templates *templates.Registry

	mu   sync.Mutex
	apps map[string]*app.App
}

// NewRegistry opens (and creates if needed) a root directory. Every account
// shares the templates t; nil means built-in templates only.
func NewRegistry(root string, t *templates.Registry) (*Registry, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Registry{root: root, templates: t, apps: map[string]*app.App{}}, nil
}

// Open returns the App of an account, creating the account's directory the
// first time. Every call for the same account returns the same App, so its
// settings lock and auto-refresh state are shared.
func (r *Registry) Open(id string) (*app.App, error) {
	if !ValidID(id) {
		return nil, ErrBadID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.apps[id]; ok {
		return a, nil
	}
	a, err := r.newApp(id)
	if err != nil {
		return nil, err
	}
	r.apps[id] = a
	return a, nil
}

func (r *Registry) newApp(id string) (*app.App, error) {
	dir := filepath.Join(r.root, id)
	st, err := store.NewFileStore(filepath.Join(dir, budgetsDir))
	if err != nil {
		return nil, err
	}
	a := app.New(st, r.templates)
	a.SettingsPath = filepath.Join(dir, settingsFile)
	return a, nil
}

// List returns the ids of every account that has a directory, sorted.
func (r *Registry) List() ([]string, error) {
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() && ValidID(e.Name()) {
			ids = append(ids, e.Name())
		}
	}
	slices.Sort(ids)
	return ids, nil
}
