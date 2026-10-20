package store

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type snap struct{ N int }

func TestPutKeepsLastGoodData(t *testing.T) {
	s := New()
	s.Put("git", snap{1}, nil, time.Time{})
	s.Put("git", nil, errors.New("boom"), time.Time{})

	e, _ := s.Snapshot().Get("git")
	if e.Data != (snap{1}) {
		t.Errorf("data = %v, want the cached snapshot", e.Data)
	}
	if e.Err != "boom" || e.Failures != 1 || !e.Stale() {
		t.Errorf("entry = %+v", e)
	}

	s.Put("git", snap{2}, nil, time.Time{})
	e, _ = s.Snapshot().Get("git")
	if e.Data != (snap{2}) || e.Err != "" || e.Failures != 0 || e.Stale() {
		t.Errorf("entry after recovery = %+v", e)
	}
}

func TestTypedData(t *testing.T) {
	s := New()
	if _, ok := Data[snap](s.Snapshot(), "git"); ok {
		t.Error("missing source reported as present")
	}
	s.Put("git", snap{7}, nil, time.Time{})
	v, ok := Data[snap](s.Snapshot(), "git")
	if !ok || v.N != 7 {
		t.Errorf("Data = %v, %v", v, ok)
	}
	if _, ok := Data[string](s.Snapshot(), "git"); ok {
		t.Error("wrong type reported as present")
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	s := New()
	s.Put("a", snap{1}, nil, time.Time{})
	st := s.Snapshot()
	s.Put("b", snap{2}, nil, time.Time{})
	if _, ok := st.Get("b"); ok {
		t.Error("an older snapshot sees later writes")
	}
	if st.Version >= s.Snapshot().Version {
		t.Error("version did not increase")
	}
}

func TestChangedCoalesces(t *testing.T) {
	s := New()
	for i := range 100 {
		s.Put("a", snap{i}, nil, time.Time{})
	}
	select {
	case <-s.Changed():
	default:
		t.Fatal("no change signal")
	}
	select {
	case <-s.Changed():
		t.Fatal("signals were not coalesced")
	default:
	}
}

func TestSubscribersAreIndependent(t *testing.T) {
	s := New()
	sub := s.Subscribe()
	s.Put("a", snap{1}, nil, time.Time{})
	// The UI reads its signal; the other subscriber must still have its own.
	<-s.Changed()
	select {
	case <-sub:
	default:
		t.Fatal("a reader consumed another reader's signal")
	}
	select {
	case <-sub:
		t.Fatal("signals were not coalesced per subscriber")
	default:
	}
}

func TestUnsubscribe(t *testing.T) {
	s := New()
	sub := s.Subscribe()
	s.Unsubscribe(sub)
	s.Put("a", snap{1}, nil, time.Time{})
	select {
	case <-sub:
		t.Fatal("an unsubscribed channel was signaled")
	default:
	}
	s.Unsubscribe(sub) // twice is harmless
}

func TestJSON(t *testing.T) {
	s := New()
	s.Put("git", snap{3}, nil, time.Time{})
	s.Put("github", nil, errors.New("sin token"), time.Time{})
	b, err := json.Marshal(s.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"sources"`, `"git":{"data":{"N":3}`, `"github":{"data":null,"error":"sin token"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("JSON %s does not contain %s", b, want)
		}
	}
}

func TestConcurrentAccess(t *testing.T) {
	s := New()
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 200 {
				s.SetRunning("src", true)
				s.Put("src", snap{w*1000 + i}, nil, time.Time{})
			}
		})
		wg.Go(func() {
			for range 200 {
				_ = s.Snapshot()
				select {
				case <-s.Changed():
				default:
				}
			}
		})
	}
	wg.Wait()
}
