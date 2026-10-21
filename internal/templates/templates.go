// Package templates provides starting points for new budgets.
//
// Templates are budget documents in YAML or JSON. A few neutral ones are
// built in; anything else lives in the user's template directory (by default
// ~/.config/bbbudget/templates), outside this repository. A user template
// with the same name as a built-in replaces it.
//
// When a budget is created from a template, its name is the one given at
// creation (or the template's) and its first month is the current month.
package templates

import (
	"cmp"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"bbbudget/internal/budget"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

// builtinDir is the embedded directory, and the start of a built-in
// template's Source.
const builtinDir = "builtin"

// Template is a validated budget document plus where it came from.
type Template struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Source is the file it was loaded from, under builtin/ for the embedded ones.
	Source string        `json:"source"`
	Doc    budget.Budget `json:"-"`
}

// Registry holds templates by name.
type Registry struct{ byName map[string]Template }

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Builtin returns a registry with the embedded templates only.
func Builtin() *Registry {
	r := &Registry{byName: map[string]Template{}}
	if err := r.loadFS(builtinFS, builtinDir, builtinDir); err != nil {
		panic(err) // embedded files are covered by tests
	}
	return r
}

// Load returns the built-ins plus every template in dir. A missing dir is
// fine; a broken file is an error naming the file, so mistakes are not
// silently ignored.
func Load(dir string) (*Registry, error) {
	r := Builtin()
	if dir == "" {
		return r, nil
	}
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	return r, r.loadFS(os.DirFS(dir), ".", dir)
}

func (r *Registry) loadFS(fsys fs.FS, root, sourceDir string) error {
	entries, err := fs.ReadDir(fsys, root)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		ext := strings.ToLower(path.Ext(e.Name()))
		if e.IsDir() || (ext != ".json" && ext != ".yaml" && ext != ".yml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), path.Ext(e.Name()))
		source := filepath.Join(sourceDir, e.Name())
		t, err := parse(fsys, path.Join(root, e.Name()), name, ext[1:], source)
		if err != nil {
			errs = append(errs, fmt.Errorf("template %s: %w", source, err))
			continue
		}
		r.byName[name] = t
	}
	return errors.Join(errs...)
}

func parse(fsys fs.FS, file, name, format, source string) (Template, error) {
	if !nameRe.MatchString(name) {
		return Template{}, fmt.Errorf("file name must be lowercase letters, digits, '-' or '_' (it becomes the template name)")
	}
	f, err := fsys.Open(file)
	if err != nil {
		return Template{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, budget.MaxDocumentBytes+1))
	if err != nil {
		return Template{}, err
	}
	doc, err := budget.ParseDocument(data, format) // rejects oversized input
	if err != nil {
		return Template{}, err
	}
	// Validate with the values creation will fill in.
	probe := doc
	probe.StartMonth = "2000-01"
	if probe.Name == "" {
		probe.Name = name
	}
	if _, err := budget.Compile(probe); err != nil {
		return Template{}, err
	}
	return Template{Name: name, Description: doc.Description, Source: source, Doc: doc}, nil
}

// List returns templates sorted by name.
func (r *Registry) List() []Template {
	out := slices.Collect(maps.Values(r.byName))
	slices.SortFunc(out, func(a, b Template) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Names returns template names, sorted.
func (r *Registry) Names() []string {
	var out []string
	for _, t := range r.List() {
		out = append(out, t.Name)
	}
	return out
}

// New builds a budget from a template, starting in the month containing start.
func (r *Registry) New(template, name string, start time.Time) (budget.Budget, error) {
	t, ok := r.byName[template]
	if !ok {
		return budget.Budget{}, &budget.ValidationError{Issues: []budget.Issue{{
			Path: "template", Message: fmt.Sprintf("unknown template %q (have %s)", template, strings.Join(r.Names(), ", ")),
		}}}
	}
	b, err := budget.Clone(t.Doc)
	if err != nil {
		return budget.Budget{}, err
	}
	switch {
	case name != "":
		b.Name = name
	case b.Name == "":
		b.Name = template
	}
	b.StartMonth = start.Format("2006-01")
	return b, nil
}

// DefaultDir is $BBBUDGET_TEMPLATES, else $XDG_CONFIG_HOME/bbbudget/templates,
// else ~/.config/bbbudget/templates.
func DefaultDir() string {
	if d := os.Getenv("BBBUDGET_TEMPLATES"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "bbbudget", "templates")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "bbbudget", "templates")
	}
	return ""
}
