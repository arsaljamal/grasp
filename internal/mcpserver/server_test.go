package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arsaljamal/grasp/internal/store"
)

func TestRecordThenFeedback(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ct, svt := mcp.NewInMemoryTransports()
	if _, err := New(st, "test").Connect(ctx, svt, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "agent"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "record_decision", Arguments: map[string]any{
		"title": "Cache user profiles in Redis", "kind": "performance", "why": "profile reads dominate", "files": []string{"cache.go"},
	}})
	if err != nil || res.IsError {
		t.Fatalf("record: %v %+v", err, res)
	}
	if res, _ := cs.CallTool(ctx, &mcp.CallToolParams{Name: "record_decision", Arguments: map[string]any{"title": "no reason"}}); !res.IsError {
		t.Fatal("decision without a reason was accepted")
	}

	se, err := st.Session("agent-log")
	if err != nil || len(se.Decisions) != 1 {
		t.Fatalf("logged: %+v %v", se, err)
	}
	if _, err := st.Review(se.Decisions[0].ID, "lee", store.Rejected, true, "We do not run Redis; use the in-process LRU"); err != nil {
		t.Fatal(err)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_review_feedback", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "REJECTED: Cache user profiles in Redis") || !strings.Contains(text, "in-process LRU") {
		t.Fatalf("feedback = %s", text)
	}
}
