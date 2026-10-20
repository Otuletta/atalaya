// Package summary reduces the state to what fits in a shell prompt or a
// menu bar. A running dashboard keeps it in status.json, so `atalaya prompt`
// answers in milliseconds without collecting anything.
package summary

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/store"
)

// Project is one project, reduced.
type Project struct {
	Name     string   `json:"name"`
	Path     string   `json:"path,omitempty"`
	Branch   string   `json:"branch,omitempty"`
	Ahead    int      `json:"ahead,omitempty"`
	Behind   int      `json:"behind,omitempty"`
	Dirty    bool     `json:"dirty,omitempty"`
	CI       string   `json:"ci,omitempty"` // worst of default and local branch
	CIURL    string   `json:"ci_url,omitempty"`
	RepoURL  string   `json:"repo_url,omitempty"`
	ProdDown []string `json:"prod_down,omitempty"`
	Waiting  int      `json:"agents_waiting,omitempty"`
}

// Summary is everything a prompt or a menu bar shows.
type Summary struct {
	UpdatedAt time.Time `json:"updated_at"`
	CIBroken  int       `json:"ci_broken"`
	ProdDown  int       `json:"prod_down"`
	Waiting   int       `json:"agents_waiting"`
	Reviews   int       `json:"reviews"`
	Dirty     int       `json:"dirty"`
	OutOfSync int       `json:"out_of_sync"`
	Projects  []Project `json:"projects"`
}

// Compute summarizes a state.
func Compute(st store.State, cfg *config.Config, now time.Time) Summary {
	s := Summary{UpdatedAt: now}
	byName := map[string]*Project{}
	for _, p := range cfg.Projects {
		s.Projects = append(s.Projects, Project{Name: p.Name, Path: p.Path})
	}
	for i := range s.Projects {
		byName[s.Projects[i].Name] = &s.Projects[i]
	}
	if g, ok := store.Data[collector.GitSnapshot](st, "git"); ok {
		for _, r := range g.Repos {
			p := byName[r.Project]
			if p == nil {
				continue
			}
			p.Branch, p.Ahead, p.Behind, p.Dirty = r.Branch, r.Ahead, r.Behind, r.Dirty()
			if r.Dirty() {
				s.Dirty++
			}
			if r.OutOfSync() {
				s.OutOfSync++
			}
		}
	}
	if gh, ok := store.Data[collector.GitHubSnapshot](st, "github"); ok {
		s.Reviews = len(gh.OtherReviewRequests)
		for _, r := range gh.Repos {
			s.Reviews += len(r.ReviewRequests)
			p := byName[r.Project]
			if p == nil {
				continue
			}
			p.RepoURL = r.URL
			for _, run := range []*collector.CIRun{r.CI, branchCI(r)} {
				if run == nil {
					continue
				}
				if p.CI == "" || run.State == collector.CIFailure {
					p.CI, p.CIURL = run.State, run.URL
				}
			}
			if p.CI == collector.CIFailure {
				s.CIBroken++
			}
		}
	}
	if h, ok := store.Data[collector.HealthSnapshot](st, "health"); ok {
		for _, c := range h.Checks {
			if c.OK {
				continue
			}
			s.ProdDown++
			if p := byName[c.Project]; p != nil {
				p.ProdDown = append(p.ProdDown, c.URL)
			}
		}
	}
	if a, ok := store.Data[collector.AgentsSnapshot](st, "agents"); ok {
		for _, ag := range a.Agents {
			if ag.Session == nil || ag.Session.State == collector.SessionWorking {
				continue
			}
			s.Waiting++
			if p := byName[ag.Project]; p != nil {
				p.Waiting++
			}
		}
	}
	return s
}

func branchCI(r collector.GitHubRepo) *collector.CIRun {
	if r.Branch == nil {
		return nil
	}
	return r.Branch.CI
}

// Prompt is the global line: "✗2 ◉1", empty when nothing needs you.
func (s Summary) Prompt() string {
	var parts []string
	if n := s.CIBroken + s.ProdDown; n > 0 {
		parts = append(parts, fmt.Sprintf("✗%d", n))
	}
	if n := s.Waiting + s.Reviews; n > 0 {
		parts = append(parts, fmt.Sprintf("◉%d", n))
	}
	return strings.Join(parts, " ")
}

// Prompt is a project's line: "✗ CI ↓3 ◉".
func (p Project) Prompt() string {
	var parts []string
	if p.CI == collector.CIFailure {
		parts = append(parts, "✗ CI")
	}
	if len(p.ProdDown) > 0 {
		parts = append(parts, "✗ prod")
	}
	if p.Ahead > 0 {
		parts = append(parts, fmt.Sprintf("↑%d", p.Ahead))
	}
	if p.Behind > 0 {
		parts = append(parts, fmt.Sprintf("↓%d", p.Behind))
	}
	if p.Waiting > 0 {
		parts = append(parts, "◉")
	}
	return strings.Join(parts, " ")
}

// ProjectFor is the project that contains dir, the deepest one.
func (s Summary) ProjectFor(dir string) (Project, bool) {
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	dir = real(dir)
	var best Project
	found := false
	for _, p := range s.Projects {
		if p.Path == "" {
			continue
		}
		root := real(p.Path)
		if (dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))) && len(root) > len(best.Path) {
			best, found = p, true
			best.Path = root
		}
	}
	return best, found
}

// SwiftBar renders the menu bar plugin format (https://swiftbar.app):
// the title line, then one item per project that needs attention.
func (s Summary) SwiftBar(atalaya string) string {
	var b strings.Builder
	title := "◆"
	if p := s.Prompt(); p != "" {
		title += " " + p
	}
	color := ""
	if s.CIBroken+s.ProdDown > 0 {
		color = " | color=red"
	}
	fmt.Fprintf(&b, "%s%s\n---\n", title, color)
	projects := append([]Project(nil), s.Projects...)
	sort.SliceStable(projects, func(i, j int) bool { return rank(projects[i]) > rank(projects[j]) })
	for _, p := range projects {
		line := p.Prompt()
		if p.Dirty {
			line = strings.TrimSpace(line + " cambios sin commitear")
		}
		if line == "" {
			line = "✓"
		}
		href := p.CIURL
		if href == "" || p.CI != collector.CIFailure {
			href = p.RepoURL
		}
		item := p.Name + " · " + line
		if href != "" {
			item += " | href=" + href
		}
		b.WriteString(item + "\n")
	}
	fmt.Fprintf(&b, "---\nActualizado %s\nAbrir Atalaya | bash=%s terminal=true\n", s.UpdatedAt.Local().Format("15:04:05"), atalaya)
	return b.String()
}

func rank(p Project) int {
	switch {
	case p.CI == collector.CIFailure || len(p.ProdDown) > 0:
		return 3
	case p.Waiting > 0 || p.Behind > 0 || p.Ahead > 0:
		return 2
	case p.Dirty:
		return 1
	}
	return 0
}

// Write saves the summary atomically: a prompt never reads half a file.
func Write(path string, s Summary) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Read loads a summary written by a running Atalaya.
func Read(path string) (Summary, error) {
	var s Summary
	b, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

// Keep rewrites the summary at path whenever the store changes, at most
// every interval, until ctx is done.
func Keep(ctx context.Context, st *store.Store, cfg *config.Config, path string, interval time.Duration) {
	changes := st.Subscribe()
	defer st.Unsubscribe(changes)
	for {
		// Write first, then wait: the file exists from the start.
		_ = Write(path, Compute(st.Snapshot(), cfg, time.Now()))
		select {
		case <-ctx.Done():
			return
		case <-changes:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
