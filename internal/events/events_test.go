package events

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	for i := range 3 {
		if err := l.Append(Event{At: at.Add(time.Duration(i) * time.Minute), Kind: "ci_broken", Title: fmt.Sprint("e", i)}); err != nil {
			t.Fatal(err)
		}
	}
	got := l.Recent(2)
	if len(got) != 2 || got[0].Title != "e2" || got[1].Title != "e1" {
		t.Errorf("recent = %+v, want newest first", got)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if r := again.Recent(10); len(r) != 3 || r[0].Title != "e2" || !r[0].At.Equal(at.Add(2*time.Minute)) {
		t.Errorf("reloaded = %+v", r)
	}
}

func TestLogCompacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	var b strings.Builder
	for i := range 5*keep + 7 {
		fmt.Fprintf(&b, `{"kind":"x","title":"e%d"}`+"\n", i)
	}
	b.WriteString("not json\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if r := l.Recent(1); r[0].Title != fmt.Sprint("e", 5*keep+6) {
		t.Errorf("newest = %+v", r[0])
	}
	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), "\n"); n != keep {
		t.Errorf("compacted file has %d lines, want %d", n, keep)
	}
}

func TestMemoryOnly(t *testing.T) {
	l, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	for i := range keep + 10 {
		_ = l.Append(Event{Title: fmt.Sprint(i)})
	}
	if r := l.Recent(keep * 2); len(r) != keep {
		t.Errorf("kept %d events in memory, want %d", len(r), keep)
	}
}
