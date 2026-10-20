package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/gitparse"
)

// Commit is the last commit of a repo.
type Commit struct {
	Hash    string    `json:"hash"`
	Author  string    `json:"author"`
	Subject string    `json:"subject"`
	When    time.Time `json:"when"`
}

// FileChange is one entry of git status, for the detail view.
type FileChange struct {
	XY   string `json:"xy"`
	Path string `json:"path"`
}

// Worktree is a linked worktree of a repo (`git worktree add`), where
// agents often work in parallel.
type Worktree struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"` // empty when detached
}

// Repo is the local state of one project.
type Repo struct {
	Project      string          `json:"project"`
	Path         string          `json:"path"`
	Branch       string          `json:"branch"`
	Detached     bool            `json:"detached,omitempty"`
	Upstream     string          `json:"upstream,omitempty"`
	UpstreamGone bool            `json:"upstream_gone,omitempty"`
	Ahead        int             `json:"ahead"`
	Behind       int             `json:"behind"`
	Changes      gitparse.Counts `json:"changes"`
	Files        []FileChange    `json:"files,omitempty"`
	Stash        int             `json:"stash"`
	Worktrees    []Worktree      `json:"worktrees,omitempty"`
	LastCommit   *Commit         `json:"last_commit,omitempty"`
	Err          string          `json:"error,omitempty"`
}

// Dirty reports uncommitted work (shown in yellow).
func (r Repo) Dirty() bool { return r.Changes.Dirty() }

// OutOfSync reports a repo that differs from its upstream (shown in red).
func (r Repo) OutOfSync() bool { return r.Ahead > 0 || r.Behind > 0 || r.UpstreamGone }

// GitSnapshot is the result of the git collector.
type GitSnapshot struct {
	Repos []Repo `json:"repos"`
}

// maxFiles caps the file list kept for the detail view.
const maxFiles = 200

// gitEnv makes polling side-effect free and never interactive. Without
// GIT_OPTIONAL_LOCKS=0, `git status` may take index.lock every 5 seconds and
// collide with the user's own git commands.
var gitEnv = []string{"GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}

// Git reads the local state of every project with a path.
//
// Each git process costs about 8 ms just to start, so it runs as few as it
// can: `git status` every poll, the last commit only when HEAD moved, the
// worktree list only when git's worktrees folder changed. A repo that has
// not changed for idleAfter is read every idleEvery instead of every poll,
// unless git's own files show it moved (a commit, a checkout, git add, a
// pull, a fetch): what goes stale is at most an edit made idleEvery ago.
type Git struct {
	Projects []config.Project
	Every    time.Duration
	Now      func() time.Time // for tests

	mu    sync.Mutex
	cache map[string]*repoCache // by path
}

const (
	idleAfter = 2 * time.Minute
	idleEvery = 15 * time.Second
)

// repoCache is what the last look at a repo found.
type repoCache struct {
	repo      Repo
	meta      string // git's own files, when they were read
	readAt    time.Time
	changedAt time.Time // when its state last changed
	oid       string    // HEAD, which decides the last commit
	commit    *Commit
	wtStamp   string // git's worktrees folder, which decides the worktree list
	worktrees []Worktree
}

func (g *Git) Name() string            { return "git" }
func (g *Git) Interval() time.Duration { return g.Every }

func (g *Git) Collect(ctx context.Context) (Snapshot, error) {
	var projects []config.Project
	for _, p := range g.Projects {
		if p.Path != "" {
			projects = append(projects, p)
		}
	}
	repos := make([]Repo, len(projects))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(8)
	now := time.Now()
	if g.Now != nil {
		now = g.Now()
	}
	for i, p := range projects {
		eg.Go(func() error {
			repos[i] = g.repo(gctx, p, now)
			return nil
		})
	}
	_ = eg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return GitSnapshot{Repos: repos}, nil
}

// repo reads one repo, or returns the last look when it is idle and git's
// files did not move.
func (g *Git) repo(ctx context.Context, p config.Project, now time.Time) Repo {
	g.mu.Lock()
	c := g.cache[p.Path]
	g.mu.Unlock()
	dir := gitDir(p.Path)
	meta := stampGit(dir)
	if c != nil && meta != "" && meta == c.meta && now.Sub(c.changedAt) >= idleAfter && now.Sub(c.readAt) < idleEvery {
		return c.repo
	}
	next := &repoCache{}
	if c != nil {
		*next = *c
	}
	r := repoStatus(ctx, p, dir, next)
	if c == nil || !reflect.DeepEqual(c.repo, r) {
		next.changedAt = now
	}
	next.repo, next.meta, next.readAt = r, meta, now
	if ctx.Err() == nil {
		g.mu.Lock()
		if g.cache == nil {
			g.cache = map[string]*repoCache{}
		}
		g.cache[p.Path] = next
		g.mu.Unlock()
	}
	return r
}

// gitDir is the repo's git directory: .git, or where a .git file points
// (a linked worktree, a submodule). Empty when path is not a repo's top.
func gitDir(path string) string {
	dot := filepath.Join(path, ".git")
	info, err := os.Stat(dot)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return dot
	}
	b, err := os.ReadFile(dot)
	if err != nil {
		return ""
	}
	d, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
	if !ok {
		return ""
	}
	if !filepath.IsAbs(d) {
		d = filepath.Join(path, d)
	}
	return d
}

