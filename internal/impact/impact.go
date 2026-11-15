// Package impact finds what an agent's changes could break that the agent
// did not mention: existing tests it edited, and code elsewhere in the
// project that uses what it changed but was not updated.
package impact

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/arsaljamal/grasp/internal/session"
)

// Report is everything a reviewer should double-check.
type Report struct {
	Tests    []TestChange `json:"tests"`
	Ripples  []Ripple     `json:"ripples"`
	Searched int          `json:"searched"` // files searched for uses
	Note     string       `json:"note,omitempty"`
}

// TestChange is an existing test, fixture or check config the agent edited.
type TestChange struct {
	File          string   `json:"file"`
	Edits         []int    `json:"edits"`
	Decisions     []string `json:"decisions,omitempty"`
	RemovedLines  int      `json:"removed_lines"`
	RemovedChecks int      `json:"removed_checks"` // removed lines that look like assertions
}

// Ripple is a changed definition that other, unchanged code uses.
type Ripple struct {
	Symbol   string `json:"symbol"`
	File     string `json:"file"`
	Decision string `json:"decision,omitempty"`
	Removed  bool   `json:"removed,omitempty"` // the definition no longer exists
	Uses     []Use  `json:"uses"`
}

// Use is one place that refers to a changed definition.
type Use struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

const (
	maxFiles    = 5000
	maxFileSize = 1 << 20
	maxUses     = 8
)

var testPath = regexp.MustCompile(`(_test\.go|\.test\.[jt]sx?|\.spec\.[jt]sx?|(^|/)tests?/|(^|/)__tests__/|(^|/)test_[^/]+\.py|_test\.py|(^|/)testdata/|(^|/)fixtures?/|(^|/)\.github/workflows/|\.golangci\.ya?ml|jest\.config|vitest\.config|pytest\.ini)`)

var checkLine = regexp.MustCompile(`\b(assert\w*|expect|require\.\w+|t\.(Error|Errorf|Fatal|Fatalf|Fail)|should|toBe|toEqual|toThrow|Equal|NotEqual|wantErr|want:)\b|\bif\s+.*(!=|==)`)

// Definition patterns for common languages; the first group is the name.
var defs = []*regexp.Regexp{
	regexp.MustCompile(`^\s*func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)\s*[\[(]`),                                // Go func/method
	regexp.MustCompile(`^\s*type\s+([A-Za-z_]\w*)\s`),                                                       // Go type
	regexp.MustCompile(`^\s*(?:export\s+)?(?:default\s+)?(?:async\s+)?function\s*\*?\s*([A-Za-z_$][\w$]*)`), // JS function
	regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*(?:async\s*)?(?:\(|function|[A-Za-z_$][\w$]*\s*=>)`),
	regexp.MustCompile(`^\s*(?:export\s+)?(?:abstract\s+)?(?:class|interface)\s+([A-Za-z_$][\w$]*)`),   // JS/TS/Java/Kotlin class
	regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)`),                                        // Python/Ruby def
	regexp.MustCompile(`^\s*(?:private\s+|public\s+|internal\s+)?fun\s+(?:[\w.<>]+\.)?([A-Za-z_]\w*)`), // Kotlin fun
}

// Names too common to search for usefully.
var stop = map[string]bool{"main": true, "init": true, "New": true, "new": true, "String": true, "Error": true,
	"Close": true, "Run": true, "Get": true, "Set": true, "get": true, "set": true, "run": true, "handler": true,
	"Handle": true, "ServeHTTP": true, "test": true, "setup": true, "constructor": true, "render": true}

// Analyze looks at a session's edits against the project as it is now.
func Analyze(s *session.Session) Report {
	r := Report{Tests: []TestChange{}, Ripples: []Ripple{}}
	if s.Dir == "" {
		r.Note = "project folder unknown"
		return r
	}
	if fi, err := os.Stat(s.Dir); err != nil || !fi.IsDir() {
		r.Note = "project folder not found on this machine"
		return r
	}
	edited, created := map[string]bool{}, map[string]bool{}
	tests := map[string]*TestChange{}
	type sym struct {
		file, decision string
		removed        bool
	}
	syms := map[string]sym{}
	for _, e := range s.Events {
		if e.Kind != session.KindEdit {
			continue
		}
		edited[e.File] = true
		if e.NewFile {
			created[e.File] = true
			continue
		}
		removed := removedLines(e.Before, e.After)
		if testPath.MatchString(e.File) && !created[e.File] {
			tc := tests[e.File]
			if tc == nil {
				tc = &TestChange{File: e.File}
				tests[e.File] = tc
			}
			tc.Edits = append(tc.Edits, e.Seq)
			if e.DecisionID != "" && !contains(tc.Decisions, e.DecisionID) {
				tc.Decisions = append(tc.Decisions, e.DecisionID)
			}
			tc.RemovedLines += len(removed)
			for _, l := range removed {
				if checkLine.MatchString(l) {
					tc.RemovedChecks++
				}
			}
			continue // a test's own functions are not worth a ripple search
		}
		if created[e.File] {
			continue
		}
		current, _ := os.ReadFile(filepath.Join(s.Dir, e.File))
		lines := strings.Split(string(current), "\n")
		for _, name := range enclosing(lines, e.Before, e.After) {
			if _, ok := syms[name]; !ok {
				syms[name] = sym{file: e.File, decision: e.DecisionID}
			}
		}
		for _, l := range removed {
			if name := defName(l); name != "" && !definedIn(lines, name) {
				syms[name] = sym{file: e.File, decision: e.DecisionID, removed: true}
			}
		}
	}
	for _, tc := range tests {
		if tc.RemovedLines > 0 { // only added tests: nothing was weakened
			r.Tests = append(r.Tests, *tc)
		}
	}
	sort.Slice(r.Tests, func(i, j int) bool { return r.Tests[i].File < r.Tests[j].File })

	files := projectFiles(s.Dir)
	for name, sy := range syms {
		if len(name) < 3 || stop[name] {
			continue
		}
		rp := Ripple{Symbol: name, File: sy.file, Decision: sy.decision, Removed: sy.removed}
		word := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
		for _, f := range files {
			if edited[f] || len(rp.Uses) >= maxUses {
				continue
			}
			rp.Uses = append(rp.Uses, find(filepath.Join(s.Dir, f), f, word, maxUses-len(rp.Uses))...)
		}
		if len(rp.Uses) > 0 {
			r.Ripples = append(r.Ripples, rp)
		}
	}
	sort.Slice(r.Ripples, func(i, j int) bool {
		if r.Ripples[i].Removed != r.Ripples[j].Removed {
			return r.Ripples[i].Removed
		}
		return r.Ripples[i].Symbol < r.Ripples[j].Symbol
	})
	r.Searched = 0
	for _, f := range files {
		if !edited[f] {
			r.Searched++
		}
	}
	return r
}

