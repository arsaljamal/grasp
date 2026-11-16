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
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/arsaljamal/grasp/internal/behavior"
	"github.com/arsaljamal/grasp/internal/demo"
	"github.com/arsaljamal/grasp/internal/mcpserver"
	"github.com/arsaljamal/grasp/internal/session"
	"github.com/arsaljamal/grasp/internal/store"
	"github.com/arsaljamal/grasp/internal/web"
)

var version = "dev"

const usage = `grasp: review what your coding agent did, decision by decision.

Usage:
  grasp init                 set up Claude Code in this project (MCP server, hooks, CLAUDE.md)
  grasp serve [-addr A]      open the review page (default http://127.0.0.1:7080)
  grasp pr -base REF [-head REF] [-title T] [-body B] [-explain]
                             review a branch or pull request that has no agent session
                             (-explain: Claude records decisions and runnable examples)
  grasp import [--latest | transcript.jsonl ...]
                             load Claude Code sessions (--latest: newest session for this folder)
  grasp feedback             print reviewer feedback as instructions for the agent
  grasp mcp [-dir D]         run the MCP server (started by the agent, not by you)
  grasp hook                 Claude Code hook entry point (reads the hook event on stdin)
  grasp demo                 try the review page on a sample session (no agent needed)
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
	case "demo":
		err = runDemo(ctx, args)
	case "import":
		err = runImport(args)
	case "pr":
		err = runPR(ctx, args)
	case "feedback":
		err = runFeedback()
	case "mcp":
		err = runMCP(ctx, args)
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

// runDemo serves a sample project and session from a temporary folder.
func runDemo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:7080", "listen address (another free port is used if it is taken)")
	noOpen := fs.Bool("no-open", false, "don't open the browser")
	_ = fs.Parse(args)
	dir, err := demo.Setup()
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	st, err := store.Open(dir)
	if err != nil {
		return err
	}
	if ln, err := net.Listen("tcp", *addr); err == nil {
		ln.Close()
	} else if ln, err := net.Listen("tcp", "127.0.0.1:0"); err == nil {
		*addr = ln.Addr().String()
		ln.Close()
	}
	url := "http://" + *addr
	fmt.Printf("grasp demo: two real Claude Code sessions on a small Go API.\nReview it at %s (Ctrl+C to stop; nothing is kept).\n", url)
	if !*noOpen {
		go func() { time.Sleep(300 * time.Millisecond); openBrowser(url) }()
	}
	return web.New(st).ListenAndServe(ctx, *addr)
}

func openBrowser(url string) {
	cmd := "xdg-open"
	if runtime.GOOS == "darwin" {
		cmd = "open"
	}
	_ = exec.Command(cmd, url).Start()
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
	enc := encodeDir(wd)
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

const explainPrompt = `You are explaining a change to a human reviewer who has not read the code. Do not edit any files.

The full change is in the file %[3]s (a git diff from %[1]s to %[2]s) and the commit messages are in %[4]s. Read both files with the Read tool; you can also read any file in the project for context.

