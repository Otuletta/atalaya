package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/otuletta/atalaya/internal/ai"
	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/gitparse"
	"github.com/otuletta/atalaya/internal/store"
)

// Golden files live in testdata/. Regenerate them after an intended UI
// change with:  go test ./internal/ui -update

var now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

func TestMain(m *testing.M) {
	time.Local = time.UTC // detail views print local times
	os.Exit(m.Run())
}

func testConfig() *config.Config {
	return &config.Config{
		Path: "/cfg/atalaya/config.toml",
		Projects: []config.Project{
			{Name: "odomto", Path: "/w/odomto", GitHub: "otuletta/odomto", Health: []string{"https://api.odomto.com/health"}},
			{Name: "kdx", Path: "/w/kdx"},
			{Name: "cue", Path: "/w/cue", GitHub: "otuletta/cue", Health: []string{"https://cue.dev"}},
		},
		Agents:  config.Agents{Patterns: []string{"claude", "cline"}},
		GitHub:  config.GitHubSettings{BotAuthors: config.DefaultBotAuthors},
		Plugins: []config.Plugin{{Name: "npm-audit", Command: []string{"x"}}, {Name: "roto", Command: []string{"y"}}},
	}
}

func samples(lat ...int64) []collector.Sample {
	var s []collector.Sample
	for i, l := range lat {
		s = append(s, collector.Sample{At: now.Add(time.Duration(i-len(lat)) * 30 * time.Second), LatencyMS: l, OK: l >= 0})
	}
	return s
}

