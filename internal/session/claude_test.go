package session

import (
	"reflect"
	"testing"
)

func TestRecordedDecisionsLinkEdits(t *testing.T) {
	s, err := ParseClaudeFile("../../testdata/decided.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Rate limiter" || s.ID != "s1" {
		t.Fatalf("title/id = %q/%q", s.Title, s.ID)
	}
	if len(s.Decisions) != 1 {
		t.Fatalf("decisions = %d, want 1", len(s.Decisions))
	}
	d := s.Decisions[0]
	if d.Kind != "architecture" || !reflect.DeepEqual(d.Files, []string{"api/limit.go", "api/router.go"}) {
		t.Fatalf("decision = %+v", d)
	}
	if s.Events[d.Edits[0]-1].File != "api/limit.go" || !s.Events[d.Edits[0]-1].NewFile {
		t.Fatalf("edits = %v", d.Edits)
	}
	if len(d.Edits) != 2 || len(d.Unverified) != 0 {
		t.Fatalf("edits = %v, unverified = %v", d.Edits, d.Unverified)
	}
	if w := s.Events[d.Edits[1]-1]; w.NewFile || w.Before != "package api // old\n" {
		t.Fatalf("rewrite read as %+v", w)
	}
	if len(s.Unexplained) != 1 || s.Events[s.Unexplained[0]-1].File != "config.yaml" {
		t.Fatalf("unexplained = %v", s.Unexplained)
	}
	var failed, side bool
	for _, e := range s.Events {
		if e.Kind == KindCommand && e.Failed {
			failed = true
		}
		if e.File == "side.go" {
			side = true
		}
	}
	if !failed || side {
		t.Fatalf("failed command marked=%v, sidechain included=%v", failed, side)
	}
}

func TestInferredDecisionsWhenNoneRecorded(t *testing.T) {
	s, err := ParseClaudeFile("../../testdata/inferred.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Decisions) != 2 {
		t.Fatalf("decisions = %d, want 2", len(s.Decisions))
	}
	if d := s.Decisions[0]; !d.Inferred || d.Title != "Renaming username to handle across the model" || len(d.Edits) != 2 || len(d.Files) != 1 {
		t.Fatalf("first = %+v", d)
	}
	if d := s.Decisions[1]; d.Title != "Now the migration" || d.Files[0] != "db/002.sql" {
		t.Fatalf("second = %+v", d)
	}
	if len(s.Unexplained) != 0 {
		t.Fatalf("unexplained = %v", s.Unexplained)
	}
}

// One rewrite of a file can carry out several decisions recorded before it;
// none of them should be flagged as unverified.
func TestOneEditServesSeveralDecisions(t *testing.T) {
	s := &Session{
		Events: []Event{
			{Seq: 1, Kind: KindDecision}, {Seq: 2, Kind: KindDecision},
			{Seq: 3, Kind: KindEdit, File: "main.go"},
			{Seq: 4, Kind: KindDecision},
			{Seq: 5, Kind: KindEdit, File: "main.go"},
		},
		Decisions: []Decision{
			{ID: "a", Seq: 1, Files: []string{"main.go"}},
			{ID: "b", Seq: 2, Files: []string{"main.go"}},
			{ID: "c", Seq: 4, Files: []string{"main.go"}},
		},
	}
	Link(s)
	want := map[string][]int{"a": {3}, "b": {3}, "c": {5}}
	for _, d := range s.Decisions {
		if !reflect.DeepEqual(d.Edits, want[d.ID]) || len(d.Unverified) != 0 {
			t.Errorf("%s: edits=%v unverified=%v, want edits=%v", d.ID, d.Edits, d.Unverified, want[d.ID])
		}
	}
}

func TestBehaviorEvidenceIsParsedAndLinked(t *testing.T) {
	s, err := ParseClaudeFile("../../testdata/decided.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Behaviors) != 1 {
		t.Fatalf("behaviors = %d", len(s.Behaviors))
	}
	b := s.Behaviors[0]
	if b.Title != "11th request is limited" || b.Before.Output != "201" || b.After.Output != "429" || !b.Changed {
		t.Fatalf("behavior = %+v", b)
	}
	if b.DecisionID != "d1" {
		t.Fatalf("linked to %q, want the earlier decision d1", b.DecisionID)
	}
}

func TestTestFilesGoWithTheCodeTheyTest(t *testing.T) {
	s := &Session{
		Events: []Event{
			{Seq: 1, Kind: KindDecision},
			{Seq: 2, Kind: KindEdit, File: "pkg/repo.go"},
			{Seq: 3, Kind: KindEdit, File: "pkg/repo_sqlmock_test.go"},
			{Seq: 4, Kind: KindEdit, File: "other/x_test.go"},
		},
		Decisions: []Decision{{ID: "d", Seq: 1, Files: []string{"pkg/repo.go"}}},
	}
	Link(s)
	if !reflect.DeepEqual(s.Decisions[0].Edits, []int{2, 3}) || !reflect.DeepEqual(s.Unexplained, []int{4}) {
		t.Fatalf("edits=%v unexplained=%v", s.Decisions[0].Edits, s.Unexplained)
	}
}
