// Package web serves the review page: the animated decision graph, the
// replay, and the approve/reject/comment controls.
package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/arsaljamal/grasp/internal/impact"
	"github.com/arsaljamal/grasp/internal/session"
	"github.com/arsaljamal/grasp/internal/store"
)

//go:embed ui/index.html
var page []byte

// Server is the review page over one store.
type Server struct {
	st *store.Store
}

// New returns the review server.
func New(st *store.Store) *Server { return &Server{st: st} }

// Handler routes the page and its API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	mux.HandleFunc("GET /api/sessions", s.sessions)
	mux.HandleFunc("GET /api/session", s.session)
	mux.HandleFunc("POST /api/review", s.review)
	mux.HandleFunc("GET /api/feedback", s.feedback)
	mux.HandleFunc("GET /api/impact", s.impact)
	mux.HandleFunc("GET /api/live", s.live)
	return mux
}

type sessionSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Agent     string    `json:"agent"`
	Updated   time.Time `json:"updated"`
	Decisions int       `json:"decisions"`
	Edits     int       `json:"edits"`
}

func (s *Server) sessions(w http.ResponseWriter, _ *http.Request) {
	list, err := s.st.Sessions()
	if err != nil {
		fail(w, err)
		return
	}
	out := []sessionSummary{}
	for _, se := range list {
		n := 0
		for _, e := range se.Events {
			if e.Kind == session.KindEdit {
				n++
			}
		}
		out = append(out, sessionSummary{se.ID, se.Title, se.Agent, se.Updated, len(se.Decisions), n})
	}
	reply(w, out)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	se, err := s.st.Session(r.URL.Query().Get("id"))
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "no sessions yet: run `grasp import <transcript>` or set up the hook with `grasp init`", http.StatusNotFound)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	reviews, err := s.st.Reviews()
	if err != nil {
		fail(w, err)
		return
	}
	mine := map[string]*store.Review{}
	for _, d := range se.Decisions {
		if rv := reviews[d.ID]; rv != nil {
			mine[d.ID] = rv
		}
	}
	for _, b := range se.Behaviors {
		if rv := reviews[b.ID]; rv != nil {
			mine[b.ID] = rv
		}
	}
	reply(w, map[string]any{"session": se, "reviews": mine})
}

type reviewIn struct {
	Decision string  `json:"decision"`
	Author   string  `json:"author"`
	Verdict  *string `json:"verdict"`
	Comment  string  `json:"comment"`
	Expected *bool   `json:"expected"` // for behavior examples: did it match the prediction?
}

func (s *Server) review(w http.ResponseWriter, r *http.Request) {
	var in reviewIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil || in.Decision == "" {
		http.Error(w, "need decision", http.StatusBadRequest)
		return
	}
	if in.Author == "" {
		in.Author = "reviewer"
	}
	if in.Expected != nil {
		rv, err := s.st.SetExpected(in.Decision, in.Author, *in.Expected)
		if err != nil {
			fail(w, err)
			return
		}
		reply(w, rv)
		return
	}
	verdict := ""
	if in.Verdict != nil {
		verdict = *in.Verdict
	}
	rv, err := s.st.Review(in.Decision, in.Author, verdict, in.Verdict != nil, in.Comment)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reply(w, rv)
}

// impact reports what the session's changes could break, checked against
// the project as it is now.
func (s *Server) impact(w http.ResponseWriter, r *http.Request) {
	se, err := s.st.Session(r.URL.Query().Get("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	reply(w, impact.Analyze(se))
}

func (s *Server) feedback(w http.ResponseWriter, _ *http.Request) {
	fb, err := s.st.Feedback()
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(store.FeedbackMarkdown(fb)))
}

// live tells the page when anything changes, so a running agent's work and
// other reviewers' verdicts appear without a reload.
func (s *Server) live(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	last := int64(-1)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		s.st.Refresh()
		if st := s.st.Stamp(); st != last {
			last = st
			fmt.Fprintf(w, "data: %d\n\n", st)
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

// ListenAndServe serves until ctx ends.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func reply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
