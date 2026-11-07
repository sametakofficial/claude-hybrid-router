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
