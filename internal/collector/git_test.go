package collector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/config"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Ada", "GIT_AUTHOR_EMAIL=ada@example.com",
		"GIT_COMMITTER_NAME=Ada", "GIT_COMMITTER_EMAIL=ada@example.com",
		"GIT_AUTHOR_DATE=2026-01-01T12:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T12:00:00Z",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newClone returns a clone that is one commit behind and one ahead of its
// upstream, with a dirty worktree and a stash.
func newClone(t *testing.T) string {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	clone := filepath.Join(root, "clone")
	git(t, root, "init", "-q", "--bare", "-b", "main", remote)
	git(t, root, "clone", "-q", remote, seed)
	git(t, seed, "checkout", "-q", "-b", "main")
	write(t, filepath.Join(seed, "a.txt"), "a\n")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-qm", "inicial")
	git(t, seed, "push", "-q", "origin", "main")
	git(t, root, "clone", "-q", remote, clone)

	write(t, filepath.Join(seed, "b.txt"), "b\n")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-qm", "remoto")
	git(t, seed, "push", "-q", "origin", "main")

	write(t, filepath.Join(clone, "c.txt"), "c\n")
	git(t, clone, "add", ".")
	git(t, clone, "commit", "-qm", "local: añade c")
	write(t, filepath.Join(clone, "a.txt"), "stash me\n")
	git(t, clone, "stash", "-q")
	write(t, filepath.Join(clone, "a.txt"), "changed\n")
	write(t, filepath.Join(clone, "new.txt"), "new\n")
	return clone
}

func TestGitAndFetch(t *testing.T) {
	clone := newClone(t)
	projects := []config.Project{
		{Name: "app", Path: clone},
		{Name: "missing", Path: filepath.Join(t.TempDir(), "nope")},
		{Name: "remote-only", GitHub: "a/b"},
	}
	ctx := context.Background()

	snap, err := (&Git{Projects: projects}).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	repos := snap.(GitSnapshot).Repos
	if len(repos) != 2 {
		t.Fatalf("repos = %+v, want app and missing (no path → skipped)", repos)
	}
	app := repos[0]
	if app.Branch != "main" || app.Upstream != "origin/main" || app.Err != "" {
		t.Errorf("app = %+v", app)
	}
	// The remote commit is not known until a fetch.
	if app.Ahead != 1 || app.Behind != 0 {
		t.Errorf("before fetch ahead/behind = %d/%d, want 1/0", app.Ahead, app.Behind)
	}
	if app.Changes.Unstaged != 1 || app.Changes.Untracked != 1 || app.Stash != 1 {
		t.Errorf("changes = %+v stash = %d", app.Changes, app.Stash)
	}
	if !app.Dirty() || !app.OutOfSync() {
		t.Error("app should be dirty and out of sync")
	}
	if c := app.LastCommit; c == nil || c.Subject != "local: añade c" || c.Author != "Ada" ||
		!c.When.Equal(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("last commit = %+v", app.LastCommit)
	}
	if repos[1].Err == "" {
		t.Error("a missing checkout must be reported, not hidden")
	}

	fsnap, err := (&Fetch{Projects: projects[:1]}).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r := fsnap.(FetchSnapshot).Repos[0]; r.Err != "" || r.At.IsZero() {
		t.Errorf("fetch = %+v", r)
	}
	snap, _ = (&Git{Projects: projects[:1]}).Collect(ctx)
	if app := snap.(GitSnapshot).Repos[0]; app.Ahead != 1 || app.Behind != 1 {
		t.Errorf("after fetch ahead/behind = %d/%d, want 1/1", app.Ahead, app.Behind)
	}
}

func TestWorktrees(t *testing.T) {
	clone := newClone(t)
	wt := filepath.Join(filepath.Dir(clone), "clone-feature")
	git(t, clone, "worktree", "add", "-q", "-b", "feature", wt)
	git(t, clone, "worktree", "add", "-q", "--detach", wt+"-detached")
	snap, err := (&Git{Projects: []config.Project{{Name: "app", Path: clone}}}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r := snap.(GitSnapshot).Repos[0]
	if len(r.Worktrees) != 2 || r.Worktrees[0].Branch != "feature" || r.Worktrees[1].Branch != "" ||
		!strings.HasSuffix(r.Worktrees[0].Path, "clone-feature") {
		t.Errorf("worktrees = %+v", r.Worktrees)
	}
	branch, worktree := gitPlace(context.Background(), wt)
	if branch != "feature" || worktree != "clone-feature" {
		t.Errorf("gitPlace(worktree) = %q, %q", branch, worktree)
	}
	if branch, worktree := gitPlace(context.Background(), clone); branch != "main" || worktree != "" {
		t.Errorf("gitPlace(main checkout) = %q, %q", branch, worktree)
	}
}

func TestGitDoesNotTakeIndexLock(t *testing.T) {
	clone := newClone(t)
	// Simulate the user running a git command that holds the lock.
	lock := filepath.Join(clone, ".git", "index.lock")
	write(t, lock, "")
	snap, err := (&Git{Projects: []config.Project{{Name: "app", Path: clone}}}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r := snap.(GitSnapshot).Repos[0]; r.Err != "" {
		t.Errorf("status failed while another git held index.lock: %s", r.Err)
	}
}

func TestParseLogLine(t *testing.T) {
	c, err := parseLogLine([]byte("abc123\x00Ada Lovelace\x001767268800\x00fix: algo con\x00nul\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash != "abc123" || c.Author != "Ada Lovelace" || c.Subject != "fix: algo con\x00nul" || c.When.Unix() != 1767268800 {
		t.Errorf("commit = %+v", c)
	}
	for _, bad := range []string{"", "a\x00b", "a\x00b\x00notatime\x00s"} {
		if _, err := parseLogLine([]byte(bad)); err == nil {
			t.Errorf("parseLogLine(%q) did not fail", bad)
		}
	}
}
