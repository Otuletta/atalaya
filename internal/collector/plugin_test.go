package collector

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/config"
)

// script writes an executable plugin and returns its config.
func script(t *testing.T, body string) config.Plugin {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plugin.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return config.Plugin{Name: "prueba", Command: []string{path}, Interval: config.Duration{Duration: time.Minute}}
}

func TestPluginReadsItsTable(t *testing.T) {
	// The plugin echoes the first project it receives, to check the input.
	p := &Plugin{Plugin: script(t, `name=$(printf '%s' "$ATALAYA_PROJECTS" | sed 's/.*"name":"\([^"]*\)".*/\1/')
cat <<JSON
{"title": "Deploys", "summary": "2 deploys", "columns": ["APP", "ESTADO"],
 "rows": [{"cells": ["$name", "Ready"], "status": "ok", "url": "https://x"},
          {"cells": ["b", "Error", "extra"], "status": "rarísimo"}]}
JSON`), Projects: []config.Project{{Name: "odomto", Path: "/w/odomto"}}}
	if p.Name() != "plugin:prueba" {
		t.Errorf("Name = %q", p.Name())
	}
	snap, err := p.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s := snap.(PluginSnapshot)
	if s.Title != "Deploys" || s.Summary != "2 deploys" || len(s.Rows) != 2 {
		t.Fatalf("snapshot = %+v", s)
	}
	if s.Rows[0].Cells[0] != "odomto" {
		t.Errorf("ATALAYA_PROJECTS not passed: first cell = %q", s.Rows[0].Cells[0])
	}
	if s.Rows[1].Status != PluginInfo {
		t.Errorf("unknown status kept: %q", s.Rows[1].Status)
	}
	if len(s.Columns) != 3 {
		t.Errorf("columns not padded to the widest row: %q", s.Columns)
	}
}

func TestPluginFailures(t *testing.T) {
	_, err := (&Plugin{Plugin: script(t, `echo "token caducado" >&2; exit 2`)}).Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "token caducado") {
		t.Errorf("failing command: %v", err)
	}
	_, err = (&Plugin{Plugin: script(t, `echo "esto no es JSON"`)}).Collect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Errorf("bad output: %v", err)
	}
	snap, err := (&Plugin{Plugin: script(t, `echo '{}'`)}).Collect(context.Background())
	if err != nil || snap.(PluginSnapshot).Title != "prueba" || snap.(PluginSnapshot).Rows == nil {
		t.Errorf("empty object = %+v, %v", snap, err)
	}
}

func TestPluginLimitsOutput(t *testing.T) {
	var rows []string
	for range maxPluginRows + 50 {
		rows = append(rows, `{"cells":["x"]}`)
	}
	body := "cat <<'JSON'\n{\"rows\": [" + strings.Join(rows, ",") + "]}\nJSON"
	snap, err := (&Plugin{Plugin: script(t, body)}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n := len(snap.(PluginSnapshot).Rows); n != maxPluginRows {
		t.Errorf("rows = %d, want capped at %d", n, maxPluginRows)
	}
}

// The examples are part of the product: they must keep producing valid
// tables.
func TestExamplePlugins(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "plugins")
	run := func(cmd ...string) PluginSnapshot {
		t.Helper()
		p := &Plugin{Plugin: config.Plugin{Name: "ej", Command: cmd}, Projects: []config.Project{{Name: "vacío", Path: t.TempDir()}}}
		snap, err := p.Collect(context.Background())
		if err != nil {
			t.Fatalf("%v: %v", cmd, err)
		}
		b, _ := json.Marshal(snap)
		t.Logf("%s", b)
		return snap.(PluginSnapshot)
	}
	if s := run(filepath.Join(dir, "disk.sh")); len(s.Rows) != 1 || !strings.HasSuffix(s.Rows[0].Cells[1], "%") {
		t.Errorf("disk.sh = %+v", s)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed")
	}
	// No project has a package-lock.json: an empty, valid table.
	if s := run("python3", filepath.Join(dir, "npm-audit.py")); len(s.Rows) != 0 || s.Summary == "" {
		t.Errorf("npm-audit.py = %+v", s)
	}
	if testing.Short() {
		return // brew outdated takes a while
	}
	if s := run("python3", filepath.Join(dir, "brew-outdated.py")); s.Title != "Homebrew" || s.Summary == "" {
		t.Errorf("brew-outdated.py = %+v", s)
	}
}
