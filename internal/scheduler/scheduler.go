// Package scheduler runs every collector in its own goroutine, at its own
// interval, with exponential backoff on errors.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/store"
)

// MaxBackoff caps the wait after repeated errors.
const MaxBackoff = 5 * time.Minute

// Backoff returns the wait after the given number of consecutive failures:
// interval·2^failures, capped at max(MaxBackoff, interval).
func Backoff(interval time.Duration, failures int) time.Duration {
	limit := max(MaxBackoff, interval)
	d := interval
	for range failures {
		d *= 2
		if d >= limit {
			return limit
		}
	}
	return d
}

// Timeout is how long one collection may take: twice its interval, between
// 20 seconds and 2 minutes.
func Timeout(interval time.Duration) time.Duration {
	return min(max(2*interval, 20*time.Second), 2*time.Minute)
}

// Scheduler owns the collector goroutines.
type Scheduler struct {
	store      *store.Store
	collectors []collector.Collector
	triggers   map[string]chan struct{}
	// Jitter spreads retries so failing sources do not fire in lockstep.
	// Tests set it to false.
	Jitter bool
}

// New prepares a scheduler. Collector names must be unique.
func New(st *store.Store, cs ...collector.Collector) *Scheduler {
	s := &Scheduler{store: st, collectors: cs, triggers: map[string]chan struct{}{}, Jitter: true}
	for _, c := range cs {
		s.triggers[c.Name()] = make(chan struct{}, 1)
	}
	return s
}

// Names lists the registered collectors in order.
func (s *Scheduler) Names() []string {
	names := make([]string, len(s.collectors))
	for i, c := range s.collectors {
		names[i] = c.Name()
	}
	return names
}

// Run starts one goroutine per collector and blocks until ctx is done and
// every goroutine has exited.
func (s *Scheduler) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, c := range s.collectors {
		wg.Go(func() { s.loop(ctx, c) })
	}
	wg.Wait()
}

// RunOnce runs every collector once, concurrently, and waits for all of
// them. Used by `atalaya status`.
func (s *Scheduler) RunOnce(ctx context.Context) {
	var wg sync.WaitGroup
	for _, c := range s.collectors {
		wg.Go(func() {
			snap, err := s.collect(ctx, c)
			s.store.Put(c.Name(), snap, err, time.Time{})
		})
	}
	wg.Wait()
}

// Refresh wakes the named collectors (all when none is given) so they run
// now instead of at their next tick. It never blocks.
func (s *Scheduler) Refresh(names ...string) {
	if len(names) == 0 {
		names = s.Names()
	}
	for _, n := range names {
		if ch, ok := s.triggers[n]; ok {
			select {
			case ch <- struct{}{}:
			default: // a refresh is already pending
			}
		}
	}
}

func (s *Scheduler) loop(ctx context.Context, c collector.Collector) {
	failures := 0
	for {
		s.store.SetRunning(c.Name(), true)
		snap, err := s.collect(ctx, c)
		if ctx.Err() != nil {
			return
		}
		wait := c.Interval()
		var retry *collector.RetryAfterError
		switch {
		case err == nil:
			failures = 0
		case errors.As(err, &retry):
			failures++
			wait = max(time.Until(retry.At), c.Interval())
		default:
			failures++
			wait = Backoff(c.Interval(), failures)
			if s.Jitter {
				wait += rand.N(wait/10 + 1)
			}
		}
		s.store.Put(c.Name(), snap, err, time.Now().Add(wait))

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-s.triggers[c.Name()]:
			timer.Stop()
		}
	}
}

// collect runs one collection with a timeout, turning a panic into an error
// so a bug in one source cannot take the whole program down.
func (s *Scheduler) collect(ctx context.Context, c collector.Collector) (snap collector.Snapshot, err error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout(c.Interval()))
	defer cancel()
	defer func() {
		if r := recover(); r != nil {
			snap, err = nil, fmt.Errorf("panic en %s: %v", c.Name(), r)
		}
	}()
	snap, err = c.Collect(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("%s tardó más de %s", c.Name(), Timeout(c.Interval()))
	}
	return snap, err
}
