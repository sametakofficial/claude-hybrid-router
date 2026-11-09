package panel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/sametakofficial/claude-hybrid-router/internal/agents"
	"github.com/sametakofficial/claude-hybrid-router/internal/config"
)

type renameEnv struct {
	t     *testing.T
	p     *Panel
	h     func(method, path, body string) (int, string)
	dir   string
	trash string
}

func newRenameEnv(t *testing.T) *renameEnv {
	p, h, dir := setup(t)
	return &renameEnv{t: t, p: p, dir: dir, trash: config.TrashDir(),
		h: func(m, path, body string) (int, string) { return do(h, m, path, body, nil) }}
}

func (e *renameEnv) write(file, content string) {
	os.MkdirAll(e.dir, 0o755)
	if err := os.WriteFile(filepath.Join(e.dir, file), []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *renameEnv) files(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, x := range entries {
		out = append(out, x.Name())
	}
	sort.Strings(out)
	return out
}

func (e *renameEnv) route(name string) string {
	return e.p.Store.Current().Agents[name].Model
}

// checkAligned: frontmatter name == file name == marker == config key (case 6, 8).
func (e *renameEnv) checkAligned(name string) {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.dir, name+".md"))
	if err != nil {
		e.t.Fatal(err)
	}
	s := string(data)
	var front struct{ Name string }
	yaml.Unmarshal([]byte(strings.SplitN(strings.TrimPrefix(s, "---\n"), "\n---", 2)[0]), &front)
	if front.Name != name || strings.Count(s, "claude-hybrid:agent=") != 1 || !strings.Contains(s, "<!-- claude-hybrid:agent="+name+" -->") {
		e.t.Fatalf("%s.md not aligned:\n%s", name, s)
	}
	if _, ok := e.p.Store.Current().Agents[name]; !ok {
		e.t.Fatalf("config key %q missing: %v", name, e.p.Store.Current().Agents)
	}
}

func (e *renameEnv) list() []map[string]any {
	_, out := e.h("GET", "/api/agents", "")
	var l []map[string]any
	json.Unmarshal([]byte(out), &l)
	return l
}

func body(name, model string) string {
	return `{"name":"` + name + `","description":"d","prompt":"You work.","route":{"provider":"gpt","model":"` + model + `"}}`
}

