package agents

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sametakofficial/claude-hybrid-router/internal/marker"
)

// Dir manages the agent files in one directory.
type Dir struct {
	Path     string
	TrashDir string
}

// List returns all agents in the directory, sorted by name.
func (d Dir) List() ([]Agent, error) {
	entries, err := os.ReadDir(d.Path)
	if os.IsNotExist(err) {
		return []Agent{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Agent{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		file := filepath.Join(d.Path, e.Name())
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		a, err := Parse(file, data)
		if err != nil {
			// Show broken files instead of hiding them.
			a = Agent{Name: strings.TrimSuffix(e.Name(), ".md"), File: file, Description: "(could not parse: " + err.Error() + ")"}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Exists reports whether the agent's file exists.
func (d Dir) Exists(name string) bool {
	_, err := os.Stat(d.file(name))
	return err == nil
}

// Get loads one agent by name (its file name).
func (d Dir) Get(name string) (Agent, error) {
	file := d.file(name)
	data, err := os.ReadFile(file)
	if err != nil {
		return Agent{}, err
	}
	return Parse(file, data)
}

func (d Dir) file(name string) string { return filepath.Join(d.Path, name+".md") }

// Save writes an agent. If oldName is set and differs, the old file is moved
// to the trash after the new one is written. Unedited frontmatter keys of the
// existing file are kept.
func (d Dir) Save(oldName string, a Agent) (Agent, error) {
	if oldName == "" {
		oldName = a.Name
	}
	if !marker.ValidName(a.Name) || !marker.ValidName(oldName) {
		return Agent{}, fmt.Errorf("agent name may only use letters, digits, '.', '-' and '_'")
	}
	a.FrontName, a.NameMismatch = "", false
	if prev, err := d.Get(oldName); err == nil && a.Extra == nil {
		a.Extra = prev.Extra
	}
	if oldName != a.Name {
		if _, err := os.Stat(d.file(a.Name)); err == nil {
			return Agent{}, fmt.Errorf("an agent named %q already exists", a.Name)
		}
	}
	data, err := Render(a)
	if err != nil {
		return Agent{}, err
	}
	if err := os.MkdirAll(d.Path, 0o755); err != nil {
		return Agent{}, err
	}
	a.File = d.file(a.Name)
	tmp := a.File + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return Agent{}, err
	}
	if err := os.Rename(tmp, a.File); err != nil {
		return Agent{}, err
	}
	if oldName != a.Name {
		if _, err := d.Trash(oldName); err != nil && !os.IsNotExist(err) {
			return a, err
		}
	}
	return a, nil
}

// Trash moves the agent's file into the trash directory.
func (d Dir) Trash(name string) (string, error) {
	src := d.file(name)
	if _, err := os.Stat(src); err != nil {
		return "", err
	}
	if err := os.MkdirAll(d.TrashDir, 0o700); err != nil {
		return "", err
	}
	// Unique even for two moves in the same second.
	base := filepath.Join(d.TrashDir, time.Now().Format("20060102-150405")+"-"+name)
	dst := base + ".md"
	for i := 2; ; i++ {
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			break
		}
		dst = fmt.Sprintf("%s-%d.md", base, i)
	}
	return dst, os.Rename(src, dst)
}
