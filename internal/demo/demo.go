// Package demo provides fake collectors with believable, deterministic data.
// `atalaya --demo` runs the real scheduler, store and UI on top of them, so
// the README GIF (demo.tape) can be recorded the same way on any machine.
package demo

import (
	"context"
	"errors"
	"math/rand/v2"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"github.com/otuletta/atalaya/internal/ai"
	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/events"
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
		Agents:  config.Agents{Patterns: []string{"claude", "cline"}},
		GitHub:  config.GitHubSettings{BotAuthors: config.DefaultBotAuthors},
		Plugins: []config.Plugin{{Name: "npm-audit", Command: []string{"demo"}, Interval: config.Duration{Duration: time.Hour}}},
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
		&fake{name: "agents", every: 2 * time.Second, fn: func(el time.Duration) collector.Snapshot { return agents(start, el) }},
		&fake{name: "usage", every: time.Minute, fn: func(time.Duration) collector.Snapshot { return usage(start) }},
		&fake{name: collector.PluginSource("npm-audit"), every: time.Hour, fn: func(time.Duration) collector.Snapshot { return npmAudit() }},
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
			Worktrees: []collector.Worktree{{Path: "/demo/kdx/.claude/worktrees/passkeys", Branch: "feat/passkeys"}},
			Changes:   gitparse.Counts{Staged: 2, Unstaged: 3, Untracked: 1}, Stash: 1,
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
	bump := func(n int, title, checks string, age time.Duration) collector.PullRequest {
		return pr("otuletta/odomto", n, title, "dependabot", checks, age, start)
	}
	return collector.GitHubSnapshot{
		Viewer:    "otuletta",
		RateLimit: collector.RateLimit{Limit: 5000, Remaining: 4991, Cost: 1},
		Repos: []collector.GitHubRepo{
			{Project: "odomto", Repo: "otuletta/odomto", URL: "https://github.com/otuletta/odomto", DefaultBranch: "main",
				CI: &collector.CIRun{State: collector.CIFailure, Workflow: "CI", StartedAt: start.Add(-time.Hour), DurationSec: 187,
					URL: "https://github.com/otuletta/odomto/actions/runs/1234"},
				FailedChecks: []collector.FailedCheck{{Name: "test (macos)"}},
				OpenPRs:      7,
				PRs: []collector.PullRequest{
					review,
					pr("otuletta/odomto", 91, "fix: timeout en health checks", "otuletta", collector.CIFailure, 15*time.Minute, start),
					pr("otuletta/odomto", 87, "docs: arquitectura", "otuletta", collector.CISuccess, 30*time.Hour, start),
					bump(95, "Bump vite from 6.3.5 to 7.1.0", collector.CIFailure, 2*time.Hour),
					bump(94, "Bump zod from 3.25 to 4.1", collector.CIFailure, 26*time.Hour),
					bump(93, "Bump @types/node from 22 to 24", collector.CISuccess, 3*24*time.Hour),
					bump(92, "Bump actions/checkout from 4 to 5", collector.CISuccess, 6*24*time.Hour),
				},
				ReviewRequests: []collector.PullRequest{review}},
			{Project: "kdx", Repo: "otuletta/kdx", URL: "https://github.com/otuletta/kdx", DefaultBranch: "main",
				CI:      &collector.CIRun{State: collector.CISuccess, Workflow: "CI", StartedAt: start.Add(-5 * time.Hour), DurationSec: 49},
				OpenPRs: 1,
				// kdx is checked out on feat/sparkline (see git above).
				Branch: &collector.BranchCI{Name: "feat/sparkline", Pushed: true, CI: kdxCI, PRNumber: 12,
					PRURL: "https://github.com/otuletta/kdx/pull/12", URL: "https://github.com/otuletta/kdx/tree/feat/sparkline"},
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
	uptime       float64
}{
	{"odomto", "https://api.odomto.com/health", 120, 61, 100},
	{"odomto", "https://odomto.com", 80, 61, 99.98},
	{"cue", "https://cue.dev", 210, 9, 97.2},
	{"landing", "https://landing.otuletta.dev", 95, 74, 100},
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
			TLS:   &collector.TLSInfo{DaysLeft: e.days, NotAfter: now.Add(time.Duration(e.days) * 24 * time.Hour), Issuer: "R11", Subject: e.url[8:]},
			Stats: &collector.HealthStats{Uptime24h: e.uptime, Uptime7d: e.uptime - 0.02, P95MS: e.base * 2}}
		if e.project == "cue" {
			c.Stats.Incidents = []collector.Incident{{Start: h.start.Add(-3 * time.Hour), End: h.start.Add(-3*time.Hour + 4*time.Minute)}}
		}
		if !ok {
			c.Stats.Uptime24h -= float64(h.n-34) * 0.4
			c.Stats.Incidents = []collector.Incident{{Start: now.Add(-time.Duration(h.n-34) * 2 * time.Second)}}
		}
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

// agents: one working, one that finishes its turn a few seconds into the
// demo (and notifies), one stuck in a worktree and Cline without a
// transcript.
func agents(start time.Time, el time.Duration) collector.Snapshot {
	kdx := &collector.Session{ID: "d1", Title: "Sparkline en health", State: collector.SessionWorking,
		Activity: "Bash: go test ./internal/ui", LastAt: start}
	if el > 8*time.Second {
		kdx = &collector.Session{ID: "d1", Title: "Sparkline en health", State: collector.SessionWaiting, LastAt: start.Add(8 * time.Second)}
	}
	return collector.AgentsSnapshot{Agents: []collector.Agent{
		{PID: 51200, Name: "claude", Project: "kdx", Cwd: "/demo/kdx", Branch: "feat/sparkline", CPU: 38,
			Started: start.Add(-(47 * time.Minute)), Cmdline: "claude", Session: kdx},
		{PID: 51388, Name: "claude", Project: "odomto", Cwd: "/demo/odomto", Branch: "main", CPU: 12,
			Started: start.Add(-(2*time.Hour + 5*time.Minute)), Cmdline: "claude --resume",
			Session: &collector.Session{ID: "d2", Title: "Reintentos con backoff", State: collector.SessionWorking,
				Activity: "editando health.go", LastAt: start}},
		{PID: 51420, Name: "claude", Project: "kdx", Cwd: "/demo/kdx/.claude/worktrees/passkeys", Branch: "feat/passkeys", Worktree: "passkeys",
			Started: start.Add(-12 * time.Minute), Cmdline: "claude",
			Session: &collector.Session{ID: "d3", Title: "Login con passkeys", State: collector.SessionBlocked,
				Activity: "Bash: npm install @simplewebauthn/server", LastAt: start.Add(-2 * time.Minute)}},
		{PID: 52011, Name: "cline", Project: "landing", Cwd: "/demo/landing", Started: start.Add(-8 * time.Minute), Cmdline: "cline"},
	}}
}

// usage is a week of Claude Code spend, matching the demo's sessions.
func usage(start time.Time) collector.Snapshot {
	u := collector.UsageSnapshot{
		Projects: []collector.ProjectUsage{
			{Project: "kdx", Tokens: collector.Tokens{CacheRead: 412_000_000, Output: 2_900_000}, CostUSD: 148.20},
			{Project: "odomto", Tokens: collector.Tokens{CacheRead: 268_000_000, Output: 1_700_000}, CostUSD: 96.75},
			{Project: "landing", Tokens: collector.Tokens{CacheRead: 52_000_000, Output: 400_000}, CostUSD: 19.40},
		},
		Sessions: []collector.SessionUsage{
			{ID: "d1", Project: "kdx", Model: "claude-opus-5-5", Messages: 388, CostUSD: 21.60,
				Tokens: collector.Tokens{Input: 12_000, Output: 340_000, CacheWrite5m: 1_100_000, CacheRead: 14_800_000}},
			{ID: "d2", Project: "odomto", Model: "claude-opus-5-5", Messages: 612, CostUSD: 34.15,
				Tokens: collector.Tokens{Input: 20_000, Output: 520_000, CacheWrite5m: 1_900_000, CacheRead: 23_400_000}},
			{ID: "d3", Project: "kdx", Model: "claude-sonnet-5-5", Messages: 74, CostUSD: 2.85,
				Tokens: collector.Tokens{Input: 3_000, Output: 61_000, CacheWrite5m: 300_000, CacheRead: 4_100_000}},
		},
	}
	for i, c := range []float64{18.2, 44.9, 31.0, 7.5, 52.3, 38.4, 41.05} {
		u.Days = append(u.Days, collector.DayUsage{Day: start.AddDate(0, 0, i-6).Format("2006-01-02"), CostUSD: c,
			Tokens: collector.Tokens{CacheRead: int64(c * 3.4e6), Output: int64(c * 21e3)}})
	}
	u.Today = u.Days[len(u.Days)-1]
	return u
}

// npmAudit is what examples/plugins/npm-audit.py prints for the demo.
func npmAudit() collector.Snapshot {
	return collector.PluginSnapshot{Name: "npm-audit", Title: "npm audit", Summary: "3 proyectos · 1 con vulnerabilidades altas o críticas",
		Columns: []string{"PROYECTO", "TOTAL", "SEVERIDAD"},
		Rows: []collector.PluginRow{
			{Cells: []string{"odomto", "6", "1 crítica, 3 alta, 2 moderada"}, Status: collector.PluginError, Project: "odomto",
				URL: "https://github.com/otuletta/odomto/security/dependabot", Detail: "npm audit fix arregla lo que no rompe dependencias"},
			{Cells: []string{"landing", "2", "2 moderada"}, Status: collector.PluginWarn, Project: "landing"},
			{Cells: []string{"kdx", "0", "ninguna"}, Status: collector.PluginOK, Project: "kdx"},
		}}
}

// SeedEvents gives the demo's Activity panel a believable past.
func SeedEvents(log *events.Log, now time.Time) {
	for _, e := range []events.Event{
		{At: now.Add(-5 * time.Hour), Kind: "agent_ended", Severity: events.Info, Project: "landing", Title: "claude terminó en landing", Body: "Hero nuevo"},
		{At: now.Add(-3 * time.Hour), Kind: "prod_down", Severity: events.Bad, Project: "cue", Title: "Producción caída en cue", Body: "https://cue.dev · timeout"},
		{At: now.Add(-3*time.Hour + 4*time.Minute), Kind: "prod_up", Severity: events.Good, Project: "cue", Title: "Producción recuperada en cue", Body: "https://cue.dev"},
		{At: now.Add(-2 * time.Hour), Kind: "pr_opened", Severity: events.Info, Project: "odomto", Title: "Nuevo PR #95 en odomto", Body: "Bump vite from 6.3.5 to 7.1.0 · dependabot"},
		{At: now.Add(-time.Hour), Kind: "ci_broken", Severity: events.Bad, Project: "odomto", Title: "CI roto en odomto", Body: "main · CI",
			URL: "https://github.com/otuletta/odomto/actions/runs/1234"},
		{At: now.Add(-40 * time.Minute), Kind: "review_requested", Severity: events.Ask, Project: "odomto", Title: "Te piden review en otuletta/odomto",
			Body: "#88 Migrar a Bubble Tea v2 · grace", URL: "https://github.com/otuletta/odomto/pull/88"},
	} {
		_ = log.Append(e)
	}
}

// Actions are inert in the demo: nothing real may be opened, pulled or
// killed while recording.
type Actions struct{}

func (Actions) Editor(dir string) (*exec.Cmd, bool)          { return exec.Command("editor", dir), false }
func (Actions) Start(*exec.Cmd) error                        { return nil }
func (Actions) OpenURL(string) error                         { return nil }
func (Actions) Pull(context.Context, string) (string, error) { return "Already up to date.", nil }
func (Actions) Kill(context.Context, int32, time.Time) error { return nil }
func (Actions) RerunFailed(context.Context, string) error    { return nil }
func (Actions) MergePR(context.Context, string, int) error   { return nil }

// Diagnose answers like Claude would about the demo's broken CI.
func (Actions) Diagnose(context.Context, ai.Failure) (string, error) {
	time.Sleep(1500 * time.Millisecond)
	return "El job «test (macos)» falla en TestHealthTimeout: el servidor de prueba responde en 120 ms " +
		"pero el test fija un timeout de 100 ms, y en los runners de macOS el arranque es más lento.\n\n" +
		"Arreglo: sube el timeout del test a 500 ms o, mejor, inyecta un reloj y no dependas del tiempo real " +
		"(internal/collector/health_test.go:72).", nil
}

// Fix never opens Claude Code in the demo.
func (Actions) Fix(context.Context, ai.Failure) (*exec.Cmd, error) {
	return nil, errors.New("en la demo no se abre Claude Code; con tus repos, c lo abre aquí mismo")
}