// testStore holds one realistic snapshot of every source.
func testStore() *store.Store {
	st := store.New()
	put := func(name string, s collector.Snapshot) { st.Put(name, s, nil, now.Add(time.Minute)) }
	put("git", collector.GitSnapshot{Repos: []collector.Repo{
		{Project: "odomto", Path: "/w/odomto", Branch: "feat/sparkline", Upstream: "origin/feat/sparkline", Ahead: 2, Behind: 1,
			Changes: gitparse.Counts{Staged: 1, Unstaged: 2, Untracked: 1}, Stash: 1,
			Files:      []collector.FileChange{{XY: "M.", Path: "api/server.go"}, {XY: ".M", Path: "web/app.tsx"}, {XY: "??", Path: "notes.md"}},
			LastCommit: &collector.Commit{Hash: "abc1234def", Author: "Ada", Subject: "feat: sparkline de latencia", When: now.Add(-3 * time.Hour)}},
		{Project: "kdx", Path: "/w/kdx", Branch: "feat/login", Upstream: "origin/feat/login",
			Changes:    gitparse.Counts{Unstaged: 4},
			Worktrees:  []collector.Worktree{{Path: "/w/kdx/.claude/worktrees/passkeys", Branch: "feat/passkeys"}},
			LastCommit: &collector.Commit{Author: "Grace", Subject: "wip: login", When: now.Add(-20 * time.Minute)}},
		{Project: "cue", Path: "/w/cue", Branch: "main", Upstream: "origin/main",
			LastCommit: &collector.Commit{Author: "Ada", Subject: "release 1.0", When: now.Add(-50 * time.Hour)}},
	}})
	put("fetch", collector.FetchSnapshot{Repos: []collector.FetchResult{{Project: "odomto", At: now.Add(-4 * time.Minute)}}})
	put("github", collector.GitHubSnapshot{
		Viewer:    "otuletta",
		RateLimit: collector.RateLimit{Limit: 5000, Remaining: 4987, Cost: 1},
		Repos: []collector.GitHubRepo{
			{Project: "odomto", Repo: "otuletta/odomto", URL: "https://github.com/otuletta/odomto", DefaultBranch: "main",
				CI:           &collector.CIRun{State: collector.CIFailure, Workflow: "CI", URL: "https://github.com/otuletta/odomto/actions/runs/9", Commit: "abc1234", StartedAt: now.Add(-10 * time.Minute), DurationSec: 195},
				FailedChecks: []collector.FailedCheck{{Name: "test"}},
				Branch: &collector.BranchCI{Name: "feat/sparkline", Pushed: true, PRNumber: 42,
					PRURL: "https://github.com/otuletta/odomto/pull/42", URL: "https://github.com/otuletta/odomto/tree/feat/sparkline",
					CI: &collector.CIRun{State: collector.CIPending, Workflow: "CI", StartedAt: now.Add(-80 * time.Second), DurationSec: 80}},
				OpenPRs: 5,
				PRs: []collector.PullRequest{
					{Repo: "otuletta/odomto", Number: 42, Title: "feat: sparkline", URL: "https://github.com/otuletta/odomto/pull/42", Author: "otuletta", Checks: collector.CIPending, UpdatedAt: now.Add(-2 * time.Hour)},
					{Repo: "otuletta/odomto", Number: 41, Title: "Revisa el parser", URL: "https://github.com/otuletta/odomto/pull/41", Author: "grace", Checks: collector.CIFailure, UpdatedAt: now.Add(-26 * time.Hour)},
					{Repo: "otuletta/odomto", Number: 52, Title: "Bump vite from 6.0 to 7.1", URL: "https://github.com/otuletta/odomto/pull/52", Author: "dependabot", Checks: collector.CIFailure, UpdatedAt: now.Add(-3 * time.Hour)},
					{Repo: "otuletta/odomto", Number: 51, Title: "Bump zod from 3 to 4", URL: "https://github.com/otuletta/odomto/pull/51", Author: "dependabot", Checks: collector.CIFailure, UpdatedAt: now.Add(-50 * time.Hour)},
					{Repo: "otuletta/odomto", Number: 50, Title: "Bump actions/checkout from 4 to 5", URL: "https://github.com/otuletta/odomto/pull/50", Author: "dependabot", Checks: collector.CISuccess, UpdatedAt: now.Add(-9 * 24 * time.Hour)},
				},
				ReviewRequests: []collector.PullRequest{
					{Repo: "otuletta/odomto", Number: 41, Title: "Revisa el parser", URL: "https://github.com/otuletta/odomto/pull/41", Author: "grace", Checks: collector.CIFailure, UpdatedAt: now.Add(-26 * time.Hour)},
				}},
			{Project: "cue", Repo: "otuletta/cue", URL: "https://github.com/otuletta/cue", DefaultBranch: "main",
				CI: &collector.CIRun{State: collector.CIPending, Workflow: "Release", StartedAt: now.Add(-45 * time.Second), DurationSec: 45}},
		},
		OtherReviewRequests: []collector.PullRequest{
			{Repo: "acme/infra", Number: 7, Title: "Terraform 2.0", URL: "https://github.com/acme/infra/pull/7", Author: "linus", Checks: collector.CIPending, UpdatedAt: now.Add(-5 * time.Minute)},
		},
	})
	put("health", collector.HealthSnapshot{Checks: []collector.Check{
		{Project: "odomto", URL: "https://api.odomto.com/health", Status: 200, OK: true, LatencyMS: 142, CheckedAt: now,
			Stats:   &collector.HealthStats{Uptime24h: 99.95, Uptime7d: 99.9, P95MS: 310},
			TLS:     &collector.TLSInfo{NotAfter: now.Add(61 * 24 * time.Hour), DaysLeft: 61, Issuer: "R11", Subject: "api.odomto.com"},
			History: samples(120, 130, 180, 150, 400, 142, 138, -1, 140, 142)},
		{Project: "cue", URL: "https://cue.dev", OK: false, LatencyMS: 10000, Err: "timeout", CheckedAt: now,
			Stats: &collector.HealthStats{Uptime24h: 91.3, Uptime7d: 97, Incidents: []collector.Incident{
				{Start: now.Add(-50 * time.Hour), End: now.Add(-49 * time.Hour)}, {Start: now.Add(-10 * time.Minute)}}},
			TLS:     &collector.TLSInfo{DaysLeft: 5},
			History: samples(90, 95, -1, -1)},
	}})
	put("ports", collector.PortsSnapshot{Ports: []collector.Port{
		{Port: 3000, Addr: "*", PID: 4242, Process: "node", Project: "odomto", Cwd: "/w/odomto/web", Started: now.Add(-time.Hour)},
		{Port: 5432, Addr: "localhost", PID: 865, Process: "postgres", Cwd: "/opt/homebrew/var/postgresql@17"},
		{Port: 8080, Addr: "*", PID: 5151, Process: "kdx-server", Project: "kdx", Cwd: "/w/kdx"},
	}})
	put("docker", collector.DockerSnapshot{Available: true, Containers: []collector.Container{
		{ID: "a1", Name: "odomto-db-1", Image: "postgres:17", State: "running", Status: "Up 2 hours", Ports: "0.0.0.0:5433->5432/tcp", Project: "odomto"},
	}})
	put("ollama", collector.OllamaSnapshot{Available: true, Models: []collector.OllamaModel{
		{Name: "qwen3:8b", Params: "8.2B", Quant: "Q4_K_M", VRAMBytes: 6_100_000_000, ExpiresAt: now.Add(4 * time.Minute)},
	}})
	put(collector.PluginSource("npm-audit"), collector.PluginSnapshot{Name: "npm-audit", Title: "npm audit",
		Summary: "2 proyectos · 1 con vulnerabilidades altas o críticas", Columns: []string{"PROYECTO", "TOTAL", "SEVERIDAD"},
		Rows: []collector.PluginRow{
			{Cells: []string{"odomto", "16", "1 crítica, 11 alta"}, Status: collector.PluginError, Project: "odomto",
				URL: "https://github.com/otuletta/odomto/security/dependabot"},
			{Cells: []string{"kdx", "0", "ninguna"}, Status: collector.PluginOK, Project: "kdx"},
		}})
	st.Put(collector.PluginSource("roto"), nil, errors.New("token caducado"), time.Time{})
	put("events", events.Snapshot{Events: []events.Event{
		{At: now.Add(-2 * time.Minute), Kind: "ci_broken", Severity: events.Bad, Project: "odomto", Title: "CI roto en odomto",
			Body: "main · CI", URL: "https://github.com/otuletta/odomto/actions/runs/9"},
		{At: now.Add(-30 * time.Minute), Kind: "review_requested", Severity: events.Ask, Project: "odomto",
			Title: "Te piden review en otuletta/odomto", Body: "#41 Revisa el parser · grace", URL: "https://github.com/otuletta/odomto/pull/41"},
		{At: now.Add(-26 * time.Hour), Kind: "pr_opened", Severity: events.Info, Project: "kdx", Title: "Nuevo PR #7 en kdx", Body: "Login · grace"},
	}})
	put("agents", collector.AgentsSnapshot{Agents: []collector.Agent{
		{PID: 777, Name: "claude", Project: "odomto", Cwd: "/w/odomto", Branch: "feat/sparkline", CPU: 31,
			Started: now.Add(-(2*time.Hour + 13*time.Minute)),
			Session: &collector.Session{ID: "s-777", Title: "Sparkline de latencia", State: collector.SessionWorking,
				Activity: "editando model.go", LastAt: now.Add(-3 * time.Second)}},
		{PID: 779, Name: "claude", Project: "kdx", Cwd: "/w/kdx/.claude/worktrees/passkeys", Branch: "feat/passkeys", Worktree: "passkeys",
			Started: now.Add(-40 * time.Minute),
			Session: &collector.Session{ID: "s-779", Title: "Login con passkeys", State: collector.SessionWaiting, LastAt: now.Add(-6 * time.Minute)}},
		{PID: 888, Name: "cline", Cwd: "/tmp/scratch", Started: now.Add(-90 * time.Second)},
	}})
	return st
}

