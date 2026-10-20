package notify

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/store"
)

type recorder struct {
	mu   sync.Mutex
	sent []string
}

func (r *recorder) Notify(title, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, title+" | "+body)
	return nil
}

func (r *recorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.sent
	r.sent = nil
	return out
}

func gh(main, branch string) collector.GitHubSnapshot {
	r := collector.GitHubRepo{Project: "odomto", Repo: "otuletta/odomto", DefaultBranch: "main",
		CI: &collector.CIRun{State: main, Workflow: "CI"}}
	if branch != "" {
		r.Branch = &collector.BranchCI{Name: "feat/x", Pushed: true, CI: &collector.CIRun{State: branch}}
	}
	return collector.GitHubSnapshot{Repos: []collector.GitHubRepo{r}}
}

func state(snaps map[string]collector.Snapshot) store.State {
	st := store.New()
	for name, s := range snaps {
		st.Put(name, s, nil, time.Time{})
	}
	return st.Snapshot()
}

func TestCITransitions(t *testing.T) {
	rec := &recorder{}
	d := &Detector{Notifier: rec, Rules: Rules{CI: true, Health: true}}
	steps := []struct {
		main, branch string
		want         []string
	}{
		// Red at startup: that is the baseline, not news.
		{collector.CIFailure, "", nil},
		{collector.CIFailure, "", nil},
		{collector.CIPending, "", nil},
		{collector.CISuccess, "", []string{"✓ CI arreglado en odomto | main · CI vuelve a pasar"}},
		{collector.CISuccess, "", nil},
		{collector.CIPending, "", nil}, // a new run starts…
		{collector.CIFailure, "", []string{"✗ CI roto en odomto | main · CI"}},
		{collector.CIFailure, "", nil}, // …and is only reported once
		// A branch appears: its first state is a baseline too.
		{collector.CIFailure, collector.CISuccess, nil},
		{collector.CIFailure, collector.CIFailure, []string{"✗ CI roto en odomto | feat/x"}},
		{collector.CICancelled, collector.CIFailure, nil},
	}
	for i, s := range steps {
		d.Observe(state(map[string]collector.Snapshot{"github": gh(s.main, s.branch)}))
		if got := rec.take(); !slices.Equal(got, s.want) {
			t.Errorf("step %d (%s/%s): sent %q, want %q", i, s.main, s.branch, got, s.want)
		}
	}
}

func health(results ...bool) collector.HealthSnapshot {
	var hist []collector.Sample
	for _, ok := range results {
		hist = append(hist, collector.Sample{OK: ok})
	}
	c := collector.Check{Project: "landing", URL: "https://landing.dev", OK: results[len(results)-1], History: hist}
	if !c.OK {
		c.Err = "502 Bad Gateway"
	}
	return collector.HealthSnapshot{Checks: []collector.Check{c}}
}

func TestHealthTransitions(t *testing.T) {
	rec := &recorder{}
	d := &Detector{Notifier: rec, Rules: Rules{CI: true, Health: true}}
	steps := []struct {
		hist []bool
		want []string
	}{
		{[]bool{true}, nil},
		{[]bool{true, false}, nil}, // one failure is not an outage
		{[]bool{true, false, true}, nil},
		{[]bool{true, false, true, false}, nil},
		{[]bool{true, false, true, false, false}, []string{"✗ Producción caída en landing | https://landing.dev · 502 Bad Gateway"}},
		{[]bool{false, true, false, false, false}, nil},
		{[]bool{true, false, false, false, true}, []string{"✓ Producción recuperada en landing | https://landing.dev"}},
	}
	for i, s := range steps {
		d.Observe(state(map[string]collector.Snapshot{"health": health(s.hist...)}))
		if got := rec.take(); !slices.Equal(got, s.want) {
			t.Errorf("step %d: sent %q, want %q", i, got, s.want)
		}
	}
}

func TestDownAtStartupIsNotNews(t *testing.T) {
	rec := &recorder{}
	d := &Detector{Notifier: rec, Rules: Rules{Health: true}}
	d.Observe(state(map[string]collector.Snapshot{"health": health(false)}))
	d.Observe(state(map[string]collector.Snapshot{"health": health(false, false)}))
	d.Observe(state(map[string]collector.Snapshot{"health": health(false, false, false)}))
	if got := rec.take(); got != nil {
		t.Errorf("sent %q for an endpoint that was already down", got)
	}
}

