package session

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// FromGit builds a session from a range of commits, for changes that came
// with no agent transcript (a pull request, say). Each commit becomes a
// decision whose reason is its message, and each changed file becomes one
// edit holding the file before and after the whole range.
func FromGit(dir, base, head, title, body string) (*Session, error) {
	headSHA, err := git(dir, "rev-parse", head)
	if err != nil {
		return nil, err
	}
	s := &Session{ID: "git-" + short(headSHA), Agent: "git", Dir: dir, Title: title}
	log, err := git(dir, "log", "--reverse", "--format=%H%x1f%aI%x1f%s%x1f%b%x1e", base+".."+head)
	if err != nil {
		return nil, err
	}
	b := &builder{s: s, turn: 1}
	prompt := strings.TrimSpace(title + "\n\n" + body)
	if prompt == "" {
		prompt = "Changes " + short(base) + ".." + short(headSHA)
	}
	for _, rec := range strings.Split(log, "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) < 4 {
			continue
		}
		when, _ := time.Parse(time.RFC3339, f[1])
		if s.Started.IsZero() {
			s.Started = when
			b.emit(Event{Time: when, Kind: KindPrompt, Text: prompt})
			if s.Title == "" {
				s.Title = firstLine(f[2], 80)
			}
		}
		s.Updated = when
		files, _ := git(dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "--root", f[0])
		b.emit(Event{Time: when, Kind: KindDecision, Text: f[2], DecisionID: "commit-" + short(f[0])})
		s.Decisions = append(s.Decisions, Decision{
			ID: "commit-" + short(f[0]), Seq: len(s.Events), Turn: 1, Title: f[2], Kind: commitKind(f[2]),
			Why: strings.TrimSpace(f[3]), Files: lines(files),
		})
	}
	if len(s.Decisions) == 0 {
		return nil, fmt.Errorf("no commits in %s..%s", base, head)
	}
	if err := gitEdits(b, dir, base, head, s.Updated); err != nil {
		return nil, err
	}
	Link(s)
	return s, nil
}

// gitEdits adds one edit per file changed between base and head.
func gitEdits(b *builder, dir, base, head string, when time.Time) error {
	names, err := git(dir, "diff", "--name-status", "--no-renames", base, head)
	if err != nil {
		return err
	}
	for _, l := range lines(names) {
		f := strings.SplitN(l, "\t", 2)
		if len(f) != 2 {
			continue
		}
		status, path := f[0], f[1]
		e := Event{Time: when, Kind: KindEdit, Tool: "git", File: path}
		if status != "A" {
			e.Before, _ = gitRaw(dir, "show", base+":"+path)
		}
		if status != "D" {
			e.After, _ = gitRaw(dir, "show", head+":"+path)
		}
		e.NewFile = status == "A"
		b.emit(e)
	}
	return nil
}

// MergeExplanation replaces commit decisions with the ones an explaining
// agent recorded for the same changes, and takes its examples and notes.
// The edits stay those from git, so every claim is checked against the real
// diff.
func MergeExplanation(g, explained *Session) {
	if len(explained.Decisions) == 0 && len(explained.Behaviors) == 0 {
		return
	}
	var events []Event
	for _, e := range g.Events {
		if e.Kind == KindPrompt {
			events = append(events, e)
		}
	}
	renumber := map[int]int{}
	for _, e := range explained.Events {
		switch e.Kind {
		case KindDecision, KindBehavior, KindSay:
			renumber[e.Seq] = len(events) + 1
			e.Seq, e.Turn = len(events)+1, 1
			events = append(events, e)
		}
	}
	for _, e := range g.Events {
		if e.Kind == KindEdit {
			e.Seq, e.DecisionID = len(events)+1, ""
			events = append(events, e)
		}
	}
	g.Events = events
	if len(explained.Decisions) > 0 {
		g.Decisions = nil
		for _, d := range explained.Decisions {
			d.Seq, d.Turn = renumber[d.Seq], 1
			g.Decisions = append(g.Decisions, d)
		}
	}
	g.Behaviors = nil
	for _, bh := range explained.Behaviors {
		bh.Seq = renumber[bh.Seq]
		g.Behaviors = append(g.Behaviors, bh)
	}
	g.Agent = "git+" + explained.Agent
	Link(g)
}

func commitKind(subject string) string {
	s := strings.ToLower(subject)
	switch {
	case strings.HasPrefix(s, "test"):
		return "testing"
	case strings.HasPrefix(s, "perf"):
		return "performance"
	case strings.HasPrefix(s, "refactor"), strings.HasPrefix(s, "chore"), strings.HasPrefix(s, "style"):
		return "quality"
	case strings.HasPrefix(s, "sec"):
		return "security"
	}
	return "other"
}

func git(dir string, args ...string) (string, error) {
	out, err := gitRaw(dir, args...)
	return strings.TrimSpace(out), err
}

func gitRaw(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
