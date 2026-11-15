// Package store keeps grasp's state in a .grasp folder inside the project:
// parsed sessions, reviewer verdicts and comments, and decisions recorded by
// agents that have no transcript grasp can read.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/arsaljamal/grasp/internal/session"
)

// Verdicts a reviewer can give a decision.
const (
	Approved = "approved"
	Rejected = "rejected"
	Pending  = ""
)

// Comment is one reviewer note on a decision.
type Comment struct {
	Author string    `json:"author"`
	Text   string    `json:"text"`
	Time   time.Time `json:"time"`
}

// Review is the reviewers' state for one decision.
type Review struct {
	Verdict  string    `json:"verdict"`
	By       string    `json:"by,omitempty"`
	Comments []Comment `json:"comments,omitempty"`
	// Expected records, for a behavior example, whether the reviewer's
	// prediction matched what the new code did.
	Expected *bool     `json:"expected,omitempty"`
	Updated  time.Time `json:"updated"`
}

// Store is a .grasp folder.
type Store struct {
	Dir string
	mu  sync.Mutex
}

// Open returns the store for a project directory, creating .grasp if needed.
func Open(projectDir string) (*Store, error) {
	d := filepath.Join(projectDir, ".grasp")
	if err := os.MkdirAll(filepath.Join(d, "sessions"), 0o755); err != nil {
		return nil, err
	}
	return &Store{Dir: d}, nil
}

// SaveSession writes a parsed session.
func (s *Store) SaveSession(se *session.Session) error {
	return writeJSON(filepath.Join(s.Dir, "sessions", safe(se.ID)+".json"), se)
}

// Import parses a Claude Code transcript and saves it.
func (s *Store) Import(transcript string) (*session.Session, error) {
	se, err := session.ParseClaudeFile(transcript)
	if err != nil {
		return nil, err
	}
	return se, s.SaveSession(se)
}

// Refresh re-reads each saved session's transcript, so a running agent's new
// work shows up without waiting for a hook.
func (s *Store) Refresh() {
	list, _ := s.Sessions()
	for _, se := range list {
		if se.Source == "" {
			continue
		}
		src, err := os.Stat(se.Source)
		if err != nil {
			continue
		}
		saved, err := os.Stat(filepath.Join(s.Dir, "sessions", safe(se.ID)+".json"))
		if err == nil && !src.ModTime().After(saved.ModTime()) {
			continue
		}
		_, _ = s.Import(se.Source)
	}
}

