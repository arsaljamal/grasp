package impact

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arsaljamal/grasp/internal/session"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindsRipplesAndTestChanges(t *testing.T) {
	dir := t.TempDir()
	// the project as it is after the agent's work
	write(t, dir, "api.go", "package api\n\nfunc normalizeTitle(s string) string {\n\treturn strings.ToLower(strings.TrimSpace(s))\n}\n")
	write(t, dir, "bulk.go", "package api\n\nfunc importAll(xs []string) {\n\tfor _, x := range xs {\n\t\tsave(normalizeTitle(x))\n\t}\n}\n")
	write(t, dir, "cli.go", "package api\n\nfunc show(id int) string { return legacyID(id) }\n")
	write(t, dir, "api_test.go", "package api\n\nfunc TestNormalize(t *testing.T) {}\n")
	write(t, dir, "new_test.go", "package api\n")

	s := &session.Session{Dir: dir, Events: []session.Event{
		{Seq: 1, Kind: session.KindEdit, File: "api.go", DecisionID: "d1",
			Before: "\treturn strings.TrimSpace(s)", After: "\treturn strings.ToLower(strings.TrimSpace(s))"},
		{Seq: 2, Kind: session.KindEdit, File: "api.go", DecisionID: "d2",
			Before: "func legacyID(id int) string { return fmt.Sprint(id) }\n", After: ""},
		{Seq: 3, Kind: session.KindEdit, File: "api_test.go", DecisionID: "d1",
			Before: "\tif got := normalizeTitle(\" A \"); got != \"A\" {\n\t\tt.Fatalf(\"got %q\", got)\n\t}\n", After: ""},
		{Seq: 4, Kind: session.KindEdit, File: "new_test.go", NewFile: true, After: "package api\n"},
		{Seq: 5, Kind: session.KindEdit, File: "new_test.go", Before: "package api\n", After: "package api\n// more\n"},
		{Seq: 6, Kind: session.KindEdit, File: "more_test.go", Before: "}\n", After: "}\n\nfunc TestExtra(t *testing.T) {}\n"}, // only adds a test
	}}
	r := Analyze(s)

	if len(r.Tests) != 1 || r.Tests[0].File != "api_test.go" || r.Tests[0].RemovedChecks < 2 {
		t.Fatalf("tests = %+v (new_test.go was created in the session and must not count)", r.Tests)
	}
	if len(r.Ripples) != 2 {
		t.Fatalf("ripples = %+v", r.Ripples)
	}
	if rp := r.Ripples[0]; rp.Symbol != "legacyID" || !rp.Removed || rp.Uses[0].File != "cli.go" {
		t.Fatalf("removed function still used should come first: %+v", rp)
	}
	if rp := r.Ripples[1]; rp.Symbol != "normalizeTitle" || rp.Decision != "d1" || rp.Uses[0].File != "bulk.go" || rp.Uses[0].Line != 5 {
		t.Fatalf("changed function used by an untouched file: %+v", rp)
	}
	if r.Searched != 2 {
		t.Fatalf("searched %d files, want the 2 untouched ones (bulk.go, cli.go)", r.Searched)
	}
}

func TestMissingProject(t *testing.T) {
	r := Analyze(&session.Session{Dir: filepath.Join(t.TempDir(), "gone")})
	if r.Note == "" {
		t.Fatal("expected a note when the project folder is missing")
	}
}

// A new function added right after an existing one must not make the
// existing one look changed (this was a false alarm on a real session).
func TestNewFunctionBelowExistingIsNotARipple(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "main.go", "package main\n\nfunc validateTitle(s string) string {\n\treturn s\n}\n\n// validateCreateTitle adds stricter rules.\nfunc validateCreateTitle(s string) string {\n\treturn validateTitle(s)\n}\n\nfunc create() {\n\tvalidateCreateTitle(\"x\")\n}\n")
	write(t, dir, "importer.go", "package main\n\nfunc importAll() { validateTitle(\"y\") }\n")
	s := &session.Session{Dir: dir, Events: []session.Event{
		{Seq: 1, Kind: session.KindEdit, File: "main.go", Before: "\treturn s\n}\n", After: "\treturn s\n}\n\n// validateCreateTitle adds stricter rules.\nfunc validateCreateTitle(s string) string {\n\treturn validateTitle(s)\n}\n"},
		{Seq: 2, Kind: session.KindEdit, File: "main.go", Before: "\tvalidateTitle(\"x\")", After: "\tvalidateCreateTitle(\"x\")"},
	}}
	r := Analyze(s)
	for _, rp := range r.Ripples {
		if rp.Symbol == "validateTitle" {
			t.Fatalf("validateTitle was not changed but was reported: %+v", rp)
		}
	}
}
