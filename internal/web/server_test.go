package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arsaljamal/grasp/internal/store"
)

func TestReviewRoundTrip(t *testing.T) {
	st, _ := store.Open(t.TempDir())
	if _, err := st.Import("../../testdata/decided.jsonl"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st).Handler())
	defer srv.Close()

	res, err := http.Post(srv.URL+"/api/review", "application/json", strings.NewReader(`{"decision":"d1","author":"sam","verdict":"approved","comment":"fine for v1"}`))
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("review: %v %v", err, res.Status)
	}
	res, err = http.Get(srv.URL + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Session struct{ Title string }
		Reviews map[string]store.Review
	}
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Session.Title != "Rate limiter" || got.Reviews["d1"].Verdict != store.Approved || got.Reviews["d1"].Comments[0].Author != "sam" {
		t.Fatalf("%+v", got)
	}
	if res, _ := http.Get(srv.URL + "/"); res.StatusCode != 200 {
		t.Fatal("page not served")
	}
}

// The page script must parse; a syntax error there breaks the whole page.
func TestPageScriptParses(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	s := string(page)
	start, end := strings.Index(s, "<script>"), strings.LastIndex(s, "</script>")
	f := filepath.Join(t.TempDir(), "page.js")
	if err := os.WriteFile(f, []byte(s[start+len("<script>"):end]), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, "--check", f).CombinedOutput(); err != nil {
		t.Fatalf("page script does not parse:\n%s", out)
	}
}
