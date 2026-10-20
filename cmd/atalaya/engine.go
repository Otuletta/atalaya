package main

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/notify"
	"github.com/otuletta/atalaya/internal/scheduler"
	"github.com/otuletta/atalaya/internal/store"
	"github.com/otuletta/atalaya/internal/summary"
)

// engine runs everything that depends on the config — the sources, the
// detector that fills the activity log and notifies, and status.json — and
// starts it again over the same store when the config file changes, so
// editing the config never needs a restart.
type engine struct {
	path        string // empty in the demo: nothing to watch
	store       *store.Store
	log         *events.Log
	notifier    notify.Notifier
	build       func(*config.Config) []collector.Collector
	summaryPath string // empty: no status.json (the demo)
	// onReload hears about every reload, or about the error that kept the
	// previous config. Set it before Watch.
	onReload func(cfg *config.Config, change string, err error)

	mu    sync.Mutex
	cfg   *config.Config
	sched *scheduler.Scheduler
	stop  context.CancelFunc
	done  chan struct{}
	stamp fileStamp
	slow  map[string]int // slowdowns asked for, kept across reloads
}

type fileStamp struct {
	mod  time.Time
	size int64
	ok   bool
}

func stampOf(path string) fileStamp {
	info, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{info.ModTime(), info.Size(), true}
}

func (a fileStamp) same(b fileStamp) bool {
	return a.ok == b.ok && a.size == b.size && a.mod.Equal(b.mod)
}

// Start runs cfg's sources (already built, so `status` and the dashboard
// share them) until ctx is done or a reload replaces them.
func (e *engine) Start(ctx context.Context, cfg *config.Config, sources []collector.Collector) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.path != "" {
		e.stamp = stampOf(e.path)
	}
	e.start(ctx, cfg, sources)
}

// start runs one generation. Call it with e.mu held.
func (e *engine) start(ctx context.Context, cfg *config.Config, sources []collector.Collector) {
	ctx, cancel := context.WithCancel(ctx)
	sched := scheduler.New(e.store, sources...)
	for name, f := range e.slow {
		sched.SlowDown(f, name)
	}
	// The detector always runs: it fills the activity log; the rules only
	// decide which events also become notifications.
	d := &notify.Detector{
		Notifier: e.notifier,
		Rules: notify.Rules{CI: cfg.Notify.WantCI(), Health: cfg.Notify.WantHealth(),
			Agents: cfg.Notify.WantAgents(), Reviews: cfg.Notify.WantReviews()},
		Log:   e.log,
		Store: e.store,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup
		wg.Go(func() { sched.Run(ctx) })
		wg.Go(func() { d.Watch(ctx, e.store) })
		if e.summaryPath != "" {
			wg.Go(func() { summary.Keep(ctx, e.store, cfg, e.summaryPath, 2*time.Second) })
		}
		wg.Wait()
	}()
	e.cfg, e.sched, e.stop, e.done = cfg, sched, cancel, done
}

// Config is the config in use.
func (e *engine) Config() *config.Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

// Refresh wakes the named sources (all when none is given).
func (e *engine) Refresh(names ...string) {
	e.mu.Lock()
	sched := e.sched
	e.mu.Unlock()
	sched.Refresh(names...)
}

// SlowDown multiplies the interval of the named sources, now and after a
// reload.
func (e *engine) SlowDown(factor int, names ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.slow == nil {
		e.slow = map[string]int{}
	}
	for _, n := range names {
		e.slow[n] = factor
	}
	e.sched.SlowDown(factor, names...)
}

// Watch reloads the config every time its file changes, checking every
// interval, until ctx is done.
func (e *engine) Watch(ctx context.Context, every time.Duration) {
	if e.path == "" {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s := stampOf(e.path)
		e.mu.Lock()
		changed := !s.same(e.stamp)
		e.stamp = s
		e.mu.Unlock()
		if changed {
			e.reload(ctx)
		}
	}
}

// reload starts the sources again with the config on disk. One that does
// not load keeps the previous config running; one that did not really
// change (the file was only touched) changes nothing.
func (e *engine) reload(ctx context.Context) {
	cfg, err := config.Load(e.path)
	if err != nil {
		e.tell(nil, "", err)
		return
	}
	e.mu.Lock()
	old, stop, done, oldNames := e.cfg, e.stop, e.done, e.sched.Names()
	e.mu.Unlock()
	if reflect.DeepEqual(old, cfg) {
		return
	}
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second): // a source that ignores ctx
	}
	e.mu.Lock()
	e.start(ctx, cfg, e.build(cfg))
	newNames := e.sched.Names()
	e.mu.Unlock()
	var gone []string
	for _, n := range oldNames {
		if !slices.Contains(newNames, n) {
			gone = append(gone, n)
		}
	}
	e.store.Remove(gone...) // a plugin that is no longer configured
	e.tell(cfg, describeChange(old, cfg), nil)
}

func (e *engine) tell(cfg *config.Config, change string, err error) {
	if e.onReload != nil {
		e.onReload(cfg, change, err)
	}
}

// describeChange says what a reload changed: "13 proyectos (+atalaya)".
func describeChange(old, cfg *config.Config) string {
	names := func(c *config.Config) (projects, plugins []string) {
		for _, p := range c.Projects {
			projects = append(projects, p.Name)
		}
		for _, p := range c.Plugins {
			plugins = append(plugins, p.Name)
		}
		return projects, plugins
	}
	oldProjects, oldPlugins := names(old)
	projects, plugins := names(cfg)
	parts := []string{count(len(projects), "proyecto", "proyectos") + delta(oldProjects, projects)}
	if d := delta(oldPlugins, plugins); d != "" || len(plugins) > 0 {
		parts = append(parts, count(len(plugins), "plugin", "plugins")+d)
	}
	return strings.Join(parts, " · ")
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// delta is " (+a, −b)" for the names added and removed, or "".
func delta(before, after []string) string {
	var d []string
	for _, n := range after {
		if !slices.Contains(before, n) {
			d = append(d, "+"+n)
		}
	}
	for _, n := range before {
		if !slices.Contains(after, n) {
			d = append(d, "−"+n)
		}
	}
	if len(d) == 0 {
		return ""
	}
	if len(d) > 3 {
		d = append(d[:3], "…")
	}
	return " (" + strings.Join(d, ", ") + ")"
}
