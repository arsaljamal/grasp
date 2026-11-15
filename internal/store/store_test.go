package store

import (
	"strings"
	"testing"

	"github.com/arsaljamal/grasp/internal/session"
)

func TestReviewAndFeedback(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Import("../../testdata/decided.jsonl"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Review("d1", "ana", Rejected, true, "Use the shared limiter in pkg/ratelimit"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Review("d1", "ana", "maybe", true, ""); err == nil {
		t.Fatal("unknown verdict accepted")
	}
	fb, err := st.Feedback()
	if err != nil || len(fb) != 1 || fb[0].Verdict != Rejected {
		t.Fatalf("feedback = %+v, %v", fb, err)
	}
	md := FeedbackMarkdown(fb)
	if !strings.Contains(md, "REJECTED: Token bucket per client IP") || !strings.Contains(md, "pkg/ratelimit") {
		t.Fatal(md)
	}
}

func TestLoggedDecisionsWithoutTranscript(t *testing.T) {
	st, _ := Open(t.TempDir())
	if _, err := st.LogDecision(session.DecisionInput{Title: "Cache in Redis", Why: "hot reads", Kind: "performance"}); err != nil {
		t.Fatal(err)
	}
	se, err := st.Session("agent-log")
	if err != nil || len(se.Decisions) != 1 || se.Decisions[0].Title != "Cache in Redis" {
		t.Fatalf("%+v %v", se, err)
	}
}
