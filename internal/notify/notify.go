// Package notify sends desktop notifications when something breaks or
// recovers: a CI that turns red, a production endpoint that goes down. It
// reads the same store as the UI and only reports transitions, so leaving
// Atalaya open in a background tab is enough to find out.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/store"
)

// Notifier shows one notification.
type Notifier interface {
	Notify(title, body string) error
}

// System returns this OS's notifier: osascript on macOS, notify-send on
// Linux (when installed) and nothing elsewhere.
func System() Notifier {
	switch runtime.GOOS {
	case "darwin":
		return macOS{}
	case "linux":
		if _, err := exec.LookPath("notify-send"); err == nil {
			return linux{}
		}
	}
	return none{}
}

type macOS struct{}

func (macOS) Notify(title, body string) error {
	return exec.Command("osascript", macArgs(title, body)...).Run()
}

// macArgs passes title and body to the script as arguments instead of
// splicing them into AppleScript, so a quote in a PR title can neither break
// nor inject into the script.
func macArgs(title, body string) []string {
	return []string{
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, body,
	}
}

type linux struct{}

func (linux) Notify(title, body string) error {
	return exec.Command("notify-send", "--app-name=Atalaya", title, body).Run()
}

type none struct{}

func (none) Notify(string, string) error { return nil }

// Rules says which transitions to report.
type Rules struct {
	CI     bool
	Health bool
	Agents bool
}

// Detector turns state changes into notifications. The first state seen of
// every CI and endpoint is the baseline: starting Atalaya never notifies
// what was already broken.
type Detector struct {
	Notifier Notifier
	Rules    Rules

	ci     map[string]string // repo@branch → last settled state
	health map[string]bool   // url → down
	agents map[string]string // pid/session → state
}

// Watch observes the store until ctx is done.
func (d *Detector) Watch(ctx context.Context, st *store.Store) {
	changed := st.Subscribe()
	d.Observe(st.Snapshot())
	for {
		select {
		case <-ctx.Done():
			return
		case <-changed:
			d.Observe(st.Snapshot())
		}
	}
}

// Observe compares a state with the previous one and notifies what changed.
func (d *Detector) Observe(st store.State) {
	if d.ci == nil {
		d.ci, d.health, d.agents = map[string]string{}, map[string]bool{}, map[string]string{}
	}
	if gh, ok := store.Data[collector.GitHubSnapshot](st, "github"); ok && d.Rules.CI {
		for _, r := range gh.Repos {
			if r.CI != nil {
				d.checkCI(r.Project, r.Repo, r.DefaultBranch, r.CI)
			}
			if b := r.Branch; b != nil && b.CI != nil {
				d.checkCI(r.Project, r.Repo, b.Name, b.CI)
			}
		}
	}
	if hs, ok := store.Data[collector.HealthSnapshot](st, "health"); ok && d.Rules.Health {
		for _, c := range hs.Checks {
			d.checkHealth(c)
		}
	}
	if as, ok := store.Data[collector.AgentsSnapshot](st, "agents"); ok && d.Rules.Agents {
		for _, a := range as.Agents {
			d.checkAgent(a)
		}
	}
}

// checkAgent reports an agent that stops working: it finished its turn and
// waits for you, or a tool call stalled (likely a permission prompt).
func (d *Detector) checkAgent(a collector.Agent) {
	if a.Session == nil {
		return
	}
	key := fmt.Sprintf("%d/%s", a.PID, a.Session.ID)
	prev, known := d.agents[key]
	state := a.Session.State
	d.agents[key] = state
	if !known || prev == state || prev != collector.SessionWorking {
		return
	}
	where := a.Project
	if where == "" {
		where = filepath.Base(a.Cwd)
	}
	body := a.Session.Title
	switch state {
	case collector.SessionWaiting:
		d.send("◉ "+a.Name+" te espera en "+where, body)
	case collector.SessionBlocked:
		d.send("⏸ "+a.Name+" puede necesitar permiso en "+where, strings.TrimSpace(a.Session.Activity+" · "+body))
	}
}

// checkCI only looks at settled states: a CI that goes red → running → red
// again is one failure, and red → running → green is one recovery.
func (d *Detector) checkCI(project, repo, branch string, run *collector.CIRun) {
	if run.State != collector.CISuccess && run.State != collector.CIFailure {
		return
	}
	key := repo + "@" + branch
	prev, known := d.ci[key]
	d.ci[key] = run.State
	if !known || prev == run.State {
		return
	}
	what := branch
	if run.Workflow != "" {
		what += " · " + run.Workflow
	}
	if run.State == collector.CIFailure {
		d.send("✗ CI roto en "+project, what)
	} else {
		d.send("✓ CI arreglado en "+project, what+" vuelve a pasar")
	}
}

// checkHealth calls an endpoint down after two failed checks in a row, so a
// single timeout does not wake anyone, and up again on the first success.
func (d *Detector) checkHealth(c collector.Check) {
	h := c.History
	var down bool
	switch {
	case len(h) >= 2 && !h[len(h)-1].OK && !h[len(h)-2].OK:
		down = true
	case len(h) >= 1 && h[len(h)-1].OK:
		down = false
	default:
		return // one failure: not decided yet
	}
	prev, known := d.health[c.URL]
	d.health[c.URL] = down
	if !known || prev == down {
		return
	}
	if down {
		reason := c.Err
		if reason == "" && c.Status > 0 {
			reason = fmt.Sprint(c.Status)
		}
		d.send("✗ Producción caída en "+c.Project, c.URL+" · "+reason)
	} else {
		d.send("✓ Producción recuperada en "+c.Project, c.URL)
	}
}

// send ignores errors: a missing notifier must never break the dashboard.
func (d *Detector) send(title, body string) {
	_ = d.Notifier.Notify(title, body)
}
