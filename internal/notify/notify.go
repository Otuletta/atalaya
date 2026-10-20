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
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/events"
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

// Rules says which kinds of events also become desktop notifications.
// Every event is recorded in the activity log either way.
type Rules struct {
	CI      bool
	Health  bool
	Agents  bool
	Reviews bool
}

// Detector turns state changes into events: it records them in the log,
// publishes the latest under "events" for the UI and notifies the ones the
// rules ask for. The first state seen of everything is the baseline:
// starting Atalaya never reports what was already the case.
type Detector struct {
	Notifier Notifier
	Rules    Rules
	// Log records every event. Optional.
	Log *events.Log
	// Store receives the latest events under "events". Optional.
	Store *store.Store
	Now   func() time.Time

	ci      map[string]string         // repo@branch → last settled state
	health  map[string]bool           // url → down
	agents  map[string]string         // pid/session → state
	procs   map[int32]collector.Agent // running agents
	reviews map[string]bool           // review requests, by PR URL
	prs     map[string]map[int]string // repo → open PR number → title
	seen    map[string]bool           // sources with a baseline
	changed bool
}

// Watch observes the store until ctx is done.
func (d *Detector) Watch(ctx context.Context, st *store.Store) {
	changed := st.Subscribe()
	defer st.Unsubscribe(changed)
	d.publish()
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

func (d *Detector) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Observe compares a state with the previous one and reports what changed.
func (d *Detector) Observe(st store.State) {
	if d.ci == nil {
		d.ci, d.health, d.agents = map[string]string{}, map[string]bool{}, map[string]string{}
		d.procs, d.reviews, d.prs, d.seen = map[int32]collector.Agent{}, map[string]bool{}, map[string]map[int]string{}, map[string]bool{}
	}
	if gh, ok := store.Data[collector.GitHubSnapshot](st, "github"); ok {
		baseline := !d.seen["github"]
		d.seen["github"] = true
		for _, r := range gh.Repos {
			if r.CI != nil {
				d.checkCI(r.Project, r.Repo, r.DefaultBranch, r.CI)
			}
			if b := r.Branch; b != nil && b.CI != nil {
				d.checkCI(r.Project, r.Repo, b.Name, b.CI)
			}
			d.checkPRs(r, baseline)
		}
		d.checkReviews(gh, baseline)
	}
	if hs, ok := store.Data[collector.HealthSnapshot](st, "health"); ok {
		for _, c := range hs.Checks {
			d.checkHealth(c)
		}
	}
	if as, ok := store.Data[collector.AgentsSnapshot](st, "agents"); ok {
		baseline := !d.seen["agents"]
		d.seen["agents"] = true
		d.checkProcesses(as, baseline)
		for _, a := range as.Agents {
			d.checkAgent(a)
		}
	}
	if d.changed {
		d.changed = false
		d.publish()
	}
}

// emit records an event and, when notify is set, shows it on the desktop.
func (d *Detector) emit(e events.Event, notify bool) {
	e.At = d.now()
	if d.Log != nil {
		_ = d.Log.Append(e)
	}
	if notify {
		_ = d.Notifier.Notify(icon(e)+" "+e.Title, e.Body)
	}
	d.changed = true
}

// publish puts the latest events in the store for the Activity panel.
func (d *Detector) publish() {
	if d.Store != nil && d.Log != nil {
		d.Store.Put("events", events.Snapshot{Events: d.Log.Recent(200)}, nil, time.Time{})
	}
}

func icon(e events.Event) string {
	switch {
	case e.Kind == "agent_blocked":
		return "⏸"
	case e.Severity == events.Bad:
		return "✗"
	case e.Severity == events.Good:
		return "✓"
	case e.Severity == events.Ask:
		return "◉"
	}
	return "·"
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
		d.emit(events.Event{Kind: "ci_broken", Severity: events.Bad, Project: project,
			Title: "CI roto en " + project, Body: what, URL: run.URL}, d.Rules.CI)
	} else {
		d.emit(events.Event{Kind: "ci_fixed", Severity: events.Good, Project: project,
			Title: "CI arreglado en " + project, Body: what + " vuelve a pasar", URL: run.URL}, d.Rules.CI)
	}
}

