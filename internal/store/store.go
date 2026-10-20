// Package store holds the latest snapshot of every collector. The same state
// feeds the TUI and `atalaya status --json`.
package store

import (
	"encoding/json"
	"maps"
	"sync"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
)

// Entry is the state of one source.
type Entry struct {
	// Data is the last successful snapshot. It survives later errors, so a
	// panel keeps showing (stale) data while its source is failing.
	Data      collector.Snapshot `json:"data"`
	Err       string             `json:"error,omitempty"`
	UpdatedAt time.Time          `json:"updated_at"`
	SuccessAt time.Time          `json:"last_success,omitzero"`
	Failures  int                `json:"failures,omitempty"`
	NextRun   time.Time          `json:"next_run,omitzero"`
	Running   bool               `json:"-"`
}

// Stale reports an entry whose data is older than its last attempt.
func (e Entry) Stale() bool { return e.Err != "" && e.Data != nil }

// State is an immutable copy of the store.
type State struct {
	Version uint64
	Entries map[string]Entry
}

// Get returns the entry of a source.
func (s State) Get(name string) (Entry, bool) {
	e, ok := s.Entries[name]
	return e, ok
}

// MarshalJSON writes {"sources": {"git": {...}, ...}}.
func (s State) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Sources map[string]Entry `json:"sources"`
	}{s.Entries})
}

// Data returns the typed snapshot of a source. ok is false when the source
// has not produced data yet.
func Data[T any](s State, name string) (T, bool) {
	e, found := s.Entries[name]
	v, ok := e.Data.(T)
	return v, found && ok
}

// Store is safe for concurrent use.
type Store struct {
	mu      sync.RWMutex
	entries map[string]Entry
	version uint64
	changed chan struct{}
	subs    []chan struct{}
	now     func() time.Time
}

// New returns an empty store.
func New() *Store {
	changed := make(chan struct{}, 1)
	return &Store{entries: map[string]Entry{}, changed: changed, subs: []chan struct{}{changed}, now: time.Now}
}

// Changed is signaled after every update. Signals coalesce: a slow reader
// sees one signal for many updates and then reads the whole State, so it
// can never miss data or block a collector.
func (s *Store) Changed() <-chan struct{} { return s.changed }

// Subscribe returns a channel of its own with the same coalescing
// behavior as Changed, for readers other than the UI (the notifier).
func (s *Store) Subscribe() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{}, 1)
	s.subs = append(s.subs, ch)
	return ch
}

func (s *Store) notify() {
	s.version++
	for _, ch := range s.subs {
		select {
		case ch <- struct{}{}:
		default: // a signal is already pending for this reader
		}
	}
}

// Put records the outcome of a collection. A nil data keeps the previous
// snapshot (the cache); a non-nil err marks the entry as failing.
func (s *Store) Put(name string, data collector.Snapshot, err error, next time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[name]
	now := s.now()
	e.UpdatedAt, e.NextRun, e.Running = now, next, false
	if data != nil {
		e.Data = data
	}
	if err != nil {
		e.Err = err.Error()
		e.Failures++
	} else {
		e.Err, e.Failures, e.SuccessAt = "", 0, now
	}
	s.entries[name] = e
	s.notify()
}

// SetRunning flags a source as collecting right now.
func (s *Store) SetRunning(name string, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[name]
	if e.Running == running {
		return
	}
	e.Running = running
	s.entries[name] = e
	s.notify()
}

// Snapshot returns a copy of the current state. Snapshots inside entries are
// shared, which is safe because collectors never mutate them.
func (s *Store) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return State{Version: s.version, Entries: maps.Clone(s.entries)}
}