// commonDir is where a linked worktree shares refs with its main checkout.
func commonDir(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "commondir"))
	if err != nil {
		return dir
	}
	c := strings.TrimSpace(string(b))
	if !filepath.IsAbs(c) {
		c = filepath.Join(dir, c)
	}
	return c
}

// stampGit sums up the files git writes when a repo moves: the index (git
// add, a checkout), HEAD and its log (commits, resets), FETCH_HEAD and the
// packed refs (fetches, pulls) and the stash. Empty when unknown.
func stampGit(dir string) string {
	if dir == "" {
		return ""
	}
	var b strings.Builder
	stat := func(path string) {
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(&b, "%d.%d;", info.Size(), info.ModTime().UnixNano())
		} else {
			b.WriteString("-;")
		}
	}
	for _, f := range []string{"index", "HEAD", "logs/HEAD"} {
		stat(filepath.Join(dir, f))
	}
	common := commonDir(dir)
	for _, f := range []string{"FETCH_HEAD", "packed-refs", "logs/refs/stash", "refs/stash"} {
		stat(filepath.Join(common, f))
	}
	return b.String()
}

// stampWorktrees sums up git's worktrees folder: one entry per linked
// worktree, whose HEAD names its branch. Empty when unknown.
func stampWorktrees(dir string) string {
	if dir == "" {
		return ""
	}
	wt := filepath.Join(commonDir(dir), "worktrees")
	entries, err := os.ReadDir(wt)
	if os.IsNotExist(err) {
		return "none"
	}
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		if info, err := os.Stat(filepath.Join(wt, e.Name(), "HEAD")); err == nil {
			fmt.Fprintf(&b, "%s:%d;", e.Name(), info.ModTime().UnixNano())
		}
	}
	return b.String()
}

// repoStatus never fails as a whole: errors are per repo, so one broken
// checkout does not hide the others. It reuses from c what has not
// changed, and records there what it read.
func repoStatus(ctx context.Context, p config.Project, dir string, c *repoCache) Repo {
	r := Repo{Project: p.Name, Path: p.Path}
	out, err := run(ctx, gitEnv, "git", "-C", p.Path, "status", "--porcelain=v2", "--branch", "--show-stash", "-z")
	if err != nil {
		r.Err = err.Error()
		return r
	}
	st, err := gitparse.Parse(out)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	r.Branch, r.Detached, r.Upstream = st.Head, st.Detached, st.Upstream
	r.UpstreamGone = st.UpstreamGone()
	r.Ahead, r.Behind, r.Stash = st.Ahead, st.Behind, st.Stash
	r.Changes = st.Counts()
	for _, e := range st.Entries {
		if e.Kind == gitparse.Ignored {
			continue
		}
		if len(r.Files) == maxFiles {
			break
		}
		r.Files = append(r.Files, FileChange{XY: string([]byte{e.X, e.Y}), Path: e.Path})
	}
	if r.Detached && len(st.OID) >= 7 {
		r.Branch = st.OID[:7]
	}
	if wt := stampWorktrees(dir); wt != "" && wt == c.wtStamp {
		r.Worktrees = c.worktrees
	} else if out, err := run(ctx, gitEnv, "git", "-C", p.Path, "worktree", "list", "--porcelain", "-z"); err == nil {
		r.Worktrees = parseWorktrees(out)
		c.wtStamp, c.worktrees = wt, r.Worktrees
	}
	switch {
	case st.Initial:
	case st.OID != "" && st.OID == c.oid && c.commit != nil:
		r.LastCommit = c.commit
	default:
		commit, err := lastCommit(ctx, p.Path)
		if err != nil {
			r.Err = err.Error()
		}
		r.LastCommit = commit
		c.oid, c.commit = st.OID, commit
	}
	return r
}

// parseWorktrees reads `git worktree list --porcelain -z`: attributes end
// in NUL and an empty one ends each worktree. The first is the main
// checkout, which is the repo itself and is skipped.
func parseWorktrees(out []byte) []Worktree {
	var wts []Worktree
	var cur *Worktree
	first := true
	for _, f := range bytes.Split(out, []byte{0}) {
		field := string(f)
		switch {
		case field == "":
			if cur != nil && !first {
				wts = append(wts, *cur)
			}
			if cur != nil {
				first = false
			}
			cur = nil
		case strings.HasPrefix(field, "worktree "):
			cur = &Worktree{Path: strings.TrimPrefix(field, "worktree ")}
		case strings.HasPrefix(field, "branch ") && cur != nil:
			cur.Branch = strings.TrimPrefix(strings.TrimPrefix(field, "branch "), "refs/heads/")
		}
	}
	return wts
}

func lastCommit(ctx context.Context, dir string) (*Commit, error) {
	out, err := run(ctx, gitEnv, "git", "-C", dir, "log", "-1", "--no-show-signature", "--format=%H%x00%an%x00%at%x00%s")
	if err != nil {
		return nil, err
	}
	return parseLogLine(out)
}

func parseLogLine(out []byte) (*Commit, error) {
	f := bytes.SplitN(bytes.TrimRight(out, "\n"), []byte{0}, 4)
	if len(f) != 4 {
		return nil, errors.New("git log: unexpected output")
	}
	ts, err := strconv.ParseInt(string(f[2]), 10, 64)
	if err != nil {
		return nil, errors.New("git log: bad timestamp")
	}
	return &Commit{Hash: string(f[0]), Author: string(f[1]), Subject: string(f[3]), When: time.Unix(ts, 0)}, nil
}