// fakeActions records calls instead of touching the system.
type fakeActions struct {
	mu     sync.Mutex
	calls  []string
	pullFn func() (string, error)
}

func (f *fakeActions) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeActions) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeActions) Editor(dir string) (*exec.Cmd, bool) {
	return exec.Command("code", dir), false
}
func (f *fakeActions) Start(cmd *exec.Cmd) error {
	f.record("start " + strings.Join(cmd.Args, " "))
	return nil
}
func (f *fakeActions) OpenURL(url string) error { f.record("open " + url); return nil }
func (f *fakeActions) Pull(_ context.Context, dir string) (string, error) {
	f.record("pull " + dir)
	if f.pullFn != nil {
		return f.pullFn()
	}
	return "Fast-forward", nil
}
func (f *fakeActions) Diagnose(_ context.Context, fl ai.Failure) (string, error) {
	f.record("diagnose " + fl.Project + " " + fl.Branch + " " + fl.RunURL)
	return "Falla `go vet` por un import sin usar en model.go.", nil
}

func (f *fakeActions) Fix(_ context.Context, fl ai.Failure) (*exec.Cmd, error) {
	f.record("fix " + fl.Project + " " + fl.Dir)
	return exec.Command("true"), nil
}

func (f *fakeActions) Kill(_ context.Context, pid int32, _ time.Time) error {
	f.record(fmt.Sprint("kill ", pid))
	return nil
}

