// Package demo sets up sample projects with recorded agent sessions, so
// people can try the review page without running an agent first.
package demo

import (
	"embed"
	"encoding/json"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// sample holds small Go projects (files end in .txt so Go doesn't build
// them), each with the Claude Code session that changed it.
//
//go:embed sample
var sample embed.FS

// Setup writes every sample project into a new temporary folder, and its
// session into the folder's .grasp store. It returns the folder.
func Setup() (string, error) {
	dir, err := os.MkdirTemp("", "grasp-demo-")
	if err != nil {
		return "", err
	}
	err = fs.WalkDir(sample, "sample", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := sample.ReadFile(p)
		if err != nil {
			return err
		}
		project := path.Base(path.Dir(p))
		out := filepath.Join(dir, project, strings.TrimSuffix(path.Base(p), ".txt"))
		if path.Base(p) == "session.json" {
			// The session refers to its project folder; point it there.
			q, _ := json.Marshal(filepath.Join(dir, project))
			b = []byte(strings.ReplaceAll(string(b), "{{DIR}}", strings.Trim(string(q), `"`)))
			out = filepath.Join(dir, ".grasp", "sessions", project+".json")
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		return os.WriteFile(out, b, 0o644)
	})
	return dir, err
}