func TestAgentRenameCases(t *testing.T) {
	t.Run("1 plain rename", func(t *testing.T) {
		e := newRenameEnv(t)
		e.h("PUT", "/api/agents/a", body("a", "m1"))
		code, out := e.h("PUT", "/api/agents/a", body("b", "m1"))
		if code != 200 || !strings.Contains(out, `"name":"b"`) {
			t.Fatalf("%d %s", code, out)
		}
		if got := e.files(e.dir); strings.Join(got, ",") != "b.md" {
			t.Fatalf("agents dir: %v", got)
		}
		if got := e.files(e.trash); len(got) != 1 || !strings.HasSuffix(got[0], "-a.md") {
			t.Fatalf("a.md must be in the trash: %v", got)
		}
		e.checkAligned("b")
		if _, ok := e.p.Store.Current().Agents["a"]; ok || e.route("b") != "m1" {
			t.Fatal("route must move from a to b")
		}
		if l := e.list(); len(l) != 1 || l[0]["name"] != "b" {
			t.Fatalf("list %v", l)
		}
	})

	t.Run("2 mismatched frontmatter", func(t *testing.T) {
		e := newRenameEnv(t)
		e.write("foo.md", "---\nname: bar\ndescription: d\n---\n<!-- claude-hybrid:agent=bar -->\n\nYou work.\n")
		e.h("PUT", "/api/agents/bar/route", `{"provider":"gpt","model":"old"}`) // route saved by an older version under "bar"
		l := e.list()
		if len(l) != 2 || l[1]["name"] != "foo" || l[1]["name_mismatch"] != true {
			t.Fatalf("list %v", l)
		}
		// Save under its id: frontmatter fixed, no second file, stale "bar" route gone.
		if code, out := e.h("PUT", "/api/agents/foo", body("foo", "m2")); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		if got := e.files(e.dir); strings.Join(got, ",") != "foo.md" {
			t.Fatalf("agents dir: %v", got)
		}
		e.checkAligned("foo")
		if _, ok := e.p.Store.Current().Agents["bar"]; ok {
			t.Fatal("stale route under the old frontmatter name should be removed")
		}
		if l := e.list(); len(l) != 1 || l[0]["name_mismatch"] != false {
			t.Fatalf("mismatch not fixed: %v", l)
		}

		// Same start, renamed to baz.
		e = newRenameEnv(t)
		e.write("foo.md", "---\nname: bar\ndescription: d\n---\nYou work.\n")
		e.h("PUT", "/api/agents/foo/route", `{"provider":"gpt","model":"m3"}`)
		if code, out := e.h("PUT", "/api/agents/foo", body("baz", "m3")); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		if got := e.files(e.dir); strings.Join(got, ",") != "baz.md" {
			t.Fatalf("agents dir (no bar.md, no foo.md): %v", got)
		}
		if got := e.files(e.trash); len(got) != 1 || !strings.HasSuffix(got[0], "-foo.md") {
			t.Fatalf("trash: %v", got)
		}
		e.checkAligned("baz")
		if _, ok := e.p.Store.Current().Agents["foo"]; ok {
			t.Fatal("route must move to baz")
		}
	})

	t.Run("3 rename onto an existing agent", func(t *testing.T) {
		e := newRenameEnv(t)
		e.h("PUT", "/api/agents/a", body("a", "ma"))
		e.h("PUT", "/api/agents/b", body("b", "mb"))
		before, _ := os.ReadFile(filepath.Join(e.dir, "b.md"))
		cfgBefore, _ := os.ReadFile(e.p.Store.Path())
		if code, out := e.h("PUT", "/api/agents/a", body("b", "ma")); code != 400 || !strings.Contains(out, "already exists") {
			t.Fatalf("%d %s", code, out)
		}
		after, _ := os.ReadFile(filepath.Join(e.dir, "b.md"))
		cfgAfter, _ := os.ReadFile(e.p.Store.Path())
		if string(before) != string(after) || string(cfgBefore) != string(cfgAfter) || strings.Join(e.files(e.dir), ",") != "a.md,b.md" || len(e.files(e.trash)) != 0 {
			t.Fatal("a refused rename must change nothing")
		}
	})

	t.Run("4 case-only rename", func(t *testing.T) {
		e := newRenameEnv(t)
		e.h("PUT", "/api/agents/a", body("a", "m4"))
		if code, out := e.h("PUT", "/api/agents/a", body("A", "m4")); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		if got := e.files(e.dir); strings.Join(got, ",") != "A.md" {
			t.Fatalf("agents dir: %v", got)
		}
		e.checkAligned("A")
	})

	t.Run("5 file write fails: route change is undone", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("needs a read-only directory")
		}
		e := newRenameEnv(t)
		e.h("PUT", "/api/agents/a", body("a", "m5"))
		os.Chmod(e.dir, 0o500)
		defer os.Chmod(e.dir, 0o755)
		code, out := e.h("PUT", "/api/agents/a", body("b", "m5"))
		if code != 500 || !strings.Contains(out, "nothing changed") {
			t.Fatalf("%d %s", code, out)
		}
		if e.route("a") != "m5" {
			t.Fatalf("route must be back under a: %v", e.p.Store.Current().Agents)
		}
		if _, ok := e.p.Store.Current().Agents["b"]; ok {
			t.Fatal("half-rename left in config")
		}
		if got := e.files(e.dir); strings.Join(got, ",") != "a.md" {
			t.Fatalf("agents dir: %v", got)
		}
	})

	t.Run("6 stale markers are replaced", func(t *testing.T) {
		e := newRenameEnv(t)
		e.write("x.md", "---\nname: x\ndescription: d\n---\n<!-- claude-hybrid:agent=old1 -->\n<!-- claude-hybrid:agent=old2 -->\n\nYou work.\n")
		if code, out := e.h("PUT", "/api/agents/x", body("y", "m6")); code != 200 {
			t.Fatalf("%d %s", code, out)
		}
		e.checkAligned("y") // exactly one marker, agent=y
	})

	t.Run("7 two trash moves in one second", func(t *testing.T) {
		e := newRenameEnv(t)
		for i := 0; i < 2; i++ {
			e.h("PUT", "/api/agents/a", body("a", "m7"))
			if code, _ := e.h("DELETE", "/api/agents/a", ""); code != 200 {
				t.Fatal(code)
			}
		}
		if got := e.files(e.trash); len(got) != 2 {
			t.Fatalf("trash: %v", got)
		}
	})
}

// A case-insensitive filesystem opens a.md and A.md as one file. A hard
// link gives the same picture on Linux: two names, one inode.
func TestCaseOnlyRenameOnCaseInsensitiveFS(t *testing.T) {
	tmp := t.TempDir()
	d := agents.Dir{Path: filepath.Join(tmp, "agents"), TrashDir: filepath.Join(tmp, "trash")}
	if _, err := d.Save("", agents.Agent{Name: "a", Description: "d", Prompt: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(d.Path, "a.md"), filepath.Join(d.Path, "A.md")); err != nil {
		t.Skip("no hard links here")
	}
	if d.Conflict("a", "A") {
		t.Fatal("same file under another case is not a conflict")
	}
	if _, err := d.Save("a", agents.Agent{Name: "A", Description: "d", Prompt: "new"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(d.Path, "A.md"))
	if !strings.Contains(string(data), "new") || !strings.Contains(string(data), "name: A") {
		t.Fatalf("A.md:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(d.Path, "a.md")); !os.IsNotExist(err) {
		t.Fatal("old name must be gone (moved to trash)")
	}
	if trashed, _ := os.ReadDir(d.TrashDir); len(trashed) != 1 {
		t.Fatal("old file must be in the trash")
	}
}
