package summary

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/gitparse"
	"github.com/otuletta/atalaya/internal/store"
)

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func fixture(t *testing.T) (*config.Config, *store.Store) {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{Projects: []config.Project{
		{Name: "odomto", Path: filepath.Join(root, "odomto")},
		{Name: "inner", Path: filepath.Join(root, "odomto", "packages", "inner")},
		{Name: "kdx", Path: filepath.Join(root, "kdx")},
	}}
	for _, p := range cfg.Projects {
		if err := os.MkdirAll(p.Path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st := store.New()
	st.Put("git", collector.GitSnapshot{Repos: []collector.Repo{
		{Project: "odomto", Branch: "main", Upstream: "origin/main", Behind: 3},
		{Project: "kdx", Branch: "feat", Upstream: "origin/feat", Changes: gitparse.Counts{Unstaged: 2}},
	}}, nil, now)
	st.Put("github", collector.GitHubSnapshot{
		Repos: []collector.GitHubRepo{
			{Project: "odomto", URL: "https://github.com/o/odomto", CI: &collector.CIRun{State: collector.CISuccess},
				Branch: &collector.BranchCI{Name: "x", CI: &collector.CIRun{State: collector.CIFailure, URL: "https://github.com/o/odomto/actions/runs/1"}}},
			{Project: "kdx", URL: "https://github.com/o/kdx", CI: &collector.CIRun{State: collector.CISuccess},
				ReviewRequests: []collector.PullRequest{{Number: 4}}},
		},
		OtherReviewRequests: []collector.PullRequest{{Number: 9}},
	}, nil, now)
	st.Put("health", collector.HealthSnapshot{Checks: []collector.Check{
		{Project: "kdx", URL: "https://kdx.dev", OK: false},
		{Project: "odomto", URL: "https://odomto.com", OK: true},
	}}, nil, now)
	st.Put("agents", collector.AgentsSnapshot{Agents: []collector.Agent{
		{Project: "odomto", Session: &collector.Session{State: collector.SessionWaiting}},
		{Project: "kdx", Session: &collector.Session{State: collector.SessionWorking}},
	}}, nil, now)
	return cfg, st
}

func TestCompute(t *testing.T) {
	cfg, st := fixture(t)
	s := Compute(st.Snapshot(), cfg, now)
	if s.CIBroken != 1 || s.ProdDown != 1 || s.Waiting != 1 || s.Reviews != 2 || s.Dirty != 1 || s.OutOfSync != 1 {
		t.Errorf("summary = %+v", s)
	}
	if got := s.Prompt(); got != "✗2 ◉3" {
		t.Errorf("Prompt = %q", got)
	}
	odomto := s.Projects[0]
	// The local branch's red CI wins over the green default branch.
	if odomto.CI != collector.CIFailure || odomto.CIURL != "https://github.com/o/odomto/actions/runs/1" {
		t.Errorf("odomto = %+v", odomto)
	}
	if got := odomto.Prompt(); got != "✗ CI ↓3 ◉" {
		t.Errorf("odomto prompt = %q", got)
	}
	if got := s.Projects[2].Prompt(); got != "✗ prod" {
		t.Errorf("kdx prompt = %q", got)
	}
	if got := (Summary{}).Prompt(); got != "" {
		t.Errorf("all good = %q, want empty", got)
	}
}

func TestProjectFor(t *testing.T) {
	cfg, st := fixture(t)
	s := Compute(st.Snapshot(), cfg, now)
	sub := filepath.Join(cfg.Projects[0].Path, "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if p, ok := s.ProjectFor(sub); !ok || p.Name != "odomto" {
		t.Errorf("ProjectFor(src) = %q, %v", p.Name, ok)
	}
	if p, ok := s.ProjectFor(cfg.Projects[1].Path); !ok || p.Name != "inner" {
		t.Errorf("nested project = %q, %v", p.Name, ok)
	}
	if _, ok := s.ProjectFor(t.TempDir()); ok {
		t.Error("a folder outside every project matched")
	}
}

func TestSwiftBar(t *testing.T) {
	cfg, st := fixture(t)
	out := Compute(st.Snapshot(), cfg, now).SwiftBar("/usr/local/bin/atalaya")
	lines := strings.Split(out, "\n")
	if lines[0] != "◆ ✗2 ◉3 | color=red" || lines[1] != "---" {
		t.Errorf("header = %q", lines[:2])
	}
	// Broken projects first, linking to what is broken.
	if !strings.HasPrefix(lines[2], "odomto · ✗ CI ↓3 ◉ | href=https://github.com/o/odomto/actions/runs/1") {
		t.Errorf("first item = %q", lines[2])
	}
	if !strings.Contains(out, "Abrir Atalaya | bash=/usr/local/bin/atalaya terminal=true") {
		t.Errorf("no open item:\n%s", out)
	}
}

func TestWriteReadAndKeep(t *testing.T) {
	cfg, st := fixture(t)
	path := filepath.Join(t.TempDir(), "state", "status.json")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Keep(ctx, st, cfg, path, 10*time.Millisecond); close(done) }()
	st.Put("git", collector.GitSnapshot{}, nil, now) // a change to react to
	deadline := time.Now().Add(2 * time.Second)
	var s Summary
	var err error
	for {
		if s, err = Read(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status.json never written: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if s.CIBroken != 1 || len(s.Projects) != 3 {
		t.Errorf("read back = %+v", s)
	}
}
