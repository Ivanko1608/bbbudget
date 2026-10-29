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
var ErrBadID = errors.New(
	"account id must be 1-64 lowercase letters, digits or '-', " +
		"starting with a letter or digit",
)

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidID reports whether id is a safe account id (also safe as a directory name).
func ValidID(id string) bool { return idRe.MatchString(id) }

const (
	budgetsDir   = "budgets"
	settingsFile = "settings.json"
)

// Dir is the directory holding every account, and a cache of their Apps:
// <root>/<account>/budgets/ holds an account's budgets and
// <root>/<account>/settings.json its settings.
type Dir struct {
	root      string
	templates *templates.Registry

	mu   sync.Mutex
	apps map[string]*app.App
}

// OpenDir opens (and creates if needed) the accounts directory. Every account
// shares templateRegistry; nil means built-in templates only.
func OpenDir(root string, templateRegistry *templates.Registry) (*Dir, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}

	return &Dir{
		root:      root,
		templates: templateRegistry,
		apps:      map[string]*app.App{},
	}, nil
}

// GetOrCreate returns the App of an account, creating the account's directory
// the first time. Every call for the same account returns the same App, so its
// settings lock and auto-refresh state are shared.
func (d *Dir) GetOrCreate(id string) (*app.App, error) {
	if !ValidID(id) {
		return nil, ErrBadID
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if existing, ok := d.apps[id]; ok {
		return existing, nil
	}

	loaded, err := d.loadAccount(id)
	if err != nil {
		return nil, err
	}
	d.apps[id] = loaded

	return loaded, nil
}

func (d *Dir) loadAccount(id string) (*app.App, error) {
	accountDir := filepath.Join(d.root, id)

	budgetStore, err := store.NewFileStore(filepath.Join(accountDir, budgetsDir))
	if err != nil {
		return nil, err
	}

	accountApp := app.New(budgetStore, d.templates)
	accountApp.SettingsPath = filepath.Join(accountDir, settingsFile)

	return accountApp, nil
}

// List returns the ids of every account that has a directory, sorted.
func (d *Dir) List() ([]string, error) {
	entries, err := os.ReadDir(d.root)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, entry := range entries {
		if entry.IsDir() && ValidID(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	slices.Sort(ids)
	return ids, nil
}