func agents(states ...string) collector.AgentsSnapshot {
	var as []collector.Agent
	for i, s := range states {
		as = append(as, collector.Agent{PID: int32(100 + i), Name: "claude", Project: "cue",
			Session: &collector.Session{ID: fmt.Sprint("s", i), State: s, Title: "Proyecto CUE", Activity: "Bash: tests"}})
	}
	return collector.AgentsSnapshot{Agents: as}
}

func TestAgentTransitions(t *testing.T) {
	rec := &recorder{}
	d := &Detector{Notifier: rec, Rules: Rules{Agents: true}}
	w, wait, block := collector.SessionWorking, collector.SessionWaiting, collector.SessionBlocked
	steps := []struct {
		states []string
		want   []string
	}{
		{[]string{wait, w}, nil}, // baseline: already waiting at startup is not news
		{[]string{wait, wait}, []string{"◉ claude te espera en cue | Proyecto CUE"}},
		{[]string{wait, wait}, nil},
		{[]string{w, w}, nil},
		{[]string{w, block}, []string{"⏸ claude puede necesitar permiso en cue | Bash: tests · Proyecto CUE"}},
		{[]string{block, block}, []string{"⏸ claude puede necesitar permiso en cue | Bash: tests · Proyecto CUE"}},
		{[]string{wait, block}, nil}, // blocked → waiting: it never went back to work
	}
	for i, s := range steps {
		d.Observe(state(map[string]collector.Snapshot{"agents": agents(s.states...)}))
		if got := rec.take(); !slices.Equal(got, s.want) {
			t.Errorf("step %d %v: sent %q, want %q", i, s.states, got, s.want)
		}
	}
}

func repoWithPRs(numbers ...int) collector.GitHubRepo {
	r := collector.GitHubRepo{Project: "kdx", Repo: "o/kdx", URL: "https://github.com/o/kdx"}
	for _, n := range numbers {
		r.PRs = append(r.PRs, collector.PullRequest{Number: n, Title: fmt.Sprint("pr ", n), Author: "ada",
			URL: fmt.Sprint("https://github.com/o/kdx/pull/", n)})
	}
	r.OpenPRs = len(r.PRs)
	return r
}

func TestEventsAreLoggedAndPublished(t *testing.T) {
	rec := &recorder{}
	log, _ := events.Open("")
	st := store.New()
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	d := &Detector{Notifier: rec, Rules: Rules{Reviews: true}, Log: log, Store: st, Now: func() time.Time { return at }}

	review := collector.PullRequest{Repo: "o/kdx", Number: 9, Title: "Passkeys", Author: "grace", URL: "https://github.com/o/kdx/pull/9"}
	gh := func(r collector.GitHubRepo, reviews ...collector.PullRequest) collector.GitHubSnapshot {
		r.ReviewRequests = reviews
		return collector.GitHubSnapshot{Repos: []collector.GitHubRepo{r}}
	}
	d.Observe(state(map[string]collector.Snapshot{"github": gh(repoWithPRs(1, 2))}))         // baseline
	d.Observe(state(map[string]collector.Snapshot{"github": gh(repoWithPRs(2, 3), review)})) // 1 closed, 3 opened, a review
	d.Observe(state(map[string]collector.Snapshot{"github": gh(repoWithPRs(2, 3), review)})) // nothing new

	got := log.Recent(10)
	var kinds []string
	for _, e := range got {
		kinds = append(kinds, e.Kind+": "+e.Title)
	}
	want := []string{
		"review_requested: Te piden review en o/kdx",
		"pr_closed: PR #1 cerrado o mergeado en kdx",
		"pr_opened: Nuevo PR #3 en kdx",
	}
	if !slices.Equal(kinds, want) {
		t.Errorf("events (newest first):\n%s\nwant:\n%s", strings.Join(kinds, "\n"), strings.Join(want, "\n"))
	}
	if !got[0].At.Equal(at) || got[0].URL != review.URL {
		t.Errorf("review event = %+v", got[0])
	}
	// Only the review becomes a notification; PRs are just logged.
	if n := rec.take(); !slices.Equal(n, []string{"◉ Te piden review en o/kdx | #9 Passkeys · grace"}) {
		t.Errorf("notified %q", n)
	}
	pub, ok := store.Data[events.Snapshot](st.Snapshot(), "events")
	if !ok || len(pub.Events) != 3 {
		t.Errorf("published = %+v", pub)
	}
}