// enclosing returns the existing definitions whose bodies contain the
// changed text, found by locating it in the current file and walking up to
// the nearest definition line. Code at the top level of a file has no
// enclosing definition, and the walk stops at the end of the previous
// top-level block. Definitions written inside the change count only if they
// existed before it; brand-new ones cannot have callers that relied on them.
func enclosing(lines []string, before, after string) []string {
	afterLines := strings.Split(strings.Trim(after, "\n"), "\n")
	first := -1 // index in afterLines of the first non-blank line
	for k, l := range afterLines {
		if strings.TrimSpace(l) != "" {
			first = k
			break
		}
	}
	if first < 0 {
		return nil
	}
	at := -1 // where that line sits in the current file
	for i, l := range lines {
		if strings.TrimSpace(l) != "" && strings.TrimSpace(l) == strings.TrimSpace(afterLines[first]) {
			at = i
			break
		}
	}
	if at < 0 {
		return nil
	}
	unchanged := map[string]int{}
	for _, l := range strings.Split(before, "\n") {
		unchanged[strings.TrimSpace(l)]++
	}
	var out []string
	add := func(n string) {
		if n != "" && !contains(out, n) {
			out = append(out, n)
		}
	}
	for k := first; k < len(afterLines); k++ {
		t := strings.TrimSpace(afterLines[k])
		if t == "" || isComment(afterLines[k]) {
			continue
		}
		if unchanged[t] > 0 { // context the agent kept, not a change
			unchanged[t]--
			continue
		}
		pos := at + (k - first)
		if pos >= len(lines) {
			break
		}
		if name := defName(lines[pos]); name != "" {
			if mentionsDef(before, name) { // an existing definition was rewritten
				add(name)
			}
			continue
		}
		if !indented(lines[pos]) {
			continue // top-level code belongs to no definition
		}
		for j := pos - 1; j >= 0; j-- {
			if name := defName(lines[j]); name != "" {
				if !newHere(afterLines, before, name) {
					add(name)
				}
				break
			}
			if strings.HasPrefix(lines[j], "}") || (lines[j] != "" && !indented(lines[j]) && !isComment(lines[j])) {
				break
			}
		}
	}
	return out
}

// newHere reports whether the change itself introduced the definition.
func newHere(afterLines []string, before, name string) bool {
	for _, l := range afterLines {
		if defName(l) == name {
			return !mentionsDef(before, name)
		}
	}
	return false
}

func indented(l string) bool { return strings.HasPrefix(l, "\t") || strings.HasPrefix(l, " ") }

func isComment(l string) bool {
	t := strings.TrimSpace(l)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
}

func mentionsDef(text, name string) bool {
	for _, l := range strings.Split(text, "\n") {
		if defName(l) == name {
			return true
		}
	}
	return false
}

func defName(line string) string {
	for _, re := range defs {
		if m := re.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

func definedIn(lines []string, name string) bool {
	for _, l := range lines {
		if defName(l) == name {
			return true
		}
	}
	return false
}

// removedLines are lines of before that no longer appear in after.
func removedLines(before, after string) []string {
	keep := map[string]int{}
	for _, l := range strings.Split(after, "\n") {
		keep[strings.TrimSpace(l)]++
	}
	var out []string
	for _, l := range strings.Split(before, "\n") {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		if keep[t] > 0 {
			keep[t]--
			continue
		}
		out = append(out, t)
	}
	return out
}

func projectFiles(dir string) []string {
	out, err := exec.Command("git", "-C", dir, "ls-files", "--cached", "--others", "--exclude-standard").Output()
	var files []string
	if err == nil {
		for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f != "" && !strings.HasPrefix(f, ".grasp/") {
				files = append(files, f)
			}
		}
	} else {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", ".grasp", "node_modules", "vendor", "dist", "build", "target":
					return filepath.SkipDir
				}
				return nil
			}
			if rel, err := filepath.Rel(dir, p); err == nil {
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	if len(files) > maxFiles {
		files = files[:maxFiles]
	}
	return files
}

func find(path, rel string, word *regexp.Regexp, limit int) []Use {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() > maxFileSize {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil || bytes.IndexByte(b, 0) >= 0 {
		return nil
	}
	var out []Use
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), maxFileSize)
	for n := 1; sc.Scan() && len(out) < limit; n++ {
		if l := sc.Text(); word.MatchString(l) {
			t := strings.TrimSpace(l)
			if len(t) > 160 {
				t = t[:159] + "…"
			}
			out = append(out, Use{File: rel, Line: n, Text: t})
		}
	}
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
