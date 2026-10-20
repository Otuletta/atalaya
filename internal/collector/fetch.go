package collector

import (
	"context"
	"os"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/otuletta/atalaya/internal/config"
)

// FetchResult is the outcome of the last background fetch of a repo.
type FetchResult struct {
	Project string    `json:"project"`
	At      time.Time `json:"at"`
	Err     string    `json:"error,omitempty"`
}

// FetchSnapshot is the result of the fetch collector.
type FetchSnapshot struct {
	Repos []FetchResult `json:"repos"`
}

// Fetch runs `git fetch` periodically so ahead/behind reflects the remote and
// not just the last time the user happened to fetch. It is a collector of
// its own so the slow network call never delays the 5-second status poll.
type Fetch struct {
	Projects []config.Project
	Every    time.Duration
	// Now is overridable in tests.
	Now func() time.Time
}

func (f *Fetch) Name() string            { return "fetch" }
func (f *Fetch) Interval() time.Duration { return f.Every }

func (f *Fetch) Collect(ctx context.Context) (Snapshot, error) {
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	env := append([]string{}, gitEnv...)
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		// Never block on an SSH passphrase or host-key prompt.
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ConnectTimeout=10")
	}
	var projects []config.Project
	for _, p := range f.Projects {
		if p.Path != "" {
			projects = append(projects, p)
		}
	}
	res := make([]FetchResult, len(projects))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(4)
	for i, p := range projects {
		eg.Go(func() error {
			rctx, cancel := context.WithTimeout(gctx, 60*time.Second)
			defer cancel()
			r := FetchResult{Project: p.Name}
			if _, err := run(rctx, env, "git", "-C", p.Path, "fetch", "--quiet"); err != nil {
				r.Err = err.Error()
			}
			r.At = now()
			res[i] = r
			return nil
		})
	}
	_ = eg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return FetchSnapshot{Repos: res}, nil
}
