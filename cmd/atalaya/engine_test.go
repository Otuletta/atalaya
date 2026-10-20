package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/store"
)

// projectSource is a source per project, so a reload shows in the store.
type projectSource struct{ name string }

func (s projectSource) Name() string                                        { return "p-" + s.name }
func (s projectSource) Interval() time.Duration                             { return time.Hour }
func (s projectSource) Collect(context.Context) (collector.Snapshot, error) { return s.name, nil }

type nopNotifier struct{}

func (nopNotifier) Notify(string, string) error { return nil }

type reloadEvent struct {
	cfg    *config.Config
	change string
	err    error
}

func writeConfig(t *testing.T, path string, projects ...string) {
	t.Helper()
	var b strings.Builder
	for _, p := range projects {
		fmt.Fprintf(&b, "[[project]]\nname = %q\npath = %q\n\n", p, filepath.Join(filepath.Dir(path), p))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEngineReloadsTheConfigWhenSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "odomto")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	build := func(cfg *config.Config) []collector.Collector {
		var cs []collector.Collector
		for _, p := range cfg.Projects {
			cs = append(cs, projectSource{p.Name})
		}
		return cs
	}
	log, _ := events.Open("")
	st := store.New()
	reloads := make(chan reloadEvent, 10)
	e := &engine{path: path, store: st, log: log, notifier: nopNotifier{}, build: build,
		onReload: func(cfg *config.Config, change string, err error) { reloads <- reloadEvent{cfg, change, err} }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.Start(ctx, cfg, build(cfg))
	e.SlowDown(5, "p-kdx") // kept for a source that does not exist yet
	go e.Watch(ctx, 10*time.Millisecond)

	next := func() reloadEvent {
		t.Helper()
		select {
		case r := <-reloads:
			return r
		case <-time.After(3 * time.Second):
			t.Fatal("no reload")
		}
		return reloadEvent{}
	}
	has := func(name string) bool {
		_, ok := st.Snapshot().Get(name)
		return ok
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(3 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
		}
	}
	waitFor("the first source never ran", func() bool { return has("p-odomto") })

	writeConfig(t, path, "odomto", "kdx")
	if r := next(); r.err != nil || r.change != "2 proyectos (+kdx)" || len(e.Config().Projects) != 2 {
		t.Fatalf("reload = %+v", r)
	}
	waitFor("the new project's source never ran", func() bool { return has("p-kdx") })

	writeConfig(t, path, "kdx")
	if r := next(); r.change != "1 proyecto (−odomto)" {
		t.Fatalf("reload = %+v", r)
	}
	waitFor("a removed source stayed in the store", func() bool { return !has("p-odomto") })

	if err := os.WriteFile(path, []byte("[[project]\nname = "), 0o644); err != nil {
		t.Fatal(err)
	}
	if r := next(); r.err == nil {
		t.Fatalf("a broken config reloaded: %+v", r)
	}
	if got := e.Config().Projects; len(got) != 1 || got[0].Name != "kdx" {
		t.Errorf("after a broken save the config is %+v, want the previous one", got)
	}
	e.mu.Lock()
	slow := e.slow["p-kdx"]
	e.mu.Unlock()
	if slow != 5 {
		t.Errorf("slowdown lost across reloads: %d", slow)
	}
}

func TestDescribeChange(t *testing.T) {
	old := &config.Config{Projects: []config.Project{{Name: "a"}, {Name: "b"}}}
	cfg := &config.Config{Projects: []config.Project{{Name: "b"}, {Name: "c"}},
		Plugins: []config.Plugin{{Name: "npm-audit"}}}
	if got := describeChange(old, cfg); got != "2 proyectos (+c, −a) · 1 plugin (+npm-audit)" {
		t.Errorf("describeChange = %q", got)
	}
	if got := describeChange(cfg, cfg); got != "2 proyectos · 1 plugin" {
		t.Errorf("no change = %q", got)
	}
}
