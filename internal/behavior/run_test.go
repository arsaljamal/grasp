package behavior

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCompareRunsOldAndNewCode(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(dir, "greet.sh"), []byte(s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("echo hello\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-qm", "v1")
	base := BaseCommit(dir)
	write("echo 'hello, world'\nexit 3\n") // the agent's change, uncommitted

	r := Compare(context.Background(), dir, base, "sh greet.sh", 10*time.Second)
	if r.Before.Output != "hello" || r.Before.Exit != 0 {
		t.Fatalf("before = %+v", r.Before)
	}
	if r.After.Output != "hello, world" || r.After.Exit != 3 || !r.Changed {
		t.Fatalf("after = %+v changed=%v", r.After, r.Changed)
	}
	if out, _ := exec.Command("git", "-C", dir, "worktree", "list").Output(); strings.Count(string(out), "\n") != 1 {
		t.Fatalf("temporary worktree left behind:\n%s", out)
	}
}

func TestBackgroundProcessesAreStopped(t *testing.T) {
	start := time.Now()
	r := run(context.Background(), t.TempDir(), "sleep 30 & echo started", 10*time.Second)
	if r.Output != "started" || time.Since(start) > 5*time.Second {
		t.Fatalf("run = %+v after %s", r, time.Since(start))
	}
}

func TestTimeout(t *testing.T) {
	r := run(context.Background(), t.TempDir(), "echo partial; sleep 30", 500*time.Millisecond)
	if !r.TimedOut || !strings.Contains(r.Output, "partial") {
		t.Fatalf("run = %+v", r)
	}
}
