// Package agents reads and writes Claude Code subagent files
// (markdown with YAML frontmatter) and manages the routing marker in them.
//
// Identity rule: an agent is identified by its file name without ".md".
// That name is the id in the API, the key of its route in config.yaml and
// the name in its routing marker. Saving writes the same name into the
// frontmatter; a file whose frontmatter name differs is listed with
// NameMismatch set until it is saved from the panel.
package agents

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sametakofficial/claude-hybrid-router/internal/marker"
)

// Agent is one subagent file.
type Agent struct {
	Name string `json:"name"` // file name without .md: the agent's id
	File string `json:"file"`
	// FrontName is the frontmatter "name" when it differs from Name.
	FrontName    string `json:"frontmatter_name,omitempty"`
	NameMismatch bool   `json:"name_mismatch"`
	Description  string `json:"description"`
	Tools        string `json:"tools"`
	Model        string `json:"model"`
	Color        string `json:"color"`
	Prompt       string `json:"prompt"`
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
		if n := str("name"); n != "" && n != a.Name {
			a.FrontName, a.NameMismatch = n, true
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