func TestPRsBeyondTheFirstPageAreIgnored(t *testing.T) {
	log, _ := events.Open("")
	d := &Detector{Notifier: &recorder{}, Log: log}
	r := repoWithPRs(1, 2)
	r.OpenPRs = 40 // more than the query returned
	d.Observe(state(map[string]collector.Snapshot{"github": collector.GitHubSnapshot{Repos: []collector.GitHubRepo{r}}}))
	r = repoWithPRs(2, 5)
	r.OpenPRs = 40
	d.Observe(state(map[string]collector.Snapshot{"github": collector.GitHubSnapshot{Repos: []collector.GitHubRepo{r}}}))
	if got := log.Recent(10); len(got) != 0 {
		t.Errorf("events for a truncated PR list: %+v", got)
	}
}

func TestAgentsStartAndEnd(t *testing.T) {
	log, _ := events.Open("")
	d := &Detector{Notifier: &recorder{}, Log: log}
	a := func(pids ...int32) collector.AgentsSnapshot {
		var as []collector.Agent
		for _, p := range pids {
			as = append(as, collector.Agent{PID: p, Name: "claude", Project: "cue"})
		}
		return collector.AgentsSnapshot{Agents: as}
	}
	d.Observe(state(map[string]collector.Snapshot{"agents": a(1)}))
	d.Observe(state(map[string]collector.Snapshot{"agents": a(1, 2)}))
	d.Observe(state(map[string]collector.Snapshot{"agents": a(2)}))
	var titles []string
	for _, e := range log.Recent(10) {
		titles = append(titles, e.Title)
	}
	if !slices.Equal(titles, []string{"claude terminó en cue", "claude empezó en cue"}) {
		t.Errorf("events = %q", titles)
	}
}

func TestRulesDisableKinds(t *testing.T) {
	rec := &recorder{}
	d := &Detector{Notifier: rec, Rules: Rules{CI: false, Health: false}}
	for _, s := range []string{collector.CISuccess, collector.CIFailure} {
		d.Observe(state(map[string]collector.Snapshot{"github": gh(s, ""), "health": health(true)}))
	}
	d.Observe(state(map[string]collector.Snapshot{"health": health(true, false, false)}))
	if got := rec.take(); got != nil {
		t.Errorf("sent %q with every kind disabled", got)
	}
}

func TestWatch(t *testing.T) {
	rec := &recorder{}
	d := &Detector{Notifier: rec, Rules: Rules{CI: true}}
	st := store.New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.Watch(ctx, st); close(done) }()

	st.Put("github", gh(collector.CISuccess, ""), nil, time.Time{})
	time.Sleep(50 * time.Millisecond)
	st.Put("github", gh(collector.CIFailure, ""), nil, time.Time{})
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec.mu.Lock()
		n := len(rec.sent)
		rec.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no notification after a CI failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
}

func TestMacArgsKeepTextOutOfTheScript(t *testing.T) {
	title, body := `✗ CI roto en "odomto"`, `fix: comillas " y \ barras`
	args := macArgs(title, body)
	script := strings.Join(args[:len(args)-2], " ")
	if strings.Contains(script, "odomto") || strings.Contains(script, "comillas") {
		t.Errorf("text spliced into the AppleScript: %q", script)
	}
	if args[len(args)-2] != title || args[len(args)-1] != body {
		t.Errorf("argv = %q", args[len(args)-2:])
	}
}

// TestRealNotification shows one real notification. It only runs with
// ATALAYA_NOTIFY_TEST=1, because it pops up on the desktop.
func TestRealNotification(t *testing.T) {
	if os.Getenv("ATALAYA_NOTIFY_TEST") == "" {
		t.Skip("set ATALAYA_NOTIFY_TEST=1 to show a real notification")
	}
	if err := System().Notify("Atalaya · prueba", `Si ves esto, las notificaciones funcionan ("comillas" incluidas).`); err != nil {
		t.Fatal(err)
	}
}