type harness struct {
	tm       *teatest.TestModel
	actions  *fakeActions
	mu       sync.Mutex
	refresed [][]string
}

func start(t *testing.T) *harness {
	t.Helper()
	return startSized(t, 120, 30)
}

func startSized(t *testing.T, w, ht int) *harness {
	t.Helper()
	h := &harness{actions: &fakeActions{}}
	m := New(Deps{
		Config:  testConfig(),
		Store:   testStore(),
		Actions: h.actions,
		Now:     func() time.Time { return now },
		Version: "v-test",
		Refresh: func(s ...string) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.refresed = append(h.refresed, s)
		},
	})
	h.tm = teatest.NewTestModel(t, m, teatest.WithInitialTermSize(w, ht))
	return h
}

func (h *harness) keys(keys ...string) {
	for _, k := range keys {
		switch k {
		case "enter":
			h.tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "esc":
			h.tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
		case "tab":
			h.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
		default:
			r := []rune(k)[0]
			h.tm.Send(tea.KeyPressMsg{Code: r, Text: k})
		}
	}
}

func (h *harness) waitFor(t *testing.T, text string) {
	t.Helper()
	teatest.WaitFor(t, h.tm.Output(), func(b []byte) bool {
		return bytes.Contains([]byte(ansi.Strip(string(b))), []byte(text))
	}, teatest.WithDuration(3*time.Second))
}

