package collector

import (
	"bytes"
	"context"
	"errors"
	"strconv"
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
type Git struct {
	Projects []config.Project
	Every    time.Duration
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
	for i, p := range projects {
		eg.Go(func() error {
			repos[i] = repoStatus(gctx, p)
			return nil
		})
	}
	_ = eg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return GitSnapshot{Repos: repos}, nil
}

// repoStatus never fails as a whole: errors are per repo, so one broken
// checkout does not hide the others.
func repoStatus(ctx context.Context, p config.Project) Repo {
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
	if !st.Initial {
		c, err := lastCommit(ctx, p.Path)
		if err != nil {
			r.Err = err.Error()
		}
		r.LastCommit = c
	}
	return r
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
