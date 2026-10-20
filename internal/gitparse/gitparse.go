// Package gitparse parses the output of
//
//	git status --porcelain=v2 --branch --show-stash -z
//
// The -z form is the only one that is unambiguous: paths are never quoted and
// may contain spaces, newlines or any other byte except NUL.
//
// Format reference: https://git-scm.com/docs/git-status#_porcelain_format_version_2
package gitparse

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// Kind is the type of a status entry.
type Kind byte

const (
	Ordinary  Kind = '1' // changed tracked file
	Renamed   Kind = '2' // renamed or copied file
	Unmerged  Kind = 'u' // file with merge conflicts
	Untracked Kind = '?'
	Ignored   Kind = '!'
)

// Entry is one file reported by git status.
type Entry struct {
	Kind Kind
	// X is the index (staged) state and Y the worktree state. '.' means
	// unmodified. Untracked and ignored entries use '?' and '!'.
	X, Y byte
	// Sub is the submodule state ("N..." when not a submodule).
	Sub string
	// Score is the rename/copy score, e.g. "R100" (only for Renamed).
	Score    string
	Path     string
	OrigPath string // only for Renamed
}

// Staged reports whether the entry has changes in the index.
func (e Entry) Staged() bool {
	return (e.Kind == Ordinary || e.Kind == Renamed) && e.X != '.'
}

// Unstaged reports whether the entry has changes in the worktree.
func (e Entry) Unstaged() bool {
	return (e.Kind == Ordinary || e.Kind == Renamed) && e.Y != '.'
}

// Status is the parsed result.
type Status struct {
	// OID is the current commit, empty on a repo without commits.
	OID     string
	Initial bool // no commits yet
	// Head is the branch name. Empty when Detached.
	Head     string
	Detached bool
	// Upstream is the configured upstream branch, e.g. "origin/main".
	Upstream string
	// HasAheadBehind is false when there is no upstream or it is gone
	// (deleted on the remote). Ahead and Behind are only valid when true.
	HasAheadBehind bool
	Ahead, Behind  int
	Stash          int
	Entries        []Entry
}

// UpstreamGone reports a configured upstream that no longer exists.
func (s Status) UpstreamGone() bool {
	return s.Upstream != "" && !s.HasAheadBehind
}

// Counts summarizes the entries. A file can be both staged and unstaged.
type Counts struct {
	Staged    int `json:"staged"`
	Unstaged  int `json:"unstaged"`
	Untracked int `json:"untracked"`
	Conflicts int `json:"conflicts"`
}

// Dirty reports any uncommitted work, untracked files included.
func (c Counts) Dirty() bool {
	return c.Staged+c.Unstaged+c.Untracked+c.Conflicts > 0
}

// Counts returns the summary of the entries. Ignored files are not counted.
func (s Status) Counts() Counts {
	var c Counts
	for _, e := range s.Entries {
		switch e.Kind {
		case Ordinary, Renamed:
			if e.Staged() {
				c.Staged++
			}
			if e.Unstaged() {
				c.Unstaged++
			}
		case Unmerged:
			c.Conflicts++
		case Untracked:
			c.Untracked++
		}
	}
	return c
}

// ParseError points at the record that could not be parsed.
type ParseError struct {
	Record string
	Reason string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("gitparse: %s: %q", e.Reason, e.Record)
}

