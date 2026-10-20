package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/store"
)

func TestBackoff(t *testing.T) {
	tests := []struct {
		interval time.Duration
		failures int
		want     time.Duration
	}{
		{5 * time.Second, 0, 5 * time.Second},
		{5 * time.Second, 1, 10 * time.Second},
		{5 * time.Second, 3, 40 * time.Second},
		{5 * time.Second, 10, MaxBackoff},
		{5 * time.Second, 1000, MaxBackoff},
		{10 * time.Minute, 2, 10 * time.Minute}, // never below the interval
	}
	for _, tt := range tests {
		if got := Backoff(tt.interval, tt.failures); got != tt.want {
			t.Errorf("Backoff(%v, %d) = %v, want %v", tt.interval, tt.failures, got, tt.want)
		}
	}
}

func TestTimeout(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{
		3 * time.Second:  20 * time.Second,
		30 * time.Second: time.Minute,
		10 * time.Minute: 2 * time.Minute,
	} {
		if got := Timeout(in); got != want {
			t.Errorf("Timeout(%v) = %v, want %v", in, got, want)
		}
	}
}

// fake is a collector whose behavior is set per call.
type fake struct {
	name     string
	interval time.Duration
	calls    atomic.Int32
	fn       func(ctx context.Context, n int32) (collector.Snapshot, error)
}

func (f *fake) Name() string            { return f.name }
func (f *fake) Interval() time.Duration { return f.interval }
func (f *fake) Collect(ctx context.Context) (collector.Snapshot, error) {
	return f.fn(ctx, f.calls.Add(1))
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func start(t *testing.T, cs ...collector.Collector) (*Scheduler, *store.Store) {
	t.Helper()
	st := store.New()
	s := New(st, cs...)
	s.Jitter = false
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return after cancel: leaked goroutines")
		}
	})
	return s, st
}

func TestRunCollectsEverySourceIndependently(t *testing.T) {
	ok := &fake{name: "ok", interval: time.Hour, fn: func(context.Context, int32) (collector.Snapshot, error) {
		return "data", nil
	}}
	// A source that hangs until cancelled must not delay the others.
	stuck := &fake{name: "stuck", interval: time.Hour, fn: func(ctx context.Context, _ int32) (collector.Snapshot, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, st := start(t, ok, stuck)
	eventually(t, "ok to report", func() bool {
		e, found := st.Snapshot().Get("ok")
		return found && e.Data == "data"
	})
}

func TestErrorKeepsCacheAndRefreshRecovers(t *testing.T) {
	f := &fake{name: "src", interval: time.Minute, fn: func(_ context.Context, n int32) (collector.Snapshot, error) {
		switch n {
		case 1:
			return "v1", nil
		case 2:
			return nil, errors.New("api caída")
		}
		return "v3", nil
	}}
	s, st := start(t, f)
	get := func() store.Entry { e, _ := st.Snapshot().Get("src"); return e }

	eventually(t, "first run", func() bool { return get().Data == "v1" })
	s.Refresh("src")
	eventually(t, "failing run", func() bool { return get().Err != "" })
	if e := get(); e.Data != "v1" || e.Failures != 1 {
		t.Errorf("after error: %+v, want cached v1 and 1 failure", e)
	}
	// The next scheduled run is pushed back by the backoff: 2× the interval.
	if e := get(); time.Until(e.NextRun) < 110*time.Second {
		t.Errorf("next run in %v, want about 2m of backoff", time.Until(e.NextRun))
	}
	// A manual refresh skips the backoff.
	s.Refresh()
	eventually(t, "recovery", func() bool { return get().Data == "v3" && get().Err == "" })
}

func TestPanicBecomesError(t *testing.T) {
	f := &fake{name: "bad", interval: time.Hour, fn: func(context.Context, int32) (collector.Snapshot, error) {
		panic("nil map")
	}}
	_, st := start(t, f)
	eventually(t, "panic reported", func() bool {
		e, _ := st.Snapshot().Get("bad")
		return e.Err == "panic en bad: nil map"
	})
}

func TestRetryAfterIsHonored(t *testing.T) {
	at := time.Now().Add(3 * time.Hour)
	f := &fake{name: "gh", interval: time.Minute, fn: func(context.Context, int32) (collector.Snapshot, error) {
		return "partial", &collector.RetryAfterError{Err: errors.New("rate limit"), At: at}
	}}
	_, st := start(t, f)
	eventually(t, "retry scheduled", func() bool {
		e, _ := st.Snapshot().Get("gh")
		return e.Data == "partial" && !e.NextRun.Before(at.Add(-time.Second))
	})
}

func TestRunOnce(t *testing.T) {
	a := &fake{name: "a", interval: time.Hour, fn: func(context.Context, int32) (collector.Snapshot, error) { return 1, nil }}
	b := &fake{name: "b", interval: time.Hour, fn: func(context.Context, int32) (collector.Snapshot, error) {
		return nil, errors.New("no")
	}}
	st := store.New()
	New(st, a, b).RunOnce(context.Background())
	snap := st.Snapshot()
	if e, _ := snap.Get("a"); e.Data != 1 {
		t.Errorf("a = %+v", e)
	}
	if e, _ := snap.Get("b"); e.Err != "no" {
		t.Errorf("b = %+v", e)
	}
	if a.calls.Load() != 1 || b.calls.Load() != 1 {
		t.Errorf("calls = %d, %d; want exactly one each", a.calls.Load(), b.calls.Load())
	}
}

func TestRefreshNeverBlocks(t *testing.T) {
	s := New(store.New(), &fake{name: "x", interval: time.Hour})
	done := make(chan struct{})
	go func() {
		for range 1000 {
			s.Refresh()
			s.Refresh("x", "unknown")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Refresh blocked without a running loop")
	}
}