// waitCalls waits until n actions ran. Waiting on screen text is not enough
// when two messages differ in one character: the renderer only redraws the
// cells that changed, so the new text never appears whole in the output.
func (h *harness) waitCalls(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for len(h.actions.Calls()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("waited for %d actions, got %v", n, h.actions.Calls())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) final(t *testing.T) Model {
	t.Helper()
	if err := h.tm.Quit(); err != nil {
		t.Fatal(err)
	}
	return h.tm.FinalModel(t, teatest.WithFinalTimeout(3*time.Second)).(Model)
}

func screen(m Model) string {
	return ansi.Strip(m.View().Content)
}

// sizes are the terminals every screen is checked at: a roomy one and
// 80×24, the default of macOS Terminal.app.
var sizes = []struct{ w, h int }{{120, 30}, {80, 24}}

func eachSize(t *testing.T, fn func(t *testing.T, w, h int)) {
	for _, sz := range sizes {
		t.Run(fmt.Sprintf("%dx%d", sz.w, sz.h), func(t *testing.T) { fn(t, sz.w, sz.h) })
	}
}

func TestPanelsGolden(t *testing.T) {
	eachSize(t, func(t *testing.T, w, ht int) {
		for i, name := range []string{"resumen", "repos", "github", "produccion", "local", "agentes", "actividad", "plugins"} {
			t.Run(name, func(t *testing.T) {
				h := startSized(t, w, ht)
				h.keys(string(rune('0' + i)))
				golden.RequireEqual(t, []byte(screen(h.final(t))))
			})
		}
	})
}

func TestHelpGolden(t *testing.T) {
	eachSize(t, func(t *testing.T, w, ht int) {
		h := startSized(t, w, ht)
		h.keys("?")
		golden.RequireEqual(t, []byte(screen(h.final(t))))
	})
}

func TestRepoDetailGolden(t *testing.T) {
	eachSize(t, func(t *testing.T, w, ht int) {
		h := startSized(t, w, ht)
		h.keys("1", "enter")
		m := h.final(t)
		if !m.detailOpen {
			t.Fatal("enter did not open the detail")
		}
		golden.RequireEqual(t, []byte(screen(m)))
	})
}

// TestNothingOverflows renders every panel and the help at many widths: no
// line may be wider than the terminal, and the tabs and the key hints must
// never be cut.
func TestNothingOverflows(t *testing.T) {
	for w := 60; w <= 160; w += 5 {
		m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
		m.width, m.height = w, 24
		views := map[string]Model{}
		for p := range panelCount {
			m.panel = p
			views[panelNames[p]] = m
		}
		help := m
		help.showHelp = true
		views["ayuda"] = help
		for name, v := range views {
			lines := strings.Split(v.View().Content, "\n")
			for i, l := range lines {
				if lw := ansi.StringWidth(l); lw > w {
					t.Errorf("%d cols, %s, line %d is %d wide: %q", w, name, i, lw, ansi.Strip(l))
				}
			}
			// Every tab number and the active tab's name must be there.
			tabs := ansi.Strip(lines[0])
			if !strings.Contains(tabs, panelNames[v.panel]) || !strings.Contains(tabs, " 5") || strings.Contains(tabs, "…") {
				t.Errorf("%d cols, %s: tabs cut: %q", w, name, tabs)
			}
			if hint := ansi.Strip(lines[len(lines)-1]); strings.Contains(hint, "…") {
				t.Errorf("%d cols, %s: key hints cut: %q", w, name, hint)
			}
		}
	}
}

func TestCommitStaysReadableAt80(t *testing.T) {
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	m.width, m.height, m.panel = 80, 24, PanelRepos
	s := screen(m)
	// The subject keeps its 24-column minimum: 23 characters and "…".
	if !strings.Contains(s, "feat: sparkline de late…") {
		t.Errorf("the commit subject is not readable at 80 columns:\n%s", s)
	}
	if strings.Contains(s, "AUTOR") {
		t.Errorf("the author column should give way to the subject at 80 columns:\n%s", s)
	}
}

func TestNavigationAndTabs(t *testing.T) {
	h := start(t)
	h.keys("1", "j", "j", "j") // stops at the last repo
	h.keys("k")
	h.keys("tab", "tab", "tab") // Repos → GitHub → Producción → Local
	h.keys("j")
	m := h.final(t)
	if m.panel != PanelLocal {
		t.Errorf("panel = %v, want Local", m.panel)
	}
	if m.sel[PanelRepos] != "repo:kdx" {
		t.Errorf("repos selection = %q, want kdx", m.sel[PanelRepos])
	}
	// The first selectable Local row is :3000; j moves to :5432, skipping titles.
	if m.sel[PanelLocal] != "port:5432:865" {
		t.Errorf("local selection = %q", m.sel[PanelLocal])
	}
}

func TestFilter(t *testing.T) {
	h := start(t)
	h.keys("1", "/")
	h.tm.Type("cu")
	h.keys("enter")
	m := h.final(t)
	s := screen(m)
	if !strings.Contains(s, "release 1.0") || strings.Contains(s, "odomto") || strings.Contains(s, "kdx") {
		t.Errorf("filter 'cu' shows:\n%s", s)
	}
	if !strings.Contains(s, "filtro: cu") {
		t.Error("the active filter is not shown in the status line")
	}

	h = start(t)
	h.keys("1", "/")
	h.tm.Type("zzz")
	h.keys("esc")
	if s := screen(h.final(t)); !strings.Contains(s, "odomto") {
		t.Error("esc did not clear the filter")
	}
}

func TestPullAsksForConfirmation(t *testing.T) {
	h := start(t)
	h.keys("1", "p")
	h.waitFor(t, "¿git pull --ff-only en odomto?")
	h.keys("n")
	h.waitFor(t, "cancelado")
	if calls := h.actions.Calls(); len(calls) != 0 {
		t.Fatalf("pull ran without confirmation: %v", calls)
	}
	h.keys("p", "y")
	h.waitFor(t, "pull odomto: Fast-forward")
	h.final(t)
	if calls := h.actions.Calls(); len(calls) != 1 || calls[0] != "pull /w/odomto" {
		t.Errorf("calls = %v", calls)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.refresed) == 0 || h.refresed[len(h.refresed)-1][0] != "git" {
		t.Errorf("a pull must refresh git, refreshes = %v", h.refresed)
	}
}

func TestKillAsksForConfirmation(t *testing.T) {
	h := start(t)
	h.keys("4", "x")
	h.waitFor(t, "¿Matar node (pid 4242) en :3000?")
	h.keys("y")
	h.waitFor(t, "SIGTERM enviado a node (pid 4242)")
	h.final(t)
	if calls := h.actions.Calls(); len(calls) != 1 || calls[0] != "kill 4242" {
		t.Errorf("calls = %v", calls)
	}
}

func TestKillOnlyOnPorts(t *testing.T) {
	h := start(t)
	h.keys("1", "x")
	h.waitFor(t, "x solo aplica a puertos")
	h.final(t)
	if calls := h.actions.Calls(); len(calls) != 0 {
		t.Errorf("calls = %v", calls)
	}
}

func TestOpen(t *testing.T) {
	h := start(t)
	h.keys("1", "o") // repo → editor
	h.waitCalls(t, 1)
	h.keys("2", "o") // failing CI → the broken run
	h.waitCalls(t, 2)
	h.keys("j", "o") // the local branch line: its CI runs, so its PR
	h.waitCalls(t, 3)
	h.keys("j", "o") // then the review request
	h.waitCalls(t, 4)
	h.final(t)
	want := []string{
		"start code /w/odomto",
		"open https://github.com/otuletta/odomto/actions/runs/9",
		"open https://github.com/otuletta/odomto/pull/42",
		"open https://github.com/otuletta/odomto/pull/41",
	}
	if got := h.actions.Calls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestBotPRsAreGrouped(t *testing.T) {
	h := start(t)
	h.keys("2")
	h.waitFor(t, "dependabot · 3 PRs (2 con checks rotos)")
	// repo → branch → #41 (review) → #42 → dependabot
	h.keys("j", "j", "j", "j", "o")
	h.waitCalls(t, 1)
	h.keys("enter")
	h.waitFor(t, "Bump actions/checkout from 4 to 5")
	m := h.final(t)
	s := screen(m)
	if !strings.Contains(s, "dependabot · otuletta/odomto (3 PRs)") {
		t.Errorf("bot detail:\n%s", s)
	}
	calls := h.actions.Calls()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "open https://github.com/otuletta/odomto/pulls?q=") {
		t.Errorf("calls = %v", calls)
	}
}

func TestBotPRsNotListedOneByOne(t *testing.T) {
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	m.width, m.height, m.panel = 120, 30, PanelGitHub
	if s := screen(m); strings.Contains(s, "Bump vite") {
		t.Errorf("bot PRs listed one by one:\n%s", s)
	}
}

func TestAnalyzeAndFixCI(t *testing.T) {
	h := start(t)
	h.keys("2", "a") // odomto: its default branch CI fails
	h.waitCalls(t, 1)
	h.waitFor(t, "Diagnóstico de Claude")
	h.keys("c") // from the note: c fixes the same CI
	h.waitFor(t, "¿Abrir Claude Code en odomto para arreglar el CI de main?")
	h.keys("y")
	h.waitCalls(t, 2)
	h.waitFor(t, "Claude Code cerrado")
	m := h.final(t)
	want := []string{
		"diagnose odomto main https://github.com/otuletta/odomto/actions/runs/9",
		"fix odomto /w/odomto",
	}
	if got := h.actions.Calls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls = %q", got)
	}
	if !strings.Contains(m.viewport.View(), "import sin usar") {
		t.Errorf("note = %q", m.viewport.View())
	}
}

