// Package demo provides fake collectors with believable, deterministic data.
// `atalaya --demo` runs the real scheduler, store and UI on top of them, so
// the README GIF (demo.tape) can be recorded the same way on any machine.
package demo

import (
	"context"
	"math/rand/v2"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/gitparse"
)

// Config is the demo's project list.
func Config() *config.Config {
	return &config.Config{
		Path: "~/.config/atalaya/config.toml",
		Projects: []config.Project{
			{Name: "odomto", Path: "/demo/odomto", GitHub: "otuletta/odomto", Health: []string{"https://api.odomto.com/health", "https://odomto.com"}},
			{Name: "kdx", Path: "/demo/kdx", GitHub: "otuletta/kdx"},
			{Name: "cue", Path: "/demo/cue", GitHub: "otuletta/cue", Health: []string{"https://cue.dev"}},
			{Name: "landing", Path: "/demo/landing", Health: []string{"https://landing.otuletta.dev"}},
		},
		Agents: config.Agents{Patterns: []string{"claude", "cline"}},
	}
}

// Collectors returns the fake sources. start anchors every relative time.
func Collectors(start time.Time) []collector.Collector {
	return []collector.Collector{
		&fake{name: "git", every: 5 * time.Second, fn: func(time.Duration) collector.Snapshot { return git(start) }},
		&fake{name: "fetch", every: 10 * time.Minute, fn: func(time.Duration) collector.Snapshot {
			return collector.FetchSnapshot{Repos: []collector.FetchResult{{Project: "odomto", At: start}, {Project: "kdx", At: start}}}
		}},
		&fake{name: "github", every: 3 * time.Second, fn: func(el time.Duration) collector.Snapshot { return github(start, el) }},
		newHealth(start),
		&fake{name: "ports", every: 3 * time.Second, fn: func(time.Duration) collector.Snapshot { return ports(start) }},
		&fake{name: "docker", every: 10 * time.Second, fn: func(time.Duration) collector.Snapshot { return docker() }},
		&fake{name: "ollama", every: 10 * time.Second, fn: func(time.Duration) collector.Snapshot { return ollama(start) }},
		&fake{name: "agents", every: 5 * time.Second, fn: func(time.Duration) collector.Snapshot { return agents(start) }},
	}
}

// fake is a collector whose data depends only on the time since start.
type fake struct {
	name  string
	every time.Duration
	fn    func(elapsed time.Duration) collector.Snapshot

	once  sync.Once
	start time.Time
}

func (f *fake) Name() string            { return f.name }
func (f *fake) Interval() time.Duration { return f.every }
func (f *fake) Collect(context.Context) (collector.Snapshot, error) {
	f.once.Do(func() { f.start = time.Now() })
	return f.fn(time.Since(f.start)), nil
}

func git(start time.Time) collector.Snapshot {
	return collector.GitSnapshot{Repos: []collector.Repo{
		{Project: "odomto", Path: "/demo/odomto", Branch: "main", Upstream: "origin/main", Behind: 3,
			LastCommit: &collector.Commit{Hash: "9f1c2ab", Author: "Ada", Subject: "fix: reintentos con backoff", When: start.Add(-2 * time.Hour)}},
		{Project: "kdx", Path: "/demo/kdx", Branch: "feat/sparkline", Upstream: "origin/feat/sparkline", Ahead: 2,
			Changes: gitparse.Counts{Staged: 2, Unstaged: 3, Untracked: 1}, Stash: 1,
			Files: []collector.FileChange{{XY: "M.", Path: "internal/ui/spark.go"}, {XY: "A.", Path: "internal/ui/spark_test.go"},
				{XY: ".M", Path: "README.md"}, {XY: ".M", Path: "go.mod"}, {XY: ".M", Path: "go.sum"}, {XY: "??", Path: "notes.txt"}},
			LastCommit: &collector.Commit{Hash: "41de07b", Author: "otuletta", Subject: "feat: sparkline en el panel de health", When: start.Add(-25 * time.Minute)}},
		{Project: "cue", Path: "/demo/cue", Branch: "main", Upstream: "origin/main",
			LastCommit: &collector.Commit{Hash: "c0ffee1", Author: "Grace", Subject: "release v1.4.0", When: start.Add(-26 * time.Hour)}},
		{Project: "landing", Path: "/demo/landing", Branch: "redesign", Upstream: "origin/redesign",
			Changes:    gitparse.Counts{Unstaged: 1},
			LastCommit: &collector.Commit{Hash: "77aa310", Author: "otuletta", Subject: "wip: hero nuevo", When: start.Add(-10 * time.Minute)}},
	}}
}

