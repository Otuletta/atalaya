package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/summary"
)

func runCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(args, &out, &errOut)
	return out.String() + errOut.String(), err
}

func TestVersionAndUnknown(t *testing.T) {
	if out, err := runCmd(t, "version"); err != nil || !strings.Contains(out, "atalaya dev") {
		t.Errorf("version = %q, %v", out, err)
	}
	if _, err := runCmd(t, "nope"); err == nil {
		t.Error("an unknown subcommand did not fail")
	}
}

func TestMissingConfigExplainsInit(t *testing.T) {
	_, err := runCmd(t, "--config", filepath.Join(t.TempDir(), "x.toml"), "status")
	if err == nil || !strings.Contains(err.Error(), "atalaya init") {
		t.Errorf("err = %v", err)
	}
}

func TestStatusJSONDemo(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	out, err := runCmd(t, "--demo", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		State struct {
			Sources map[string]json.RawMessage `json:"sources"`
		} `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	for _, src := range []string{"git", "github", "health", "ports", "docker", "ollama", "agents", "events"} {
		if _, ok := doc.State.Sources[src]; !ok {
			t.Errorf("source %q missing from the JSON", src)
		}
	}
}

func TestLog(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	if out, err := runCmd(t, "log"); err != nil || !strings.Contains(out, "Todavía no hay eventos") {
		t.Errorf("empty log = %q, %v", out, err)
	}
	log, err := events.Open(filepath.Join(state, "atalaya", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 9, 30, 0, 0, time.Local)
	_ = log.Append(events.Event{At: at, Severity: events.Bad, Title: "CI roto en odomto", Body: "main · CI"})
	_ = log.Append(events.Event{At: at.Add(time.Hour), Severity: events.Good, Title: "CI arreglado en odomto"})
	out, err := runCmd(t, "log", "-n", "5")
	if err != nil {
		t.Fatal(err)
	}
	want := "2026-10-20 09:30  ✗  CI roto en odomto  — main · CI\n2026-10-20 10:30  ✓  CI arreglado en odomto\n"
	if out != want {
		t.Errorf("log =\n%s\nwant\n%s", out, want)
	}
}

func TestInit(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.CommandContext(context.Background(), "git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	cfg := filepath.Join(t.TempDir(), "config.toml")
	out, err := runCmd(t, "--config", cfg, "init", root)
	if err != nil || !strings.Contains(out, "Creado") || !strings.Contains(out, "app") {
		t.Fatalf("init = %q, %v", out, err)
	}
	if out, _ := runCmd(t, "--config", cfg, "init", root); !strings.Contains(out, "No hay repos nuevos") {
		t.Errorf("second init = %q", out)
	}
}

func TestPrompt(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	// Atalaya closed: nothing, so the shell prompt stays clean.
	if out, err := runCmd(t, "prompt"); err != nil || out != "" {
		t.Errorf("no status = %q, %v", out, err)
	}
	if out, _ := runCmd(t, "prompt", "--format", "swiftbar"); !strings.HasPrefix(out, "◆ ?\n") {
		t.Errorf("swiftbar without status = %q", out)
	}

	project := t.TempDir()
	s := summary.Summary{UpdatedAt: time.Now(), CIBroken: 1, Waiting: 2,
		Projects: []summary.Project{{Name: "odomto", Path: project, CI: "failure", Behind: 2}}}
	if err := summary.Write(filepath.Join(state, "atalaya", "status.json"), s); err != nil {
		t.Fatal(err)
	}
	if out, _ := runCmd(t, "prompt"); out != "✗1 ◉2\n" {
		t.Errorf("global prompt = %q", out)
	}
	t.Chdir(project)
	if out, _ := runCmd(t, "prompt"); out != "✗ CI ↓2\n" {
		t.Errorf("project prompt = %q", out)
	}
	if out, _ := runCmd(t, "prompt", "--format", "json"); !strings.Contains(out, `"ci_broken": 1`) {
		t.Errorf("json = %q", out)
	}
	// Old file: Atalaya is not running any more.
	s.UpdatedAt = time.Now().Add(-time.Hour)
	_ = summary.Write(filepath.Join(state, "atalaya", "status.json"), s)
	if out, _ := runCmd(t, "prompt"); out != "" {
		t.Errorf("stale prompt = %q", out)
	}
}

func TestDoctor(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	out, err := runCmd(t, "--config", cfgPath, "doctor")
	if err == nil || !strings.Contains(out, "✗ config") || !strings.Contains(out, "atalaya init") {
		t.Errorf("without config: %q, %v", out, err)
	}
	repo := t.TempDir()
	if o, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, o)
	}
	cfg := "[[project]]\nname = \"app\"\npath = \"" + repo + "\"\n\n[[project]]\nname = \"nogit\"\npath = \"" + t.TempDir() + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = runCmd(t, "--config", cfgPath, "doctor")
	if err != nil {
		t.Fatalf("doctor failed with a valid config: %v\n%s", err, out)
	}
	for _, want := range []string{"✓ config", "2 proyectos", "✓ git", "! proyecto nogit", "✓ estado"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
}

func TestGitTooOld(t *testing.T) {
	for v, want := range map[string]bool{
		"git version 2.54.0 (Apple Git-157)": false, "git version 2.35.0": false,
		"git version 2.34.1": true, "git version 1.9": true, "rare": false,
	} {
		if got := gitTooOld(v); got != want {
			t.Errorf("gitTooOld(%q) = %v", v, got)
		}
	}
}
