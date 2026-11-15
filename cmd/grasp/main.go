// Command grasp shows what a coding agent did as decisions a human can
// review, and feeds the reviewers' verdicts back to the agent.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arsaljamal/grasp/internal/mcpserver"
	"github.com/arsaljamal/grasp/internal/store"
	"github.com/arsaljamal/grasp/internal/web"
)

var version = "dev"

const usage = `grasp: review what your coding agent did, decision by decision.

Usage:
  grasp init                 set up Claude Code in this project (MCP server, hooks, CLAUDE.md)
  grasp serve [-addr A]      open the review page (default http://127.0.0.1:7080)
  grasp import [--latest | transcript.jsonl ...]
                             load Claude Code sessions (--latest: newest session for this folder)
  grasp feedback             print reviewer feedback as instructions for the agent
  grasp mcp                  run the MCP server (started by the agent, not by you)
  grasp hook                 Claude Code hook entry point (reads the hook event on stdin)
  grasp version

All commands work on the current folder; state lives in ./.grasp.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "init":
		err = runInit()
	case "serve":
		err = runServe(ctx, args)
	case "import":
		err = runImport(args)
	case "feedback":
		err = runFeedback()
	case "mcp":
		err = runMCP(ctx)
	case "hook":
		runHook(os.Stdin)
	case "version", "--version":
		fmt.Println("grasp", version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "grasp:", err)
		os.Exit(1)
	}
}

func openHere() (*store.Store, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	return store.Open(wd)
}

func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7080", "listen address")
	_ = fs.Parse(args)
	st, err := openHere()
	if err != nil {
		return err
	}
	fmt.Printf("grasp: review page on http://%s (Ctrl+C to stop)\n", *addr)
	return web.New(st).ListenAndServe(ctx, *addr)
}

func runImport(args []string) error {
	st, err := openHere()
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "--latest" {
		p, err := latestTranscript()
		if err != nil {
			return err
		}
		args = []string{p}
	}
	if len(args) == 0 {
		return errors.New("give transcript files, or --latest")
	}
	for _, p := range args {
		se, err := st.Import(p)
		if err != nil {
			return err
		}
		edits := 0
		for _, e := range se.Events {
			if e.Kind == "edit" {
				edits++
			}
		}
		fmt.Printf("imported %q: %d events, %d edits, %d decisions, %d unexplained edits\n",
			se.Title, len(se.Events), edits, len(se.Decisions), len(se.Unexplained))
	}
	return nil
}

// latestTranscript finds the newest Claude Code session for this folder.
func latestTranscript() (string, error) {
	wd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	enc := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(wd)
	files, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", enc, "*.jsonl"))
	if len(files) == 0 {
		return "", fmt.Errorf("no Claude Code sessions found for %s", wd)
	}
	sort.Slice(files, func(i, j int) bool { return mtime(files[i]) > mtime(files[j]) })
	return files[0], nil
}

func mtime(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime().UnixNano()
	}
	return 0
}

func runFeedback() error {
	st, err := openHere()
	if err != nil {
		return err
	}
	fb, err := st.Feedback()
	if err != nil {
		return err
	}
	fmt.Print(store.FeedbackMarkdown(fb))
	return nil
}

func runMCP(ctx context.Context) error {
	st, err := openHere()
	if err != nil {
		return err
	}
	return mcpserver.New(st, version).Run(ctx, &mcp.StdioTransport{})
}

// runHook imports the session that triggered the hook. It never fails the
// agent: errors go to stderr and the exit code stays 0.
func runHook(r io.Reader) {
	var ev struct {
		TranscriptPath string `json:"transcript_path"`
		Cwd            string `json:"cwd"`
	}
	if err := json.NewDecoder(r).Decode(&ev); err != nil || ev.TranscriptPath == "" {
		return
	}
	dir := ev.Cwd
	if dir == "" {
		dir, _ = os.Getwd()
	}
	st, err := store.Open(dir)
	if err == nil {
		_, err = st.Import(ev.TranscriptPath)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "grasp hook:", err)
	}
}

const claudeNote = `
## Decisions for review (grasp)

Reviewers read your work as decisions through grasp. Before changing code for a
non-trivial reason, call the ` + "`record_decision`" + ` tool once per decision (what, why,
files, alternatives, risk). At the start of a task, call ` + "`get_review_feedback`" + ` and
follow it. Small mechanical edits need no decision.
`

func runInit() error {
	bin := "grasp"
	if _, err := exec.LookPath("grasp"); err != nil {
		if exe, err := os.Executable(); err == nil {
			bin = exe
		}
	}
	if err := mergeJSON(".mcp.json", func(m map[string]any) bool {
		servers := child(m, "mcpServers")
		if _, ok := servers["grasp"]; ok {
			return false
		}
		servers["grasp"] = map[string]any{"command": bin, "args": []string{"mcp"}}
		return true
	}); err != nil {
		return err
	}
	hookCmd := bin + " hook"
	if err := mergeJSON(filepath.Join(".claude", "settings.json"), func(m map[string]any) bool {
		hooks := child(m, "hooks")
		a := addHook(hooks, "PostToolUse", "Edit|Write|MultiEdit|mcp__grasp__record_decision", hookCmd)
		b := addHook(hooks, "Stop", "", hookCmd)
		return a || b
	}); err != nil {
		return err
	}
	b, _ := os.ReadFile("CLAUDE.md")
	if !strings.Contains(string(b), "record_decision") {
		f, err := os.OpenFile("CLAUDE.md", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, err = f.WriteString(claudeNote)
		f.Close()
		if err != nil {
			return err
		}
		fmt.Println("updated CLAUDE.md")
	}
	fmt.Println("grasp is set up. Start Claude Code here, then run `grasp serve` to review.")
	fmt.Println("Tip: add .grasp/ to .gitignore, or commit .grasp/reviews.json to share verdicts.")
	return nil
}

func mergeJSON(path string, change func(map[string]any) bool) error {
	m := map[string]any{}
	b, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%s is not valid JSON; not touching it: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !change(m) {
		fmt.Printf("%s already set up\n", path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("updated %s\n", path)
	return nil
}

func child(m map[string]any, key string) map[string]any {
	c, ok := m[key].(map[string]any)
	if !ok {
		c = map[string]any{}
		m[key] = c
	}
	return c
}

func addHook(hooks map[string]any, event, matcher, command string) bool {
	list, _ := hooks[event].([]any)
	for _, h := range list {
		hm, _ := h.(map[string]any)
		inner, _ := hm["hooks"].([]any)
		for _, x := range inner {
			if xm, _ := x.(map[string]any); xm["command"] == command {
				return false
			}
		}
	}
	entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}
	if matcher != "" {
		entry["matcher"] = matcher
	}
	hooks[event] = append(list, entry)
	return true
}
