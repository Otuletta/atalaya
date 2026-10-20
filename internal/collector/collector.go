// Package collector defines the data sources of Atalaya.
//
// Each source implements Collector. The scheduler runs every collector in its
// own goroutine at its own interval; nothing here knows about the UI. Adding a
// source (Vercel, Sentry, Railway…) means implementing Collector and
// registering it in cmd/atalaya.
package collector

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Snapshot is the result of one collection. It must be a value that is safe
// to share between goroutines once returned (the collector never mutates it
// afterwards) and that encodes to JSON.
type Snapshot any

// Collector is one data source.
type Collector interface {
	// Name identifies the source: "git", "github", "health"…
	Name() string
	// Interval is the time between two successful collections.
	Interval() time.Duration
	// Collect gathers a fresh snapshot. It must honor ctx cancellation.
	Collect(ctx context.Context) (Snapshot, error)
}

// RetryAfterError asks the scheduler not to call the collector again before
// At, e.g. when an API rate limit is exhausted.
type RetryAfterError struct {
	Err error
	At  time.Time
}

func (e *RetryAfterError) Error() string {
	return fmt.Sprintf("%v (reintento a las %s)", e.Err, e.At.Format("15:04:05"))
}

func (e *RetryAfterError) Unwrap() error { return e.Err }

// ErrUnavailable marks a source whose tool is not installed or not running
// (Docker, Ollama). It is reported, but not retried with backoff.
var ErrUnavailable = errors.New("no disponible")

// run executes a command and returns its stdout. stderr is folded into the
// error so it can be shown to the user.
func run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return nil, err
		}
		if i := strings.IndexByte(msg, '\n'); i > 0 {
			msg = msg[:i]
		}
		return nil, fmt.Errorf("%s: %s", name, msg)
	}
	return out, nil
}