// checkPRs logs PRs that appear or disappear. Only repos whose open PRs all
// fit in the query are compared: otherwise a PR could just fall out of the
// first page.
func (d *Detector) checkPRs(r collector.GitHubRepo, baseline bool) {
	if r.Err != "" || r.OpenPRs > len(r.PRs) {
		return
	}
	now := map[int]string{}
	for _, pr := range r.PRs {
		now[pr.Number] = pr.Title
	}
	prev, known := d.prs[r.Repo]
	d.prs[r.Repo] = now
	if baseline || !known {
		return
	}
	for _, pr := range r.PRs {
		if _, was := prev[pr.Number]; !was {
			d.emit(events.Event{Kind: "pr_opened", Severity: events.Info, Project: r.Project,
				Title: fmt.Sprintf("Nuevo PR #%d en %s", pr.Number, r.Project), Body: pr.Title + " · " + pr.Author, URL: pr.URL}, false)
		}
	}
	for n, title := range prev {
		if _, still := now[n]; !still {
			d.emit(events.Event{Kind: "pr_closed", Severity: events.Info, Project: r.Project,
				Title: fmt.Sprintf("PR #%d cerrado o mergeado en %s", n, r.Project), Body: title,
				URL: fmt.Sprintf("%s/pull/%d", r.URL, n)}, false)
		}
	}
}

// checkReviews reports review requests that were not there before.
func (d *Detector) checkReviews(gh collector.GitHubSnapshot, baseline bool) {
	all := append([]collector.PullRequest(nil), gh.OtherReviewRequests...)
	project := map[string]string{}
	for _, r := range gh.Repos {
		all = append(all, r.ReviewRequests...)
		for _, pr := range r.ReviewRequests {
			project[pr.URL] = r.Project
		}
	}
	now := map[string]bool{}
	for _, pr := range all {
		now[pr.URL] = true
		if !baseline && !d.reviews[pr.URL] {
			d.emit(events.Event{Kind: "review_requested", Severity: events.Ask, Project: project[pr.URL],
				Title: "Te piden review en " + pr.Repo, Body: fmt.Sprintf("#%d %s · %s", pr.Number, pr.Title, pr.Author), URL: pr.URL},
				d.Rules.Reviews)
		}
	}
	d.reviews = now
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
		d.emit(events.Event{Kind: "prod_down", Severity: events.Bad, Project: c.Project,
			Title: "Producción caída en " + c.Project, Body: c.URL + " · " + reason, URL: c.URL}, d.Rules.Health)
	} else {
		d.emit(events.Event{Kind: "prod_up", Severity: events.Good, Project: c.Project,
			Title: "Producción recuperada en " + c.Project, Body: c.URL, URL: c.URL}, d.Rules.Health)
	}
}

func where(a collector.Agent) string {
	if a.Project != "" {
		return a.Project
	}
	return filepath.Base(a.Cwd)
}

// checkProcesses logs agents that start or end.
func (d *Detector) checkProcesses(as collector.AgentsSnapshot, baseline bool) {
	now := map[int32]collector.Agent{}
	for _, a := range as.Agents {
		now[a.PID] = a
		if _, was := d.procs[a.PID]; !was && !baseline {
			d.emit(events.Event{Kind: "agent_started", Severity: events.Info, Project: a.Project,
				Title: a.Name + " empezó en " + where(a)}, false)
		}
	}
	for pid, a := range d.procs {
		if _, still := now[pid]; !still {
			body := ""
			if a.Session != nil {
				body = a.Session.Title
			}
			d.emit(events.Event{Kind: "agent_ended", Severity: events.Info, Project: a.Project,
				Title: a.Name + " terminó en " + where(a), Body: body}, false)
		}
	}
	d.procs = now
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
	switch state {
	case collector.SessionWaiting:
		d.emit(events.Event{Kind: "agent_waiting", Severity: events.Ask, Project: a.Project,
			Title: a.Name + " te espera en " + where(a), Body: a.Session.Title, URL: a.Session.PRURL}, d.Rules.Agents)
	case collector.SessionBlocked:
		d.emit(events.Event{Kind: "agent_blocked", Severity: events.Ask, Project: a.Project,
			Title: a.Name + " puede necesitar permiso en " + where(a),
			Body:  strings.TrimSpace(a.Session.Activity + " · " + a.Session.Title)}, d.Rules.Agents)
	}
}
