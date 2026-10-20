// Package events is Atalaya's memory: a log of what changed (a CI that
// broke, an endpoint that went down, an agent that waits for you…). The
// detector writes it, the Activity panel and `atalaya log` read it.
package events

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Severity colors an event.
const (
	Bad  = "bad"  // something broke
	Good = "good" // something recovered
	Ask  = "ask"  // something waits for you
	Info = "info"
)

// Event is one thing that happened.
type Event struct {
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Severity string    `json:"severity"`
	Project  string    `json:"project,omitempty"`
	Title    string    `json:"title"`
	Body     string    `json:"body,omitempty"`
	URL      string    `json:"url,omitempty"`
}

// Snapshot is what the store holds under "events", newest first.
type Snapshot struct {
	Events []Event `json:"events"`
}

// keep is how many events stay in memory, and in the file after it is
// compacted.
const keep = 500

// Log appends events to a JSON-lines file and keeps the latest in memory.
// With an empty path it lives only in memory (demo, tests).
type Log struct {
	path string

	mu     sync.Mutex
	recent []Event // oldest first
}

// Open loads the latest events from path, compacting the file when it has
// grown past four times what is kept.
func Open(path string) (*Log, error) {
	l := &Log{path: path}
	if path == "" {
		return l, nil
	}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var all []Event
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			all = append(all, e)
		}
	}
	if len(all) > keep {
		l.recent = append([]Event(nil), all[len(all)-keep:]...)
	} else {
		l.recent = all
	}
	if len(all) > 4*keep {
		if err := l.rewrite(); err != nil {
			return nil, err
		}
	}
	return l, nil
}

// rewrite replaces the file with the in-memory events, atomically.
func (l *Log) rewrite() error {
	var buf bytes.Buffer
	for _, e := range l.recent {
		b, _ := json.Marshal(e)
		buf.Write(append(b, '\n'))
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, l.path)
}

// Append records an event in memory and on disk.
func (l *Log) Append(e Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recent = append(l.recent, e)
	if len(l.recent) > keep {
		l.recent = l.recent[len(l.recent)-keep:]
	}
	if l.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(e)
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Recent returns up to n events, newest first.
func (l *Log) Recent(n int) []Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Event, 0, min(n, len(l.recent)))
	for i := len(l.recent) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, l.recent[i])
	}
	return out
}