func TestAnalyzeNeedsABrokenCI(t *testing.T) {
	h := start(t)
	h.keys("2", "j", "j", "a") // the branch line runs, it does not fail
	h.waitFor(t, "a y c funcionan sobre un CI roto")
	h.final(t)
	if calls := h.actions.Calls(); len(calls) != 0 {
		t.Errorf("calls = %v", calls)
	}
}

func TestRefresh(t *testing.T) {
	h := start(t)
	h.keys("r")
	h.waitFor(t, "refrescando")
	h.final(t)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.refresed) != 1 || len(h.refresed[0]) != 0 {
		t.Errorf("refreshes = %v, want one refresh of everything", h.refresed)
	}
}

func TestLiveUpdate(t *testing.T) {
	st := testStore()
	m := New(Deps{Config: testConfig(), Store: st, Now: func() time.Time { return now }})
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	tm.Send(tea.KeyPressMsg{Code: '3', Text: "3"})
	st.Put("health", collector.HealthSnapshot{Checks: []collector.Check{
		{Project: "odomto", URL: "https://api.odomto.com/health", Status: 502, LatencyMS: 30, Err: "502 Bad Gateway"},
	}}, nil, time.Time{})
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return bytes.Contains([]byte(ansi.Strip(string(b))), []byte("● 502"))
	}, teatest.WithDuration(3*time.Second))
	_ = tm.Quit()
}