1. Read the diff and the commit messages.
2. For each meaningful design decision in the change, call record_decision: a short title (at most 8 words), why it was done in one or two plain sentences, the files it touches, alternatives the author visibly rejected, and the risk. Use only what the code, comments and commit messages show. If they don't say why, write "The change doesn't say why."
3. Where the change alters what the software does and you can show it with a command that runs locally within a couple of minutes, call show_behavior. grasp runs the command on %[1]s (before) and on the current code (now). Print one short line per case. Prefer existing tests or the program's own commands; skip anything that needs production services or credentials.
4. End with two sentences: what a reviewer should look at first, and why.`

func runPR(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("pr", flag.ExitOnError)
	base := fs.String("base", "", "commit or branch the change starts from (required)")
	head := fs.String("head", "HEAD", "last commit of the change")
	title := fs.String("title", "", "pull request title")
	body := fs.String("body", "", "pull request description")
	explain := fs.Bool("explain", false, "ask Claude Code to record decisions and runnable examples")
	timeout := fs.Duration("timeout", 4*time.Minute, "time limit for each example run")
	_ = fs.Parse(args)
	if *base == "" {
		return errors.New("-base is required")
	}
	wd, _ := os.Getwd()
	st, err := store.Open(wd)
	if err != nil {
		return err
	}
	se, err := session.FromGit(wd, *base, *head, *title, *body)
	if err != nil {
		return err
	}
	if *explain {
		baseSHA := behavior.BaseCommit(wd)
		if out, err := exec.Command("git", "-C", wd, "rev-parse", *base).Output(); err == nil {
			baseSHA = strings.TrimSpace(string(out))
		}
		fmt.Println("grasp: asking Claude Code to explain the change (this takes a few minutes)...")
		ex, err := explainWithClaude(ctx, wd, baseSHA, *head, *timeout)
		if err != nil {
			fmt.Fprintln(os.Stderr, "grasp: explanation failed, keeping commit messages:", err)
		} else {
			session.MergeExplanation(se, ex)
		}
	}
	if err := st.SaveSession(se); err != nil {
		return err
	}
	fmt.Printf("loaded %q: %d decisions, %d examples, %d changed files. Run `grasp serve` to review.\n",
		se.Title, len(se.Decisions), len(se.Behaviors), countEdits(se))
	return nil
}

func explainWithClaude(ctx context.Context, dir, base, head string, timeout time.Duration) (*session.Session, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cfg, err := os.CreateTemp("", "grasp-mcp-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(cfg.Name())
	json.NewEncoder(cfg).Encode(map[string]any{"mcpServers": map[string]any{"grasp": map[string]any{
		"command": exe, "args": []string{"mcp", "-base", base, "-timeout", timeout.String()}}}})
	cfg.Close()
	// Hand Claude the change as files, so it needs no shell access to read it.
	diff, err := exec.Command("git", "-C", dir, "diff", base, head).Output()
	if err != nil {
		return nil, fmt.Errorf("git diff: %w", err)
	}
	logOut, err := exec.Command("git", "-C", dir, "log", "--format=commit %h%n%B", base+".."+head).Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	work := filepath.Join(dir, ".grasp", "pr")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	diffPath, logPath := filepath.Join(work, "change.diff"), filepath.Join(work, "commits.txt")
	if err := os.WriteFile(diffPath, diff, 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(logPath, logOut, 0o644); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "claude", "-p", fmt.Sprintf(explainPrompt, base, head, diffPath, logPath),
		"--mcp-config", cfg.Name(), "--strict-mcp-config",
		"--allowedTools", "mcp__grasp__record_decision,mcp__grasp__show_behavior,mcp__grasp__get_review_feedback,Read,Grep,Glob",
		"--disallowedTools", "Edit,Write,MultiEdit,NotebookEdit",
		"--output-format", "json")
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("claude: %w", err)
	}
	var res struct {
		SessionID string `json:"session_id"`
		Result    string `json:"result"`
	}
	if err := json.Unmarshal(out, &res); err != nil || res.SessionID == "" {
		return nil, fmt.Errorf("unexpected claude output: %.200s", out)
	}
	path, err := transcriptFor(dir, res.SessionID)
	if err != nil {
		return nil, err
	}
	ex, err := session.ParseClaudeFile(path)
	if err != nil {
		return nil, err
	}
	if len(ex.Decisions) == 0 && len(ex.Behaviors) == 0 {
		return nil, fmt.Errorf("Claude recorded nothing. Its last words: %.400s", res.Result)
	}
	return ex, nil
}

// transcriptFor finds a Claude Code session's transcript for a folder.
func transcriptFor(dir, id string) (string, error) {
	home, _ := os.UserHomeDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	p := filepath.Join(home, ".claude", "projects", encodeDir(dir), id+".jsonl")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	matches, _ := filepath.Glob(filepath.Join(home, ".claude", "projects", "*", id+".jsonl"))
	if len(matches) > 0 {
		return matches[0], nil
	}
	return "", fmt.Errorf("transcript for session %s not found", id)
}

func encodeDir(dir string) string {
	return strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(dir)
}

func countEdits(se *session.Session) int {
	n := 0
	for _, e := range se.Events {
		if e.Kind == session.KindEdit {
			n++
		}
	}
	return n
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

func runMCP(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	base := fs.String("base", "", "commit examples compare against (default: HEAD when the agent starts)")
	timeout := fs.Duration("timeout", 90*time.Second, "time limit for each example run")
	dir := fs.String("dir", "", "project folder (default: $CLAUDE_PROJECT_DIR, else the current folder)")
	_ = fs.Parse(args)
	wd, _ := os.Getwd()
	if d := os.Getenv("CLAUDE_PROJECT_DIR"); d != "" {
		wd = d // a plugin's server may not start in the project folder
	}
	if *dir != "" {
		wd = *dir
	}
	st, err := store.Open(wd)
	if err != nil {
		return err
	}
	o := mcpserver.Options{Version: version, Dir: wd, Base: *base, Timeout: *timeout}
	if o.Base == "" {
		// The code as it is when the agent starts is the "before" for examples.
		o.Base = behavior.BaseCommit(wd)
	}
	return mcpserver.New(st, o).Run(ctx, &mcp.StdioTransport{})
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

const claudeNote = `<!-- grasp -->
## Work for review (grasp)

Reviewers read your work through grasp.
- At the start of a task, call ` + "`get_review_feedback`" + ` and follow it.
- Before changing code for a non-trivial reason, call ` + "`record_decision`" + ` once per
  decision (short title, why, files, alternatives, risk). Small mechanical edits need none.
- After a change that alters what the software does, call ` + "`show_behavior`" + ` with a
  command that demonstrates it. grasp runs it on the old and the new code and shows
  reviewers both outputs. Print one short line per case ("empty title -> 400"); start and
  stop servers inside the command.
<!-- /grasp -->
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
		a := addHook(hooks, "PostToolUse", "Edit|Write|MultiEdit|mcp__grasp__.*", hookCmd)
		b := addHook(hooks, "Stop", "", hookCmd)
		return a || b
	}); err != nil {
		return err
	}
	if changed, err := writeNote("CLAUDE.md"); err != nil {
		return err
	} else if changed {
		fmt.Println("updated CLAUDE.md")
	}
	fmt.Println("grasp is set up. Start Claude Code here, then run `grasp serve` to review.")
	fmt.Println("Tip: add .grasp/ to .gitignore, or commit .grasp/reviews.json to share verdicts.")
	return nil
}

// writeNote adds grasp's section to CLAUDE.md, or replaces it if an older
// version is there.
func writeNote(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	s := string(b)
	start, end := strings.Index(s, "<!-- grasp -->"), strings.Index(s, "<!-- /grasp -->")
	switch {
	case start >= 0 && end > start:
		s = s[:start] + strings.TrimLeft(claudeNote, "\n") + strings.TrimLeft(s[end+len("<!-- /grasp -->"):], "\n")
	case strings.Contains(s, "record_decision"):
		return false, nil // an older hand-edited note; leave it alone
	default:
		if s != "" && !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		s += "\n" + claudeNote
	}
	if s == string(b) {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(s), 0o644)
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
