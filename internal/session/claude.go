package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DecisionTool is the MCP tool name suffix agents call to record a decision.
const DecisionTool = "record_decision"

// DecisionInput is what an agent passes to record_decision.
type DecisionInput struct {
	Title        string   `json:"title" jsonschema:"one line: what was decided"`
	Kind         string   `json:"kind" jsonschema:"architecture, data-model, api, business-rule, dependency, security, performance, testing, quality or other"`
	Why          string   `json:"why" jsonschema:"the reason, in plain words"`
	Files        []string `json:"files" jsonschema:"files this decision changes"`
	Alternatives []string `json:"alternatives,omitempty" jsonschema:"options considered and not chosen"`
	Risk         string   `json:"risk,omitempty" jsonschema:"what could go wrong, if anything"`
}

type line struct {
	Type        string          `json:"type"`
	Timestamp   time.Time       `json:"timestamp"`
	SessionID   string          `json:"sessionId"`
	Cwd         string          `json:"cwd"`
	IsSidechain bool            `json:"isSidechain"`
	IsMeta      bool            `json:"isMeta"`
	AITitle     string          `json:"aiTitle"`
	Message     json.RawMessage `json:"message"`
}

type message struct {
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
}

// ParseClaudeFile reads a Claude Code transcript (.jsonl).
func ParseClaudeFile(path string) (*Session, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s, err := ParseClaude(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.Source = path
	return s, nil
}

// ParseClaude reads a Claude Code transcript and links edits to decisions.
func ParseClaude(r io.Reader) (*Session, error) {
	s := &Session{Agent: "claude-code"}
	b := &builder{s: s, toolIdx: map[string]int{}}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		b.add(l)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if s.ID == "" {
		return nil, fmt.Errorf("no Claude Code session found")
	}
	Link(s)
	return s, nil
}

type builder struct {
	s       *Session
	turn    int
	toolIdx map[string]int // tool_use id -> event index
}

func (b *builder) add(l line) {
	s := b.s
	if l.AITitle != "" {
		s.Title = l.AITitle
	}
	if l.IsSidechain || l.IsMeta || len(l.Message) == 0 || (l.Type != "user" && l.Type != "assistant") {
		return
	}
	if s.ID == "" {
		s.ID, s.Dir, s.Started = l.SessionID, l.Cwd, l.Timestamp
	}
	if !l.Timestamp.IsZero() {
		s.Updated = l.Timestamp
	}
	var m message
	if json.Unmarshal(l.Message, &m) != nil {
		return
	}
	var blocks []block
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		blocks = []block{{Type: "text", Text: text}}
	} else if json.Unmarshal(m.Content, &blocks) != nil {
		return
	}
	for _, bl := range blocks {
		switch {
		case l.Type == "user" && bl.Type == "text":
			t := strings.TrimSpace(bl.Text)
			if t == "" || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "[Request interrupted") {
				continue
			}
			b.turn++
			if s.Title == "" {
				s.Title = firstLine(t, 80)
			}
			b.emit(Event{Time: l.Timestamp, Kind: KindPrompt, Text: t})
		case l.Type == "user" && bl.Type == "tool_result":
			if i, ok := b.toolIdx[bl.ToolUseID]; ok && bl.IsError {
				s.Events[i].Failed = true
			}
		case l.Type == "assistant" && bl.Type == "text":
			if t := strings.TrimSpace(bl.Text); t != "" {
				b.emit(Event{Time: l.Timestamp, Kind: KindSay, Text: t})
			}
		case l.Type == "assistant" && bl.Type == "tool_use":
			b.toolIdx[bl.ID] = len(s.Events)
			b.tool(l.Timestamp, bl)
		}
	}
}

func (b *builder) emit(e Event) {
	e.Seq = len(b.s.Events) + 1
	e.Turn = b.turn
	b.s.Events = append(b.s.Events, e)
}

func (b *builder) tool(t time.Time, bl block) {
	var in map[string]any
	_ = json.Unmarshal(bl.Input, &in)
	str := func(k string) string { v, _ := in[k].(string); return v }
	file := b.rel(str("file_path"))
	switch {
	case bl.Name == "Edit":
		b.emit(Event{Time: t, Kind: KindEdit, Tool: bl.Name, File: file, Before: str("old_string"), After: str("new_string")})
	case bl.Name == "MultiEdit":
		edits, _ := in["edits"].([]any)
		var before, after []string
		for _, e := range edits {
			m, _ := e.(map[string]any)
			o, _ := m["old_string"].(string)
			n, _ := m["new_string"].(string)
			before, after = append(before, o), append(after, n)
		}
		b.emit(Event{Time: t, Kind: KindEdit, Tool: bl.Name, File: file,
			Before: strings.Join(before, "\n…\n"), After: strings.Join(after, "\n…\n")})
	case bl.Name == "Write":
		b.emit(Event{Time: t, Kind: KindEdit, Tool: bl.Name, File: file, After: str("content"), NewFile: true})
	case bl.Name == "Bash":
		b.emit(Event{Time: t, Kind: KindCommand, Tool: bl.Name, Text: str("command")})
	case bl.Name == "Read":
		b.emit(Event{Time: t, Kind: KindRead, Tool: bl.Name, File: file})
	case strings.HasSuffix(bl.Name, DecisionTool):
		var d DecisionInput
		_ = json.Unmarshal(bl.Input, &d)
		b.emit(Event{Time: t, Kind: KindDecision, Tool: bl.Name, Text: d.Title, DecisionID: bl.ID})
		for i, f := range d.Files {
			d.Files[i] = b.rel(f)
		}
		b.s.Decisions = append(b.s.Decisions, Decision{
			ID: bl.ID, Seq: len(b.s.Events), Turn: b.turn, Title: d.Title, Kind: normKind(d.Kind),
			Why: d.Why, Alternatives: d.Alternatives, Risk: d.Risk, Files: d.Files,
		})
	case strings.HasSuffix(bl.Name, "get_review_feedback"):
		b.emit(Event{Time: t, Kind: KindFeedback, Tool: bl.Name})
	default:
		b.emit(Event{Time: t, Kind: KindTool, Tool: bl.Name, Text: firstLine(string(bl.Input), 160)})
	}
}

func (b *builder) rel(p string) string {
	if p == "" || b.s.Dir == "" || !filepath.IsAbs(p) {
		return filepath.ToSlash(p)
	}
	if r, err := filepath.Rel(b.s.Dir, p); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return filepath.ToSlash(p)
}

func normKind(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	for _, v := range DecisionKinds {
		if k == v {
			return k
		}
	}
	return "other"
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > max {
		s = string(r[:max-1]) + "…"
	}
	return s
}
