// Package agents reads and writes Claude Code subagent files
// (markdown with YAML frontmatter) and manages the routing marker in them.
package agents

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/sametakofficial/claude-hybrid-router/internal/marker"
)

// Agent is one subagent file.
type Agent struct {
	Name        string `json:"name"`
	File        string `json:"file"`
	Description string `json:"description"`
	Tools       string `json:"tools"`
	Model       string `json:"model"`
	Color       string `json:"color"`
	Prompt      string `json:"prompt"`
	// Extra keeps frontmatter keys we do not edit, so saving never drops them.
	Extra map[string]any `json:"-"`
}

var known = map[string]bool{"name": true, "description": true, "tools": true, "model": true, "color": true}

// Parse reads an agent file's content. The marker is removed from Prompt.
func Parse(file string, data []byte) (Agent, error) {
	a := Agent{File: file, Name: strings.TrimSuffix(filepath.Base(file), ".md")}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	body := text
	if strings.HasPrefix(text, "---\n") {
		end := strings.Index(text[4:], "\n---")
		if end < 0 {
			return a, fmt.Errorf("%s: frontmatter is not closed", file)
		}
		front := text[4 : 4+end]
		body = text[4+end+4:]
		body = strings.TrimPrefix(body, "\n")
		var m map[string]any
		if err := yaml.Unmarshal([]byte(front), &m); err != nil {
			return a, fmt.Errorf("%s: frontmatter: %w", file, err)
		}
		str := func(k string) string {
			switch v := m[k].(type) {
			case string:
				return v
			case []any: // tools may be a YAML list
				parts := make([]string, 0, len(v))
				for _, x := range v {
					parts = append(parts, fmt.Sprint(x))
				}
				return strings.Join(parts, ", ")
			case nil:
				return ""
			default:
				return fmt.Sprint(v)
			}
		}
		if n := str("name"); n != "" {
			a.Name = n
		}
		a.Description, a.Tools, a.Model, a.Color = str("description"), str("tools"), str("model"), str("color")
		for k, v := range m {
			if !known[k] {
				if a.Extra == nil {
					a.Extra = map[string]any{}
				}
				a.Extra[k] = v
			}
		}
	}
	body = marker.AgentRE.ReplaceAllString(body, "")
	a.Prompt = strings.Trim(body, "\n")
	return a, nil
}

// Render produces file content with frontmatter and the routing marker.
func Render(a Agent) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("---\n")
	write := func(k string, v any) error {
		out, err := yaml.Marshal(map[string]any{k: v})
		if err != nil {
			return err
		}
		buf.Write(out)
		return nil
	}
	fields := []struct{ k, v string }{
		{"name", a.Name}, {"description", a.Description}, {"tools", a.Tools}, {"model", a.Model}, {"color", a.Color},
	}
	for _, f := range fields {
		if f.v == "" && f.k != "name" && f.k != "description" {
			continue
		}
		if err := write(f.k, f.v); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(a.Extra))
	for k := range a.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := write(k, a.Extra[k]); err != nil {
			return nil, err
		}
	}
	buf.WriteString("---\n")
	buf.WriteString(marker.Agent(a.Name) + "\n\n")
	buf.WriteString(strings.Trim(a.Prompt, "\n"))
	buf.WriteString("\n")
	return buf.Bytes(), nil
}

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

// Get loads one agent by name.
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
	dst := filepath.Join(d.TrashDir, time.Now().Format("20060102-150405")+"-"+name+".md")
	return dst, os.Rename(src, dst)
}
