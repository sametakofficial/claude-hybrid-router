package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripKeepsExtraKeysAndOneMarker(t *testing.T) {
	src := "---\nname: coder\ndescription: \"Writes code: fast\"\ntools: [Read, Bash]\nmemory: user\n---\n<!-- claude-hybrid:agent=coder -->\n\nYou are coder.\n"
	a, err := Parse("/x/coder.md", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if a.Tools != "Read, Bash" || a.Description != "Writes code: fast" || a.Prompt != "You are coder." || a.Extra["memory"] != "user" {
		t.Fatalf("parsed %+v", a)
	}
	out, err := Render(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "claude-hybrid:agent=coder") != 1 || !strings.Contains(string(out), "memory: user") {
		t.Fatalf("rendered:\n%s", out)
	}
	b, err := Parse("/x/coder.md", out)
	if err != nil || b.Prompt != a.Prompt || b.Description != a.Description {
		t.Fatalf("second parse %+v %v", b, err)
	}
}

func TestSaveRenameAndTrash(t *testing.T) {
	tmp := t.TempDir()
	d := Dir{Path: filepath.Join(tmp, "agents"), TrashDir: filepath.Join(tmp, "trash")}
	if _, err := d.Save("", Agent{Name: "a", Description: "d", Prompt: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Save("a", Agent{Name: "b", Description: "d2", Prompt: "p2"}); err != nil {
		t.Fatal(err)
	}
	list, _ := d.List()
	if len(list) != 1 || list[0].Name != "b" {
		t.Fatalf("list %+v", list)
	}
	trashed, _ := os.ReadDir(d.TrashDir)
	if len(trashed) != 1 {
		t.Fatal("renamed file should be in trash, not deleted")
	}
	if _, err := d.Trash("b"); err != nil {
		t.Fatal(err)
	}
	if list, _ := d.List(); len(list) != 0 {
		t.Fatal("trash should remove from list")
	}
}

func TestFileNameIsTheIdentity(t *testing.T) {
	tmp := t.TempDir()
	d := Dir{Path: filepath.Join(tmp, "agents"), TrashDir: filepath.Join(tmp, "trash")}
	os.MkdirAll(d.Path, 0o755)
	os.WriteFile(filepath.Join(d.Path, "file-name.md"), []byte("---\nname: front-name\ndescription: d\n---\nbody\n"), 0o644)
	list, _ := d.List()
	if len(list) != 1 || list[0].Name != "file-name" || !list[0].NameMismatch || list[0].FrontName != "front-name" {
		t.Fatalf("list %+v", list)
	}
	// Before: Name was "front-name", so Get/Trash looked for front-name.md.
	a, err := d.Get(list[0].Name)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := d.Save("file-name", a)
	if err != nil || saved.NameMismatch {
		t.Fatalf("%+v %v", saved, err)
	}
	data, _ := os.ReadFile(filepath.Join(d.Path, "file-name.md"))
	if !strings.Contains(string(data), "name: file-name") || !strings.Contains(string(data), "claude-hybrid:agent=file-name") {
		t.Fatalf("saving aligns the frontmatter:\n%s", data)
	}
	if _, err := d.Save("", Agent{Name: "../x", Description: "d"}); err == nil {
		t.Fatal("invalid name must be refused")
	}
}

func TestTrashTwiceInOneSecond(t *testing.T) {
	tmp := t.TempDir()
	d := Dir{Path: filepath.Join(tmp, "agents"), TrashDir: filepath.Join(tmp, "trash")}
	for i := 0; i < 2; i++ {
		d.Save("", Agent{Name: "a", Description: "d", Prompt: "p"})
		if _, err := d.Trash("a"); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.ReadDir(d.TrashDir); len(got) != 2 {
		t.Fatalf("second trash overwrote the first: %d files", len(got))
	}
}