func TestFocusIsReported(t *testing.T) {
	var got []bool
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now },
		Focus: func(focused bool) { got = append(got, focused) }})
	if !m.View().ReportFocus {
		t.Fatal("the view does not ask the terminal for focus events")
	}
	m.Update(tea.BlurMsg{})
	m.Update(tea.FocusMsg{})
	if !slices.Equal(got, []bool{false, true}) {
		t.Errorf("focus calls = %v", got)
	}
	// Without a Focus callback the messages are harmless.
	New(Deps{Config: testConfig(), Now: func() time.Time { return now }}).Update(tea.BlurMsg{})
}

func TestOverview(t *testing.T) {
	h := start(t) // 120 columns: three cards per row
	s := ""
	h.waitFor(t, "0 Resumen")
	h.keys("l", "enter") // second card: GitHub
	m := h.final(t)
	if m.panel != PanelGitHub {
		t.Errorf("enter on the second card opened %v", m.panel)
	}

	m = New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	m.width, m.height = 120, 30
	s = screen(m)
	for _, want := range []string{
		"1 desincronizado · 1 con cambios",            // repos
		"odomto ✗ CI 3m15s", "◉ #41 Revisa el parser", // github: broken CI and a review waiting
		"● cue.dev", "timeout", "● api.odomto.com", // health: the down endpoint first
		":3000 → node · odomto",              // local
		"◉ te espera kdx Login con passkeys", // agents: the one waiting first
		"plugin:roto: token caducado",        // the overview names only failing sources
		"✗ roto · token caducado",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("overview lacks %q:\n%s", want, s)
		}
	}
	m.moveCard(3) // one row down
	if m.overviewSel != 3 {
		t.Errorf("moving a row down from card 0 at 3 columns = %d", m.overviewSel)
	}
	m.moveCard(100)
	if m.overviewSel != len(m.cards())-1 {
		t.Errorf("selection not clamped: %d", m.overviewSel)
	}
}

