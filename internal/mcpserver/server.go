// Package mcpserver is the bridge an agent talks to: it records the
// agent's decisions and hands back what reviewers said about them.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arsaljamal/grasp/internal/behavior"
	"github.com/arsaljamal/grasp/internal/session"
	"github.com/arsaljamal/grasp/internal/store"
)

// Instructions tell the agent when to use the tools.
const Instructions = `grasp lets human reviewers see and judge your work as decisions, not just diffs.
Before you change code for a non-trivial reason, call record_decision once per decision: what you decided, why, the files it touches, alternatives you rejected, and any risk. Small mechanical edits (typos, formatting) need no decision.
After implementing a decision that changes what the software does, call show_behavior with a command that demonstrates it. grasp runs the command on the code from before your session and on the code now, and shows reviewers both real outputs, so they can see the change without reading code. Make the command print one short line per case, like "empty title -> 400 title is required" (for an HTTP API: start the server, send the requests with curl, print status and the key part of the body, stop the server).
At the start of a task, call get_review_feedback and follow it.`

// Options configure the bridge.
type Options struct {
	Version string
	Dir     string        // project root, where examples run
	Base    string        // commit the "before" side of examples runs on
	Timeout time.Duration // per run
}

// New builds the MCP server over a store.
func New(st *store.Store, o Options) *mcp.Server {
	if o.Timeout == 0 {
		o.Timeout = 90 * time.Second
	}
	s := mcp.NewServer(&mcp.Implementation{Name: "grasp", Version: o.Version}, &mcp.ServerOptions{Instructions: Instructions})

	mcp.AddTool(s, &mcp.Tool{
		Name: session.DecisionTool,
		Description: "Record a decision before making the changes that carry it out, so reviewers can approve or reject it. " +
			"Use one call per decision (architecture, data model, API, business rule, dependency, security, performance, testing, quality).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in session.DecisionInput) (*mcp.CallToolResult, recordOut, error) {
		if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Why) == "" {
			return nil, recordOut{}, fmt.Errorf("title and why are required")
		}
		d, err := st.LogDecision(in)
		if err != nil {
			return nil, recordOut{}, err
		}
		return nil, recordOut{ID: d.ID, Status: "recorded; a reviewer will see it next to the changes it explains"}, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name: session.BehaviorTool,
		Description: "Show reviewers what a change does. grasp runs your command twice, on the code from before this session and on the code now, " +
			"and shows both real outputs side by side. The command runs from the project root with sh; it must start and stop anything it needs " +
			"and print a short result. Use one call per behavior you want to demonstrate.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in session.BehaviorInput) (*mcp.CallToolResult, behaviorOut, error) {
		if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Command) == "" {
			return nil, behaviorOut{}, fmt.Errorf("title and command are required")
		}
		out := behaviorOut{Result: behavior.Compare(ctx, o.Dir, o.Base, in.Command, o.Timeout)}
		out.Summary = summary(out.Result)
		b, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, out, nil
	})

	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_review_feedback",
		Description: "Get reviewer verdicts and comments on your earlier decisions. Rejected decisions and comments are instructions to follow.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, feedbackOut, error) {
		fb, err := st.Feedback()
		if err != nil {
			return nil, feedbackOut{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: store.FeedbackMarkdown(fb)}}},
			feedbackOut{Items: fb}, nil
	})
	return s
}

type behaviorOut struct {
	Summary string          `json:"summary"`
	Result  behavior.Result `json:"result"`
}

func summary(r behavior.Result) string {
	switch {
	case r.Before.Skipped != "":
		return "Ran on the new code only (" + r.Before.Skipped + ")."
	case !r.Changed:
		return "Old and new code printed the same thing: this example shows no change in behavior."
	case r.After.Exit != 0:
		return fmt.Sprintf("Behavior changed, but the new code exited with code %d. Check the command.", r.After.Exit)
	default:
		return "Behavior changed. Reviewers will see both outputs."
	}
}

type recordOut struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type feedbackOut struct {
	Items []store.Feedback `json:"items"`
}
