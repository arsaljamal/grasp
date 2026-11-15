package session

import (
	"fmt"
	"path"
	"strings"
)

// Link ties every edit to the decision that explains it.
//
// An edit belongs to the recorded decisions that name its file (see the loop
// below for which ones). Files a decision names but never changes are marked
// unverified. When the agent recorded no decisions at all, edits are grouped
// by what the agent said just before making them, and marked inferred.
// Otherwise, edits no decision names are left unexplained, so a reviewer
// sees exactly which changes came with no reason.
func Link(s *Session) {
	defer linkBehaviors(s)
	s.Unexplained = nil
	for i := range s.Decisions {
		s.Decisions[i].Edits, s.Decisions[i].Unverified = nil, nil
	}
	if len(s.Decisions) == 0 {
		infer(s)
		return
	}
	lastEdit := map[string]int{} // file -> Seq of its previous edit
	for i := range s.Events {
		e := &s.Events[i]
		if e.Kind != KindEdit {
			continue
		}
		// Agents often record several decisions and then make one edit that
		// carries out all of them, so an edit serves every decision recorded
		// for its file since that file's previous edit. With none, it goes to
		// the nearest decision naming the file (one recorded afterwards).
		best, serves := -1, []int{}
		for j, d := range s.Decisions {
			if !contains(d.Files, e.File) {
				continue
			}
			if d.Seq > lastEdit[e.File] && d.Seq < e.Seq {
				serves = append(serves, j)
			}
			if best < 0 || dist(d.Seq, e.Seq) < dist(s.Decisions[best].Seq, e.Seq) {
				best = j
			}
		}
		lastEdit[e.File] = e.Seq
		if best < 0 {
			best = testOwner(s.Decisions, e.File) // a test goes with the code it tests
		}
		if best < 0 {
			s.Unexplained = append(s.Unexplained, e.Seq)
			continue
		}
		if len(serves) == 0 {
			serves = []int{best}
		}
		e.DecisionID = s.Decisions[serves[len(serves)-1]].ID
		for _, j := range serves {
			s.Decisions[j].Edits = append(s.Decisions[j].Edits, e.Seq)
		}
	}
	for i := range s.Decisions {
		d := &s.Decisions[i]
		for _, f := range d.Files {
			if !editsFile(s, d.Edits, f) {
				d.Unverified = append(d.Unverified, f)
			}
		}
	}
}

func infer(s *Session) {
	var cur *Decision
	lastSay, lastPrompt := "", ""
	for i := range s.Events {
		e := &s.Events[i]
		switch e.Kind {
		case KindPrompt:
			lastPrompt, lastSay, cur = e.Text, "", nil
		case KindSay:
			lastSay, cur = e.Text, nil
		case KindEdit:
			if cur == nil {
				title := sentence(lastSay)
				if title == "" {
					title = "Changes for: " + firstLine(lastPrompt, 70)
				}
				s.Decisions = append(s.Decisions, Decision{
					ID: fmt.Sprintf("inferred-%d", e.Seq), Seq: e.Seq, Turn: e.Turn,
					Title: title, Kind: "other", Inferred: true, Why: lastSay,
				})
				cur = &s.Decisions[len(s.Decisions)-1]
			}
			e.DecisionID = cur.ID
			cur.Edits = append(cur.Edits, e.Seq)
			if !contains(cur.Files, e.File) {
				cur.Files = append(cur.Files, e.File)
			}
		}
	}
}

func editsFile(s *Session, edits []int, f string) bool {
	for _, seq := range edits {
		if s.Events[seq-1].File == f {
			return true
		}
	}
	return false
}

// sentence returns the first sentence of what the agent said, short enough
// to be a node label.
func sentence(t string) string {
	t = firstLine(t, 200)
	for _, sep := range []string{". ", ": ", "? "} {
		if i := strings.Index(t, sep); i > 10 {
			t = t[:i]
			break
		}
	}
	t = strings.TrimRight(t, ".:")
	return firstLine(t, 90)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func dist(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// linkBehaviors ties each example to the decision it names, or else to the
// latest decision recorded before it.
func linkBehaviors(s *Session) {
	for i := range s.Behaviors {
		bh := &s.Behaviors[i]
		bh.DecisionID = ""
		want := strings.ToLower(strings.TrimSpace(bh.Decision))
		for _, d := range s.Decisions {
			t := strings.ToLower(d.Title)
			if want != "" && (t == want || strings.HasPrefix(t, want) || strings.HasPrefix(want, t)) {
				bh.DecisionID = d.ID
				break
			}
		}
		if bh.DecisionID != "" {
			continue
		}
		for _, d := range s.Decisions {
			if d.Seq < bh.Seq && !d.Inferred {
				bh.DecisionID = d.ID
			}
		}
	}
	for i := range s.Events {
		if e := &s.Events[i]; e.Kind == KindBehavior {
			for _, bh := range s.Behaviors {
				if bh.Seq == e.Seq {
					e.DecisionID = bh.DecisionID
				}
			}
		}
	}
}

// testOwner finds the decision that changed the code a test file tests:
// repository_sqlmock_test.go belongs with repository.go in the same folder.
func testOwner(ds []Decision, file string) int {
	dir, name := path.Split(file)
	stem := ""
	for _, marker := range []string{"_test.", ".test.", ".spec.", "_spec."} {
		if i := strings.Index(name, marker); i > 0 {
			stem = name[:i]
			break
		}
	}
	if strings.HasPrefix(name, "test_") {
		stem = strings.TrimSuffix(strings.TrimPrefix(name, "test_"), path.Ext(name))
	}
	if stem == "" {
		return -1
	}
	best, bestLen := -1, 0
	for i, d := range ds {
		for _, f := range d.Files {
			fdir, fname := path.Split(f)
			fstem := strings.TrimSuffix(fname, path.Ext(fname))
			if fdir == dir && fstem != "" && strings.HasPrefix(stem, fstem) && len(fstem) > bestLen && !strings.Contains(fname, "_test.") {
				best, bestLen = i, len(fstem)
			}
		}
	}
	return best
}
