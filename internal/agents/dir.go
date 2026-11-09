package agents

import (
	"errors"
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

// ErrExists is returned when a rename would overwrite another agent.
var ErrExists = errors.New("an agent with that name already exists")

// Conflict reports whether saving oldName as newName would overwrite a
// different agent. A rename that only changes letter case is not a
// conflict, also on case-insensitive filesystems where both names open the
// same file.
func (d Dir) Conflict(oldName, newName string) bool {
	if oldName == newName {
		return false
	}
	nfi, err := os.Stat(d.file(newName))
	if err != nil {
		return false
	}
	ofi, err := os.Stat(d.file(oldName))
	return err != nil || !os.SameFile(nfi, ofi)
}

// Save writes an agent as NAME.md (frontmatter name and marker = NAME).
// oldName is the agent's current name ("" or equal for a plain save); on a
// rename the old file is moved to the trash, never left in place. Unedited
// frontmatter keys of the existing file are kept.
//
// The content goes to a temp file first and is renamed into place, so a
// reader never sees half a file. A case-only rename on a case-insensitive
// filesystem (a.md -> A.md, one file) trashes the old file first, then
// renames the temp file to the new name.
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
	rename := oldName != a.Name
	if rename && d.Conflict(oldName, a.Name) {
		return Agent{}, fmt.Errorf("%w: %q", ErrExists, a.Name)
	}
	sameFile := false
	if rename {
		nfi, err1 := os.Stat(d.file(a.Name))
		ofi, err2 := os.Stat(d.file(oldName))
		sameFile = err1 == nil && err2 == nil && os.SameFile(nfi, ofi)
	}
	data, err := Render(a)
	if err != nil {
		return Agent{}, err
	}
	if err := os.MkdirAll(d.Path, 0o755); err != nil {
		return Agent{}, err
	}
	tmp, err := os.CreateTemp(d.Path, ".claude-hybrid-*.tmp")
	if err != nil {
		return Agent{}, err
	}
	defer os.Remove(tmp.Name()) // our own temp file; gone after the rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return Agent{}, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return Agent{}, err
	}
	if err := tmp.Close(); err != nil {
		return Agent{}, err
	}
	a.File = d.file(a.Name)
	var trashed string
	if sameFile {
		if trashed, err = d.Trash(oldName); err != nil {
			return Agent{}, err
		}
	}
	if err := os.Rename(tmp.Name(), a.File); err != nil {
		if trashed != "" {
			os.Rename(trashed, d.file(oldName)) // put the old file back
		}
		return Agent{}, err
	}
	if rename && !sameFile {
		if _, err := d.Trash(oldName); err != nil && !os.IsNotExist(err) {
			return a, fmt.Errorf("saved %s.md, but could not move %s.md to the trash: %w", a.Name, oldName, err)
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
