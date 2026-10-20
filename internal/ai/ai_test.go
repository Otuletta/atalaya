package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRunURL(t *testing.T) {
	r, ok := ParseRunURL("https://github.com/Otuletta/ENDONSYS/actions/runs/36158363937/job/1")
	if !ok || r.Repo != "Otuletta/ENDONSYS" || r.ID != "36158363937" {
		t.Errorf("got %+v, %v", r, ok)
	}
	for _, bad := range []string{"", "https://github.com/a/b/pull/3", "https://vercel.com/a/actions/runs/1"} {
		if _, ok := ParseRunURL(bad); ok {
			t.Errorf("ParseRunURL(%q) accepted", bad)
		}
	}
}

func TestCondense(t *testing.T) {
	var lines []string
	for i := range 300 {
		lines = append(lines, fmt.Sprintf("check\tRun tests\t2026-09-25T16:04:58.58Z línea %d", i))
	}
	lines[150] = "check\tRun tests\t2026-09-25T16:04:58.58Z ##[error]Process completed with exit code 1."
	got := condense(strings.Join(lines, "\n"))
	if !strings.HasPrefix(got, "check | línea 70\n") || !strings.HasSuffix(got, "check | ##[error]Process completed with exit code 1.") {
		t.Errorf("condensed = %q … %q", got[:30], got[len(got)-60:])
	}
	if strings.Contains(got, "2026-09-25") || strings.Contains(got, "línea 151") {
		t.Error("timestamps or the cleanup after the error were kept")
	}
	// Without an error marker, the end of the log.
	if got := condense("a\tb\t2026-01-01T00:00:00Z uno\na\tb\t2026-01-01T00:00:00Z dos"); got != "a | uno\na | dos" {
		t.Errorf("no marker = %q", got)
	}
}

func TestTail(t *testing.T) {
	var lines []string
	for range 250 {
		lines = append(lines, "x")
	}
	got := tail(strings.Join(lines, "\n")+"\n", 200)
	if n := strings.Count(got, "\n") + 1; n != 201 || !strings.HasPrefix(got, "[… 50 líneas anteriores omitidas]") {
		t.Errorf("tail kept %d lines: %q…", n, got[:40])
	}
}

// fakeBin puts an executable script named name first in PATH.
func fakeBin(t *testing.T, name, script string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func TestFailedLog(t *testing.T) {
	dir := fakeBin(t, "gh", `echo "$@" > "$(dirname "$0")/args"; echo "test	Run go test	--- FAIL: TestParse"`)
	log, err := FailedLog(context.Background(), Run{Repo: "o/r", ID: "42"})
	if err != nil || !strings.Contains(log, "--- FAIL: TestParse") {
		t.Fatalf("log = %q, %v", log, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if strings.TrimSpace(string(args)) != "run view 42 --repo o/r --log-failed" {
		t.Errorf("gh args = %q", args)
	}

	fakeBin(t, "gh", `echo "HTTP 404: Not Found" >&2; exit 1`)
	if _, err := FailedLog(context.Background(), Run{Repo: "o/r", ID: "1"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v", err)
	}
}

func TestDiagnose(t *testing.T) {
	dir := fakeBin(t, "claude", `pwd -P > "$(dirname "$0")/cwd"; printf '%s\n' "$@" > "$(dirname "$0")/args"; cat > "$(dirname "$0")/stdin"; echo "  Falta un go mod tidy.  "`)
	repo := t.TempDir()
	f := Failure{Project: "odomto", Dir: repo, Branch: "main", Workflow: "CI", RunURL: "https://github.com/o/r/actions/runs/1"}
	got, err := (Assistant{}).Diagnose(context.Background(), f, "LOG DEL FALLO")
	if err != nil || got != "Falta un go mod tidy." {
		t.Fatalf("got %q, %v", got, err)
	}
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	cwd, _ := os.ReadFile(filepath.Join(dir, "cwd"))
	realRepo, _ := filepath.EvalSymlinks(repo)
	if string(stdin) != "LOG DEL FALLO" {
		t.Errorf("stdin = %q", stdin)
	}
	if !strings.Contains(string(args), "-p\n") || !strings.Contains(string(args), "--allowedTools\nRead,Grep,Glob") ||
		!strings.Contains(string(args), "odomto") {
		t.Errorf("args = %q", args)
	}
	if strings.TrimSpace(string(cwd)) != realRepo {
		t.Errorf("ran in %q, want the repo %q", cwd, realRepo)
	}
}

func TestDiagnoseErrors(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if _, err := (Assistant{}).Diagnose(context.Background(), Failure{}, ""); !errors.Is(err, ErrNoCLI) {
		t.Errorf("without the CLI: %v", err)
	}
	fakeBin(t, "claude", `echo "not logged in" >&2; exit 1`)
	if _, err := (Assistant{}).Diagnose(context.Background(), Failure{Dir: t.TempDir()}, ""); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("err = %v", err)
	}
}

func TestFixCommand(t *testing.T) {
	fakeBin(t, "mi-claude", "true")
	repo := t.TempDir()
	a := Assistant{Command: []string{"mi-claude", "--model", "opus"}}
	cmd, err := a.FixCommand(Failure{Project: "kdx", Dir: repo, Branch: "feat/x", Workflow: "CI"}, "el log")
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Dir != repo || len(cmd.Args) != 4 || cmd.Args[1] != "--model" || !strings.Contains(cmd.Args[3], "kdx") {
		t.Fatalf("cmd = %q in %q", cmd.Args, cmd.Dir)
	}
	// The prompt points at a file with the log.
	i := strings.Index(cmd.Args[3], os.TempDir())
	if i < 0 {
		t.Fatalf("prompt does not name the log file: %q", cmd.Args[3])
	}
	path := strings.Fields(cmd.Args[3][i:])[0]
	path = strings.TrimRight(path, ".;")
	defer os.Remove(path)
	if b, err := os.ReadFile(path); err != nil || string(b) != "el log" {
		t.Errorf("log file %q = %q, %v", path, b, err)
	}
}