// Sessions lists saved sessions, newest first, plus a session built from
// decisions agents recorded over MCP without a transcript.
func (s *Store) Sessions() ([]*session.Session, error) {
	files, err := filepath.Glob(filepath.Join(s.Dir, "sessions", "*.json"))
	if err != nil {
		return nil, err
	}
	var out []*session.Session
	seen := map[string]bool{}
	for _, f := range files {
		var se session.Session
		if readJSON(f, &se) == nil {
			out = append(out, &se)
			for _, d := range se.Decisions {
				seen[d.Title] = true
			}
		}
	}
	if log := s.agentLog(seen); log != nil {
		out = append(out, log)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Session returns one session by ID, or the newest when id is empty.
func (s *Store) Session(id string) (*session.Session, error) {
	list, err := s.Sessions()
	if err != nil {
		return nil, err
	}
	for _, se := range list {
		if id == "" || se.ID == id {
			return se, nil
		}
	}
	return nil, os.ErrNotExist
}

// LoggedDecision is a decision recorded over MCP, kept in decisions.jsonl.
type LoggedDecision struct {
	session.DecisionInput
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
}

// LogDecision appends a decision an agent recorded over MCP.
func (s *Store) LogDecision(in session.DecisionInput) (LoggedDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := LoggedDecision{DecisionInput: in, Time: time.Now().UTC()}
	d.ID = fmt.Sprintf("log-%d", d.Time.UnixNano())
	f, err := os.OpenFile(filepath.Join(s.Dir, "decisions.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return d, err
	}
	defer f.Close()
	return d, json.NewEncoder(f).Encode(d)
}

// agentLog turns decisions.jsonl entries that no transcript already holds
// into a session of their own.
func (s *Store) agentLog(seen map[string]bool) *session.Session {
	b, err := os.ReadFile(filepath.Join(s.Dir, "decisions.jsonl"))
	if err != nil {
		return nil
	}
	se := &session.Session{ID: "agent-log", Title: "Decisions recorded over MCP", Agent: "mcp"}
	for _, ln := range strings.Split(string(b), "\n") {
		var d LoggedDecision
		if json.Unmarshal([]byte(ln), &d) != nil || seen[d.Title] {
			continue
		}
		if se.Started.IsZero() {
			se.Started = d.Time
		}
		se.Updated = d.Time
		seq := len(se.Events) + 1
		se.Events = append(se.Events, session.Event{Seq: seq, Time: d.Time, Kind: session.KindDecision, Text: d.Title, DecisionID: d.ID})
		se.Decisions = append(se.Decisions, session.Decision{
			ID: d.ID, Seq: seq, Title: d.Title, Kind: d.Kind, Why: d.Why,
			Alternatives: d.Alternatives, Risk: d.Risk, Files: d.Files, Unverified: d.Files,
		})
	}
	if len(se.Decisions) == 0 {
		return nil
	}
	return se
}

// Reviews returns all reviews keyed by decision ID.
func (s *Store) Reviews() (map[string]*Review, error) {
	m := map[string]*Review{}
	err := readJSON(filepath.Join(s.Dir, "reviews.json"), &m)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return m, err
}

// Review sets a verdict and/or adds a comment. An empty verdict with
// setVerdict true resets the decision to pending.
func (s *Store) Review(decisionID, author, verdict string, setVerdict bool, comment string) (*Review, error) {
	if verdict != Approved && verdict != Rejected && verdict != Pending {
		return nil, fmt.Errorf("unknown verdict %q", verdict)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.Reviews()
	if err != nil {
		return nil, err
	}
	r := m[decisionID]
	if r == nil {
		r = &Review{}
		m[decisionID] = r
	}
	now := time.Now().UTC()
	if setVerdict {
		r.Verdict, r.By = verdict, author
	}
	if c := strings.TrimSpace(comment); c != "" {
		r.Comments = append(r.Comments, Comment{Author: author, Text: c, Time: now})
	}
	r.Updated = now
	return r, writeJSON(filepath.Join(s.Dir, "reviews.json"), m)
}

// SetExpected records whether a behavior example matched the reviewer's
// prediction.
func (s *Store) SetExpected(id, author string, expected bool) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.Reviews()
	if err != nil {
		return nil, err
	}
	r := m[id]
	if r == nil {
		r = &Review{}
		m[id] = r
	}
	r.Expected, r.By, r.Updated = &expected, author, time.Now().UTC()
	return r, writeJSON(filepath.Join(s.Dir, "reviews.json"), m)
}

// Feedback is what reviewers said about one decision, for the agent to read.
type Feedback struct {
	Session  string    `json:"session"`
	Decision string    `json:"decision"`
	Kind     string    `json:"kind"`
	Files    []string  `json:"files"`
	Verdict  string    `json:"verdict"`
	Comments []Comment `json:"comments,omitempty"`
}

// Feedback lists every decision a reviewer rejected or commented on.
func (s *Store) Feedback() ([]Feedback, error) {
	reviews, err := s.Reviews()
	if err != nil {
		return nil, err
	}
	list, err := s.Sessions()
	if err != nil {
		return nil, err
	}
	var out []Feedback
	for _, se := range list {
		for _, d := range se.Decisions {
			r := reviews[d.ID]
			if r == nil || (r.Verdict != Rejected && len(r.Comments) == 0) {
				continue
			}
			v := r.Verdict
			if v == Pending {
				v = "comment"
			}
			out = append(out, Feedback{Session: se.Title, Decision: d.Title, Kind: d.Kind, Files: d.Files, Verdict: v, Comments: r.Comments})
		}
	}
	return out, nil
}

// FeedbackMarkdown renders feedback as instructions an agent can follow.
func FeedbackMarkdown(fb []Feedback) string {
	if len(fb) == 0 {
		return "No reviewer feedback yet.\n"
	}
	var b strings.Builder
	b.WriteString("# Reviewer feedback\n\nReviewers looked at these decisions. Follow this feedback in future work; for rejected decisions, change the code or explain why it should stay.\n")
	for _, f := range fb {
		fmt.Fprintf(&b, "\n## %s: %s\n", strings.ToUpper(f.Verdict), f.Decision)
		if len(f.Files) > 0 {
			fmt.Fprintf(&b, "Files: %s\n", strings.Join(f.Files, ", "))
		}
		for _, c := range f.Comments {
			fmt.Fprintf(&b, "- %s: %s\n", c.Author, c.Text)
		}
	}
	return b.String()
}

// Stamp returns a value that changes whenever anything in the store changes.
func (s *Store) Stamp() int64 {
	var latest int64
	_ = filepath.Walk(s.Dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.ModTime().UnixNano() > latest {
			latest = fi.ModTime().UnixNano()
		}
		return nil
	})
	return latest
}

func safe(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == '.' {
			return '_'
		}
		return r
	}, id)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
