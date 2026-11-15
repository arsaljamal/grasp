package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestFromGit(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	put := func(name, body string) { os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644) }
	run("init", "-q")
	put("a.go", "package a\n")
	run("add", ".")
	run("commit", "-qm", "base")
	put("a.go", "package a\n\nfunc A() {}\n")
	put("b.go", "package a\n")
	run("add", ".")
	run("commit", "-qm", "feat: add A", "-m", "Because callers need it.")
	put("b.go", "package a\n\nfunc B() {}\n")
	run("commit", "-qam", "test: add B")

	s, err := FromGit(dir, "HEAD~2", "HEAD", "Add A and B", "PR body")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Decisions) != 2 || s.Decisions[0].Why != "Because callers need it." || s.Decisions[1].Kind != "testing" {
		t.Fatalf("decisions = %+v", s.Decisions)
	}
	var edits []Event
	for _, e := range s.Events {
		if e.Kind == KindEdit {
			edits = append(edits, e)
		}
	}
	if len(edits) != 2 || edits[0].File != "a.go" || edits[0].Before != "package a\n" || !edits[1].NewFile {
		t.Fatalf("edits = %+v", edits)
	}
	// b.go was touched by both commits, so its one edit serves both
	if len(s.Decisions[1].Edits) != 1 || len(s.Unexplained) != 0 {
		t.Fatalf("links: %+v unexplained %v", s.Decisions, s.Unexplained)
	}
}
