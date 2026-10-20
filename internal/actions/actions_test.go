package actions

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEditorCommand(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "nvim")
	cmd, interactive := EditorCommand("", "/w/app")
	if !interactive || filepath.Base(cmd.Path) != "nvim" && cmd.Args[0] != "nvim" {
		t.Errorf("EDITOR=nvim → %v interactive=%v", cmd.Args, interactive)
	}
	cmd, interactive = EditorCommand("code -n", "/w/app")
	if interactive || strings.Join(cmd.Args, " ") != "code -n /w/app" {
		t.Errorf("configured → %v interactive=%v", cmd.Args, interactive)
	}
	t.Setenv("VISUAL", "hx")
	if _, interactive = EditorCommand("", "/w"); !interactive {
		t.Error("VISUAL must win over EDITOR")
	}
}

func TestOpenURLRejectsNonHTTP(t *testing.T) {
	for _, u := range []string{"", "file:///etc/passwd", "javascript:alert(1)", "-a Calculator"} {
		if err := OpenURL(u); err == nil {
			t.Errorf("OpenURL(%q) accepted", u)
		}
	}
}

func TestPullFastForwardOnly(t *testing.T) {
	root := t.TempDir()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=A", "GIT_AUTHOR_EMAIL=a@x", "GIT_COMMITTER_NAME=A", "GIT_COMMITTER_EMAIL=a@x")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	commit := func(dir, file string) {
		t.Helper()
		_ = os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644)
		git(dir, "add", ".")
		git(dir, "commit", "-qm", file)
	}
	remote, a, b := filepath.Join(root, "r.git"), filepath.Join(root, "a"), filepath.Join(root, "b")
	git(root, "init", "-q", "--bare", "-b", "main", remote)
	git(root, "clone", "-q", remote, a)
	git(a, "checkout", "-qb", "main")
	commit(a, "one")
	git(a, "push", "-q", "origin", "main")
	git(root, "clone", "-q", remote, b)
	commit(a, "two")
	git(a, "push", "-q", "origin", "main")

	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	if _, err := Pull(context.Background(), b); err != nil {
		t.Fatalf("fast-forward pull failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b, "two")); err != nil {
		t.Error("pull did not bring the new commit")
	}

	// Diverged history: --ff-only refuses instead of merging.
	commit(a, "three")
	git(a, "push", "-q", "origin", "main")
	commit(b, "local")
	if _, err := Pull(context.Background(), b); err == nil {
		t.Error("a diverged pull must fail, not merge")
	}
}

// fakeGH puts a gh that records its arguments (and fails on squash when
// asked) first in PATH.
func fakeGH(t *testing.T, failSquash bool) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"$@\" >> \"$(dirname \"$0\")/calls\"\n"
	if failSquash {
		script += "case \"$*\" in *--squash*) echo 'GraphQL: Squash merges are not allowed on this repository' >&2; exit 1;; esac\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return filepath.Join(dir, "calls")
}

func TestRerunFailed(t *testing.T) {
	calls := fakeGH(t, false)
	if err := RerunFailed(context.Background(), "https://github.com/o/r/actions/runs/123"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(calls); strings.TrimSpace(string(got)) != "run rerun 123 --repo o/r --failed" {
		t.Errorf("gh = %q", got)
	}
	if err := RerunFailed(context.Background(), "https://vercel.com/x"); err == nil {
		t.Error("a non-Actions URL was accepted")
	}
}

func TestMergePRFallsBackToMergeCommit(t *testing.T) {
	calls := fakeGH(t, true)
	if err := MergePR(context.Background(), "o/r", 7); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(calls)
	want := "pr merge 7 --repo o/r --squash --delete-branch\npr merge 7 --repo o/r --merge --delete-branch\n"
	if string(got) != want {
		t.Errorf("gh calls:\n%s\nwant:\n%s", got, want)
	}
}

func TestKillChecksIdentity(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	pid := int32(cmd.Process.Pid)

	err := Kill(context.Background(), pid, time.Now().Add(-time.Hour))
	if !errors.Is(err, ErrProcessChanged) {
		t.Fatalf("kill with a wrong start time: %v, want ErrProcessChanged", err)
	}
	if err := Kill(context.Background(), pid, time.Time{}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("sleep exited cleanly, want it killed by SIGTERM")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("process still alive after SIGTERM")
	}
	if err := Kill(context.Background(), 1, time.Time{}); err == nil {
		t.Error("killing PID 1 must be refused")
	}
}
