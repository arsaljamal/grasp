// Package session turns a coding agent's raw work log into a reviewable
// session: a timeline of events and the decisions behind the changes.
package session

import (
	"time"

	"github.com/arsaljamal/grasp/internal/behavior"
)

// Event kinds, in the order a reviewer usually cares about them.
const (
	KindPrompt   = "prompt"   // what the human asked for
	KindSay      = "say"      // what the agent told the human
	KindDecision = "decision" // the agent recorded a decision
	KindFeedback = "feedback" // the agent read reviewer feedback
	KindBehavior = "behavior" // grasp ran an example on old and new code
	KindEdit     = "edit"     // a file changed
	KindCommand  = "command"  // a shell command ran
	KindRead     = "read"     // a file was read
	KindTool     = "tool"     // any other tool call
)

// Event is one step of the agent's work, in order.
type Event struct {
	Seq        int       `json:"seq"`
	Time       time.Time `json:"time"`
	Kind       string    `json:"kind"`
	Text       string    `json:"text,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	File       string    `json:"file,omitempty"`
	Before     string    `json:"before,omitempty"`
	After      string    `json:"after,omitempty"`
	NewFile    bool      `json:"new_file,omitempty"`
	Failed     bool      `json:"failed,omitempty"`
	DecisionID string    `json:"decision_id,omitempty"`
	Turn       int       `json:"turn"`
}

// Decision kinds an agent can record.
var DecisionKinds = []string{
	"architecture", "data-model", "api", "business-rule",
	"dependency", "security", "performance", "testing", "quality", "other",
}

// Decision is a choice the agent made, with the changes that carry it out.
type Decision struct {
	ID           string   `json:"id"`
	Seq          int      `json:"seq"`
	Turn         int      `json:"turn"`
	Title        string   `json:"title"`
	Kind         string   `json:"kind"`
	Why          string   `json:"why,omitempty"`
	Alternatives []string `json:"alternatives,omitempty"`
	Risk         string   `json:"risk,omitempty"`
	Files        []string `json:"files"`
	// Edits are the Seq numbers of edit events linked to this decision.
	Edits []int `json:"edits"`
	// Inferred is true when grasp grouped the edits itself because the
	// agent recorded no decision for them.
	Inferred bool `json:"inferred,omitempty"`
	// Unverified lists files the agent named but never changed.
	Unverified []string `json:"unverified,omitempty"`
}

// Behavior is an example the agent asked grasp to run on the code before
// and after its changes, with the real output of both runs.
type Behavior struct {
	ID         string `json:"id"`
	Seq        int    `json:"seq"`
	Title      string `json:"title"`
	Command    string `json:"command"`
	Expect     string `json:"expect,omitempty"`
	Decision   string `json:"decision,omitempty"`    // decision title the agent named
	DecisionID string `json:"decision_id,omitempty"` // the decision it was linked to
	behavior.Result
}

// Session is everything a reviewer needs for one agent run.
type Session struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Agent     string     `json:"agent"`
	Dir       string     `json:"dir"`
	Source    string     `json:"source,omitempty"`
	Started   time.Time  `json:"started"`
	Updated   time.Time  `json:"updated"`
	Events    []Event    `json:"events"`
	Decisions []Decision `json:"decisions"`
	Behaviors []Behavior `json:"behaviors"`
	// Unexplained are edit Seqs no decision accounts for.
	Unexplained []int `json:"unexplained"`
}