// Parse parses NUL-separated porcelain v2 output.
func Parse(out []byte) (Status, error) {
	var s Status
	// Every record ends in NUL; trim the last one so Split does not yield a
	// trailing empty record.
	records := bytes.Split(bytes.TrimSuffix(out, []byte{0}), []byte{0})
	for i := 0; i < len(records); i++ {
		rec := string(records[i])
		if rec == "" {
			continue
		}
		var err error
		switch rec[0] {
		case '#':
			err = s.parseHeader(rec)
		case '1':
			var e Entry
			e, err = parseOrdinary(rec)
			s.Entries = append(s.Entries, e)
		case '2':
			// The original path is the next NUL-separated record.
			if i+1 >= len(records) || len(records[i+1]) == 0 {
				return s, &ParseError{rec, "rename without original path"}
			}
			i++
			var e Entry
			e, err = parseRenamed(rec, string(records[i]))
			s.Entries = append(s.Entries, e)
		case 'u':
			var e Entry
			e, err = parseUnmerged(rec)
			s.Entries = append(s.Entries, e)
		case '?', '!':
			if len(rec) < 3 || rec[1] != ' ' {
				return s, &ParseError{rec, "malformed path entry"}
			}
			s.Entries = append(s.Entries, Entry{Kind: Kind(rec[0]), X: rec[0], Y: rec[0], Path: rec[2:]})
		default:
			return s, &ParseError{rec, "unknown record type"}
		}
		if err != nil {
			return s, err
		}
	}
	return s, nil
}

func (s *Status) parseHeader(rec string) error {
	key, value, _ := strings.Cut(strings.TrimPrefix(rec, "# "), " ")
	switch key {
	case "branch.oid":
		if value == "(initial)" {
			s.Initial = true
		} else {
			s.OID = value
		}
	case "branch.head":
		if value == "(detached)" {
			s.Detached = true
		} else {
			s.Head = value
		}
	case "branch.upstream":
		s.Upstream = value
	case "branch.ab":
		a, b, ok := strings.Cut(value, " ")
		if !ok || !strings.HasPrefix(a, "+") || !strings.HasPrefix(b, "-") {
			return &ParseError{rec, "malformed branch.ab"}
		}
		ahead, err1 := strconv.Atoi(a[1:])
		behind, err2 := strconv.Atoi(b[1:])
		if err1 != nil || err2 != nil {
			return &ParseError{rec, "malformed branch.ab"}
		}
		s.HasAheadBehind, s.Ahead, s.Behind = true, ahead, behind
	case "stash":
		n, err := strconv.Atoi(value)
		if err != nil {
			return &ParseError{rec, "malformed stash count"}
		}
		s.Stash = n
	}
	// Unknown headers are ignored: git may add new ones.
	return nil
}

// fields splits rec into n space-separated fields; the last one keeps any
// remaining spaces because it is a path.
func fields(rec string, n int) ([]string, bool) {
	f := strings.SplitN(rec, " ", n)
	return f, len(f) == n
}

func xy(rec, s string) (byte, byte, error) {
	if len(s) != 2 {
		return 0, 0, &ParseError{rec, "malformed XY"}
	}
	return s[0], s[1], nil
}

// 1 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <path>
func parseOrdinary(rec string) (Entry, error) {
	f, ok := fields(rec, 9)
	if !ok {
		return Entry{}, &ParseError{rec, "malformed ordinary entry"}
	}
	x, y, err := xy(rec, f[1])
	return Entry{Kind: Ordinary, X: x, Y: y, Sub: f[2], Path: f[8]}, err
}

// 2 <XY> <sub> <mH> <mI> <mW> <hH> <hI> <X><score> <path>  NUL <origPath>
func parseRenamed(rec, orig string) (Entry, error) {
	f, ok := fields(rec, 10)
	if !ok {
		return Entry{}, &ParseError{rec, "malformed rename entry"}
	}
	x, y, err := xy(rec, f[1])
	return Entry{Kind: Renamed, X: x, Y: y, Sub: f[2], Score: f[8], Path: f[9], OrigPath: orig}, err
}

// u <XY> <sub> <m1> <m2> <m3> <mW> <h1> <h2> <h3> <path>
func parseUnmerged(rec string) (Entry, error) {
	f, ok := fields(rec, 11)
	if !ok {
		return Entry{}, &ParseError{rec, "malformed unmerged entry"}
	}
	x, y, err := xy(rec, f[1])
	return Entry{Kind: Unmerged, X: x, Y: y, Sub: f[2], Path: f[10]}, err
}
