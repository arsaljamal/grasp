// Package behavior runs an agent's example command on the code as it was
// before the session and on the code as it is now, so a reviewer sees what
// actually changed instead of what the agent says changed.
package behavior

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxOutput caps how much of a run's output is kept.
const MaxOutput = 4000

// Run is one execution of the example command.
type Run struct {
	Output   string `json:"output"`
	Exit     int    `json:"exit"`
	Error    string `json:"error,omitempty"`
	Millis   int64  `json:"millis"`
	Skipped  string `json:"skipped,omitempty"` // why this side did not run
	TimedOut bool   `json:"timed_out,omitempty"`
}

// Result is the before/after pair for one example.
type Result struct {
	Base   string `json:"base,omitempty"` // commit the "before" side ran on
	Before Run    `json:"before"`
	After  Run    `json:"after"`
	// Changed is true when the two sides printed different output or
	// exited differently.
	Changed bool `json:"changed"`
}

// BaseCommit returns HEAD of the git repository at dir, or "" if there is
// none (not a repository, or no commits yet).
func BaseCommit(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Compare runs command on base (in a temporary git worktree, leaving the
// project untouched) and then in dir itself.
func Compare(ctx context.Context, dir, base, command string, timeout time.Duration) Result {
	res := Result{Base: base}
	if base == "" {
		res.Before = Run{Skipped: "no earlier commit to compare with"}
	} else {
		res.Before = runAt(ctx, dir, base, command, timeout)
	}
	res.After = run(ctx, dir, command, timeout)
	res.Changed = res.Before.Skipped != "" || res.Before.Output != res.After.Output || res.Before.Exit != res.After.Exit
	return res
}

func runAt(ctx context.Context, dir, base, command string, timeout time.Duration) Run {
	tmp, err := os.MkdirTemp("", "grasp-before-")
	if err != nil {
		return Run{Skipped: err.Error()}
	}
	defer os.RemoveAll(tmp)
	if out, err := exec.Command("git", "-C", dir, "worktree", "add", "--detach", tmp, base).CombinedOutput(); err != nil {
		return Run{Skipped: "could not check out the earlier code: " + strings.TrimSpace(string(out))}
	}
	defer exec.Command("git", "-C", dir, "worktree", "remove", "--force", tmp).Run()
	r := run(ctx, tmp, command, timeout)
	// show paths as they would appear in the project, not in the temp copy
	r.Output = strings.ReplaceAll(r.Output, tmp+"/", "")
	if real, err := filepath.EvalSymlinks(tmp); err == nil {
		r.Output = strings.ReplaceAll(r.Output, real+"/", "")
	}
	return r
}

// run executes command with sh in its own process group, so servers the
// example starts in the background are stopped when it ends.
func run(ctx context.Context, dir, command string, timeout time.Duration) Run {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	cmd.WaitDelay = 2 * time.Second
	ownGroup(cmd)
	start := time.Now()
	err := cmd.Start()
	if err == nil {
		err = cmd.Wait()
	}
	killGroup(cmd)
	r := Run{Output: trim(buf.String()), Millis: time.Since(start).Milliseconds()}
	if ctx.Err() == context.DeadlineExceeded {
		r.TimedOut, r.Error = true, fmt.Sprintf("stopped after %s", timeout)
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			r.Exit = ee.ExitCode()
		} else if !r.TimedOut && err != exec.ErrWaitDelay {
			r.Error = err.Error()
		}
	}
	return r
}

func trim(s string) string {
	s = strings.TrimRight(s, "\n")
	if len(s) > MaxOutput {
		s = s[:MaxOutput] + "\n… (output cut)"
	}
	return s
}