func TestActivity(t *testing.T) {
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	m.width, m.height = 120, 30
	if m.unseen() != 0 {
		t.Error("events from before this run count as unseen")
	}
	m.seenUntil = now.Add(-time.Hour)
	if n := m.unseen(); n != 2 { // the broken CI and the review, not the plain PR
		t.Errorf("unseen = %d, want 2", n)
	}
	if b := ansi.Strip(m.badge(PanelActivity)); b != "●2" {
		t.Errorf("badge = %q", b)
	}
	// Opening the panel marks everything as seen.
	next, _ := m.Update(tea.KeyPressMsg{Code: '6', Text: "6"})
	if n := next.(Model).unseen(); n != 0 {
		t.Errorf("unseen after opening the panel = %d", n)
	}
}

func TestWindowTitleAndProgress(t *testing.T) {
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	v := m.View()
	if v.WindowTitle != "atalaya · 1 CI roto · 1 caído · 1 te espera" {
		t.Errorf("title = %q", v.WindowTitle)
	}
	for _, b := range []byte(v.WindowTitle) {
		if b >= 0x80 && b <= 0x9f {
			t.Fatalf("title has the C1 byte %#x, which ends an OSC in 8-bit parsers", b)
		}
	}
	if v.ProgressBar != nil {
		t.Error("progress bar shown with every source loaded")
	}

	st := store.New()
	st.Put("git", collector.GitSnapshot{}, nil, time.Time{})
	st.SetRunning("github", true) // registered, not reported yet
	m = New(Deps{Config: testConfig(), Store: st, Now: func() time.Time { return now }})
	v = m.View()
	if v.ProgressBar == nil || v.ProgressBar.Value != 50 {
		t.Errorf("progress = %+v, want 50%%", v.ProgressBar)
	}
	if v.WindowTitle != "atalaya · todo bien" {
		t.Errorf("title = %q", v.WindowTitle)
	}
}

func TestHyperlinks(t *testing.T) {
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	m.width, m.height, m.panel = 120, 30, PanelGitHub
	v := m.View().Content
	for _, url := range []string{
		"https://github.com/otuletta/odomto/pull/41",        // PR number
		"https://github.com/otuletta/odomto/actions/runs/9", // CI run
		"https://github.com/otuletta/odomto",                // repo name
	} {
		if !strings.Contains(v, ansi.SetHyperlink(url)) {
			t.Errorf("no hyperlink to %s", url)
		}
	}
}

func TestSmallTerminal(t *testing.T) {
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	m.width, m.height = 30, 5
	if s := screen(m); !strings.Contains(s, "demasiado pequeña") {
		t.Errorf("small terminal renders:\n%s", s)
	}
}

func TestSummary(t *testing.T) {
	s := ansi.Strip(Summary(testConfig(), testStore().Snapshot(), now, 120))
	for _, want := range []string{"REPOS", "odomto", "GITHUB", "✗ CI 3m15s", "PRODUCCIÓN", "LOCAL", ":3000", "AGENTES", "claude"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary does not contain %q:\n%s", want, s)
		}
	}
}

func TestSparkline(t *testing.T) {
	got := ansi.Strip(sparkline(samples(100, 200, 400, 800, -1), 10))
	if got != "▁▂▄█×" { // 100·7/800=0, 200→1, 400→3, 800→7
		t.Errorf("sparkline = %q", got)
	}
	if got := ansi.Strip(sparkline(samples(1, 2, 3, 4, 5, 6), 3)); len([]rune(got)) != 3 {
		t.Errorf("sparkline not cut to width: %q", got)
	}
	if got := ansi.Strip(sparkline(samples(-1, -1), 5)); got != "××" {
		t.Errorf("all failed = %q", got)
	}
}
