// Package mcpserver is the bridge an agent talks to: it records the
// agent's decisions and hands back what reviewers said about them.
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arsaljamal/grasp/internal/session"
	"github.com/arsaljamal/grasp/internal/store"
)

// Instructions tell the agent when to use the tools.
const Instructions = `grasp lets human reviewers see and judge your work as decisions, not just diffs.
Before you change code for a non-trivial reason, call record_decision once per decision: what you decided, why, the files it touches, alternatives you rejected, and any risk. Small mechanical edits (typos, formatting) need no decision.
At the start of a task, call get_review_feedback and follow it.`

// New builds the MCP server over a store.
func New(st *store.Store, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "grasp", Version: version}, &mcp.ServerOptions{Instructions: Instructions})

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

type recordOut struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type feedbackOut struct {
	Items []store.Feedback `json:"items"`
}