func pr(repo string, n int, title, author, checks string, age time.Duration, start time.Time) collector.PullRequest {
	return collector.PullRequest{Repo: repo, Number: n, Title: title, Author: author, Checks: checks,
		URL: "https://github.com/" + repo + "/pull/" + strconv.Itoa(n), UpdatedAt: start.Add(-age)}
}

func github(start time.Time, el time.Duration) collector.Snapshot {
	// kdx's CI runs for the first seconds of the demo, then goes green.
	kdxCI := &collector.CIRun{State: collector.CIPending, Workflow: "CI", StartedAt: start, DurationSec: int64(el.Seconds()) + 41}
	if el > 12*time.Second {
		kdxCI = &collector.CIRun{State: collector.CISuccess, Workflow: "CI", StartedAt: start, DurationSec: 53}
	}
	review := pr("otuletta/odomto", 88, "Migrar a Bubble Tea v2", "grace", collector.CISuccess, 40*time.Minute, start)
	return collector.GitHubSnapshot{
		Viewer:    "otuletta",
		RateLimit: collector.RateLimit{Limit: 5000, Remaining: 4991, Cost: 1},
		Repos: []collector.GitHubRepo{
			{Project: "odomto", Repo: "otuletta/odomto", URL: "https://github.com/otuletta/odomto", DefaultBranch: "main",
				CI:           &collector.CIRun{State: collector.CIFailure, Workflow: "CI", StartedAt: start.Add(-time.Hour), DurationSec: 187},
				FailedChecks: []collector.FailedCheck{{Name: "test (macos)"}},
				OpenPRs:      3,
				PRs: []collector.PullRequest{
					review,
					pr("otuletta/odomto", 91, "fix: timeout en health checks", "otuletta", collector.CIFailure, 15*time.Minute, start),
					pr("otuletta/odomto", 87, "docs: arquitectura", "otuletta", collector.CISuccess, 30*time.Hour, start),
				},
				ReviewRequests: []collector.PullRequest{review}},
			{Project: "kdx", Repo: "otuletta/kdx", URL: "https://github.com/otuletta/kdx", DefaultBranch: "main", CI: kdxCI, OpenPRs: 1,
				PRs: []collector.PullRequest{pr("otuletta/kdx", 12, "feat: sparkline", "otuletta", kdxCI.State, 5*time.Minute, start)}},
			{Project: "cue", Repo: "otuletta/cue", URL: "https://github.com/otuletta/cue", DefaultBranch: "main",
				CI: &collector.CIRun{State: collector.CISuccess, Workflow: "Release", StartedAt: start.Add(-26 * time.Hour), DurationSec: 312}},
		},
	}
}

// health produces latencies from a seeded generator, so the sparklines move
// but look the same on every recording.
type health struct {
	start time.Time
	mu    sync.Mutex
	rng   *rand.Rand
	hist  map[string][]collector.Sample
	n     int
}

func newHealth(start time.Time) *health {
	h := &health{start: start, rng: rand.New(rand.NewPCG(7, 42)), hist: map[string][]collector.Sample{}}
	for range 30 { // pre-fill the last minutes
		h.step(start)
	}
	return h
}

func (h *health) Name() string            { return "health" }
func (h *health) Interval() time.Duration { return 2 * time.Second }

var endpoints = []struct {
	project, url string
	base         int64
	days         int
}{
	{"odomto", "https://api.odomto.com/health", 120, 61},
	{"odomto", "https://odomto.com", 80, 61},
	{"cue", "https://cue.dev", 210, 9},
	{"landing", "https://landing.otuletta.dev", 95, 74},
}

