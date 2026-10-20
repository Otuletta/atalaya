package notify

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
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
