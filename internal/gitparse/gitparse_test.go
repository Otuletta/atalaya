package gitparse

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func load(t *testing.T, name string) Status {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".z"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := Parse(b)
	if err != nil {
		t.Fatalf("Parse(%s): %v", name, err)
	}
	return s
}

func TestFixtures(t *testing.T) {
	tests := []struct {
		fixture  string
		head     string
		detached bool
		initial  bool
		upstream string
		hasAB    bool
		ahead    int
		behind   int
		gone     bool
		stash    int
		counts   Counts
	}{
		{fixture: "clean", head: "main", upstream: "origin/main", hasAB: true},
		{fixture: "ahead_behind", head: "main", upstream: "origin/main", hasAB: true, ahead: 2, behind: 1},
		{fixture: "dirty", head: "main", upstream: "origin/main", hasAB: true,
			counts: Counts{Staged: 3, Unstaged: 2, Untracked: 3}},
		{fixture: "deleted", head: "main", upstream: "origin/main", hasAB: true,
			counts: Counts{Staged: 1, Unstaged: 1}},
		{fixture: "rename", head: "main", upstream: "origin/main", hasAB: true,
			counts: Counts{Staged: 1}},
		{fixture: "conflict", head: "main", upstream: "origin/main", hasAB: true, ahead: 1,
			counts: Counts{Conflicts: 1}},
		{fixture: "initial", head: "main", initial: true, counts: Counts{Untracked: 1}},
		{fixture: "detached", detached: true},
		{fixture: "stash", head: "main", upstream: "origin/main", hasAB: true, stash: 2},
		{fixture: "no_upstream", head: "wip"},
		{fixture: "upstream_gone", head: "topic", upstream: "origin/topic", gone: true},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			s := load(t, tt.fixture)
			if s.Head != tt.head || s.Detached != tt.detached || s.Initial != tt.initial {
				t.Errorf("head = %q detached=%v initial=%v, want %q %v %v",
					s.Head, s.Detached, s.Initial, tt.head, tt.detached, tt.initial)
			}
			if s.Upstream != tt.upstream || s.HasAheadBehind != tt.hasAB {
				t.Errorf("upstream = %q hasAB=%v, want %q %v", s.Upstream, s.HasAheadBehind, tt.upstream, tt.hasAB)
			}
			if s.Ahead != tt.ahead || s.Behind != tt.behind {
				t.Errorf("ahead/behind = %d/%d, want %d/%d", s.Ahead, s.Behind, tt.ahead, tt.behind)
			}
			if s.UpstreamGone() != tt.gone {
				t.Errorf("UpstreamGone = %v, want %v", s.UpstreamGone(), tt.gone)
			}
			if s.Stash != tt.stash {
				t.Errorf("stash = %d, want %d", s.Stash, tt.stash)
			}
			if got := s.Counts(); got != tt.counts {
				t.Errorf("counts = %+v, want %+v", got, tt.counts)
			}
			if tt.initial == (len(s.OID) == 40) {
				t.Errorf("OID = %q with initial=%v", s.OID, tt.initial)
			}
		})
	}
}

func TestDirtyEntries(t *testing.T) {
	s := load(t, "dirty")
	want := []Entry{
		{Kind: Ordinary, X: 'M', Y: '.', Sub: "N...", Path: "a.txt"},
		{Kind: Ordinary, X: 'M', Y: 'M', Sub: "N...", Path: "b.txt"},
		{Kind: Ordinary, X: '.', Y: 'M', Sub: "N...", Path: "c.txt"},
		{Kind: Ordinary, X: 'A', Y: '.', Sub: "N...", Path: "new file.txt"},
		{Kind: Untracked, X: '?', Y: '?', Path: "dir/"},
		{Kind: Untracked, X: '?', Y: '?', Path: "line\nbreak.txt"},
		{Kind: Untracked, X: '?', Y: '?', Path: "ñandú 🦤.txt"},
	}
	if !reflect.DeepEqual(s.Entries, want) {
		t.Errorf("entries:\n got %+v\nwant %+v", s.Entries, want)
	}
}

func TestRename(t *testing.T) {
	s := load(t, "rename")
	want := Entry{Kind: Renamed, X: 'R', Y: '.', Sub: "N...", Score: "R100", Path: "renamed a.txt", OrigPath: "a.txt"}
	if len(s.Entries) != 1 || s.Entries[0] != want {
		t.Errorf("entries = %+v, want [%+v]", s.Entries, want)
	}
}

func TestConflictEntry(t *testing.T) {
	s := load(t, "conflict")
	if len(s.Entries) != 1 {
		t.Fatalf("entries = %+v", s.Entries)
	}
	e := s.Entries[0]
	if e.Kind != Unmerged || e.X != 'U' || e.Y != 'U' || e.Path != "a.txt" {
		t.Errorf("entry = %+v", e)
	}
	if e.Staged() || e.Unstaged() {
		t.Error("conflicts must not count as staged or unstaged")
	}
}

func TestEmptyInput(t *testing.T) {
	s, err := Parse(nil)
	if err != nil || !reflect.DeepEqual(s, Status{}) {
		t.Errorf("Parse(nil) = %+v, %v", s, err)
	}
}

func TestIgnoredNotCounted(t *testing.T) {
	s, err := Parse([]byte("# branch.head main\x00! build/\x00"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entries) != 1 || s.Entries[0].Kind != Ignored {
		t.Fatalf("entries = %+v", s.Entries)
	}
	if s.Counts().Dirty() {
		t.Error("ignored files must not make the tree dirty")
	}
}

func TestUnknownHeaderIgnored(t *testing.T) {
	s, err := Parse([]byte("# branch.head main\x00# future.header 42\x00"))
	if err != nil || s.Head != "main" {
		t.Errorf("got %+v, %v", s, err)
	}
}

func TestMalformed(t *testing.T) {
	tests := map[string]string{
		"unknown type":      "x what\x00",
		"bad ab":            "# branch.ab 3 4\x00",
		"bad ab numbers":    "# branch.ab +a -b\x00",
		"bad stash":         "# stash many\x00",
		"short ordinary":    "1 M. N...\x00",
		"bad XY":            "1 M N... 100644 100644 100644 abc def a.txt\x00",
		"rename at the end": "2 R. N... 100644 100644 100644 abc def R100 new.txt\x00",
		"short unmerged":    "u UU N... 1 2 3\x00",
		"untracked no path": "?\x00",
	}
	for name, in := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(in))
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v, want *ParseError", err)
			}
			if !strings.Contains(err.Error(), "gitparse:") {
				t.Errorf("error message = %q", err)
			}
		})
	}
}

func FuzzParse(f *testing.F) {
	for _, name := range []string{"clean", "dirty", "rename", "conflict", "initial", "stash"} {
		b, err := os.ReadFile(filepath.Join("testdata", name+".z"))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		s, err := Parse(b)
		if err != nil {
			return
		}
		c := s.Counts()
		if c.Staged < 0 || c.Unstaged < 0 || c.Untracked < 0 || c.Conflicts < 0 {
			t.Fatalf("negative counts %+v", c)
		}
	})
}