func (h *health) step(now time.Time) collector.HealthSnapshot {
	h.n++
	var snap collector.HealthSnapshot
	for _, e := range endpoints {
		lat := e.base + h.rng.Int64N(e.base/2)
		if h.n%9 == 0 && e.project == "odomto" {
			lat *= 4 // a spike now and then
		}
		ok := true
		// landing goes down a few checks into the demo.
		if e.project == "landing" && h.n > 34 {
			ok = false
		}
		c := collector.Check{Project: e.project, URL: e.url, OK: ok, LatencyMS: lat, CheckedAt: now,
			TLS: &collector.TLSInfo{DaysLeft: e.days, NotAfter: now.Add(time.Duration(e.days) * 24 * time.Hour), Issuer: "R11", Subject: e.url[8:]}}
		if ok {
			c.Status = 200
		} else {
			c.Status, c.Err = 502, "502 Bad Gateway"
		}
		hist := h.hist[e.url]
		hist = append(hist, collector.Sample{At: now, LatencyMS: lat, OK: ok})
		if len(hist) > 40 {
			hist = hist[len(hist)-40:]
		}
		h.hist[e.url] = hist
		c.History = append([]collector.Sample(nil), hist...)
		snap.Checks = append(snap.Checks, c)
	}
	return snap
}

func (h *health) Collect(context.Context) (collector.Snapshot, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.step(time.Now()), nil
}

func ports(start time.Time) collector.Snapshot {
	return collector.PortsSnapshot{Ports: []collector.Port{
		{Port: 3000, Addr: "*", PID: 48211, Process: "node", Project: "odomto", Cwd: "/demo/odomto/web", Cmdline: "node .next/server.js", Started: start.Add(-time.Hour)},
		{Port: 5173, Addr: "localhost", PID: 48390, Process: "node", Project: "landing", Cwd: "/demo/landing", Cmdline: "node vite", Started: start.Add(-10 * time.Minute)},
		{Port: 5432, Addr: "localhost", PID: 865, Process: "postgres", Cwd: "/opt/homebrew/var/postgresql@17", Started: start.Add(-72 * time.Hour)},
		{Port: 8080, Addr: "*", PID: 50102, Process: "kdx-server", Project: "kdx", Cwd: "/demo/kdx", Cmdline: "kdx-server --dev", Started: start.Add(-3 * time.Minute)},
		{Port: 11434, Addr: "localhost", PID: 1203, Process: "ollama", Cwd: "/", Cmdline: "ollama serve", Started: start.Add(-48 * time.Hour)},
	}}
}

func docker() collector.Snapshot {
	return collector.DockerSnapshot{Available: true, Containers: []collector.Container{
		{ID: "4be1", Name: "odomto-db-1", Image: "postgres:17", State: "running", Status: "Up 3 hours", Ports: "0.0.0.0:5433->5432/tcp", Project: "odomto"},
		{ID: "9ac0", Name: "odomto-redis-1", Image: "redis:8-alpine", State: "running", Status: "Up 3 hours", Ports: "6379/tcp", Project: "odomto"},
	}}
}

func ollama(start time.Time) collector.Snapshot {
	return collector.OllamaSnapshot{Available: true, Models: []collector.OllamaModel{
		{Name: "qwen3-coder:30b", Params: "30.5B", Quant: "Q4_K_M", SizeBytes: 19_000_000_000, VRAMBytes: 19_000_000_000, ExpiresAt: start.Add(4 * time.Minute)},
	}}
}

func agents(start time.Time) collector.Snapshot {
	return collector.AgentsSnapshot{Agents: []collector.Agent{
		{PID: 51200, Name: "claude", Project: "kdx", Cwd: "/demo/kdx", Started: start.Add(-(47 * time.Minute)), Cmdline: "claude"},
		{PID: 51388, Name: "claude", Project: "odomto", Cwd: "/demo/odomto", Started: start.Add(-(2*time.Hour + 5*time.Minute)), Cmdline: "claude --resume"},
		{PID: 52011, Name: "cline", Project: "landing", Cwd: "/demo/landing", Started: start.Add(-8 * time.Minute), Cmdline: "cline"},
	}}
}

// Actions are inert in the demo: nothing real may be opened, pulled or
// killed while recording.
type Actions struct{}

func (Actions) Editor(dir string) (*exec.Cmd, bool)          { return exec.Command("editor", dir), false }
func (Actions) Start(*exec.Cmd) error                        { return nil }
func (Actions) OpenURL(string) error                         { return nil }
func (Actions) Pull(context.Context, string) (string, error) { return "Already up to date.", nil }
func (Actions) Kill(context.Context, int32, time.Time) error { return nil }
