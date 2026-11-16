package demo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arsaljamal/grasp/internal/impact"
	"github.com/arsaljamal/grasp/internal/store"
)

func TestSetup(t *testing.T) {
	dir, err := Setup()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	list, err := st.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(list))
	}
	for _, se := range list {
		if !strings.HasPrefix(se.Dir, dir) || se.Source != "" {
			t.Errorf("%s: dir %q source %q", se.ID, se.Dir, se.Source)
		}
		if len(se.Decisions) == 0 || len(se.Behaviors) == 0 {
			t.Errorf("%s: %d decisions, %d examples", se.ID, len(se.Decisions), len(se.Behaviors))
		}
	}
	// The cleanup sample shows a real ripple into importer.go.
	se, err := st.Session("demo-cleanup")
	if err != nil {
		t.Fatal(err)
	}
	r := impact.Analyze(se)
	if len(r.Ripples) != 1 || len(r.Ripples[0].Uses) == 0 || r.Ripples[0].Uses[0].File != "importer.go" {
		t.Fatalf("want a ripple into importer.go, got %+v", r.Ripples)
	}
	if _, err := os.Stat(filepath.Join(dir, "cleanup", "importer.go")); err != nil {
		t.Fatal(err)
	}
}
