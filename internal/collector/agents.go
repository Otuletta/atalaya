package collector

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/otuletta/atalaya/internal/config"
)

// Agent is a running coding-agent session.
type Agent struct {
	PID     int32  `json:"pid"`
	Name    string `json:"name"`
	Project string `json:"project,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	// Branch checked out in Cwd, and the worktree's name when Cwd is a
	// linked worktree (agents often work in one each).
	Branch   string    `json:"branch,omitempty"`
	Worktree string    `json:"worktree,omitempty"`
	Started  time.Time `json:"started"`
	// CPU is the process's CPU use since the previous collection, in %.
	CPU     float64  `json:"cpu"`
	Session *Session `json:"session,omitempty"`
	Cmdline string   `json:"cmdline,omitempty"`
}

// AgentsSnapshot is the result of the agents collector.
type AgentsSnapshot struct {
	Agents []Agent `json:"agents"`
}

// interpreters run agents distributed as scripts (`node …/bin/claude`); for
// them the script name in argv[1] is matched instead.
var interpreters = map[string]bool{"node": true, "bun": true, "deno": true, "python": true, "python3": true}

// Agents finds coding-agent processes by executable name. Detection by name
// is inherently fragile, which is why patterns live in the config. Patterns
// are globs (path.Match) and case-sensitive on purpose: "claude" is the CLI,
// "Claude" is the desktop app.
type Agents struct {
	Projects []config.Project
	Patterns []string
	Every    time.Duration
	// ClaudeDir overrides ~/.claude, where Claude Code keeps transcripts.
	ClaudeDir string
	Now       func() time.Time

	matcher  *projectMatcher
	sessions sessionReader
	cpu      map[int32]cpuSample
	places   map[string]place // by cwd
}

// procEntry is a process as listed, before it is looked at closer. ppid is
// -1 when the listing did not include it.
type procEntry struct {
	pid, ppid int32
	name      string
	truncated bool // the kernel cut the name: the full one must be asked for
}

// place is where an agent works, cached by cwd: one `git rev-parse` per
// agent every poll adds up. It is read again when the HEAD of its git dir
// changes (a checkout), and a folder outside git every placeRetry.
type place struct {
	branch, worktree string
	head, stamp      string
	checked          time.Time
}

const placeRetry = time.Minute

// couldMatch reports whether a truncated name may belong to an agent: when
// it shares the literal start of a pattern.
func couldMatch(patterns []string, name string) bool {
	for _, p := range patterns {
		lit := p
		if i := strings.IndexAny(p, "*?[\\"); i >= 0 {
			lit = p[:i]
		}
		if strings.HasPrefix(name, lit) || strings.HasPrefix(lit, name) {
			return true
		}
	}
	return false
}

func headStamp(head string) string {
	info, err := os.Stat(head)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d.%d", info.Size(), info.ModTime().UnixNano())
}

// place is gitPlace, cached.
func (a *Agents) place(ctx context.Context, cwd string, now time.Time, seen map[string]place) (branch, worktree string) {
	if p, ok := a.places[cwd]; ok {
		fresh := p.head != "" && headStamp(p.head) == p.stamp
		if fresh || p.head == "" && now.Sub(p.checked) < placeRetry {
			seen[cwd] = p
			return p.branch, p.worktree
		}
	}
	branch, worktree, gitDir := gitPlaceDir(ctx, cwd)
	p := place{branch: branch, worktree: worktree, checked: now}
	if gitDir != "" {
		p.head = filepath.Join(gitDir, "HEAD")
		p.stamp = headStamp(p.head)
	}
	seen[cwd] = p
	return branch, worktree
}

// cpuSample is a process's total CPU time at a moment, to compute its use
// between two collections.
type cpuSample struct {
	total float64 // seconds
	at    time.Time
}

func (a *Agents) Name() string            { return "agents" }
func (a *Agents) Interval() time.Duration { return a.Every }

// gitPlace is the branch checked out in dir and, when dir is a linked
// worktree, the worktree's name. Both are empty outside a repository.
func gitPlace(ctx context.Context, dir string) (branch, worktree string) {
	branch, worktree, _ = gitPlaceDir(ctx, dir)
	return branch, worktree
}

// gitPlaceDir is gitPlace plus the git dir, whose HEAD tells when the
// branch changes.
func gitPlaceDir(ctx context.Context, dir string) (branch, worktree, gitDir string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := run(ctx, gitEnv, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD",
		"--show-toplevel", "--absolute-git-dir", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", ""
	}
	f := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(f) != 4 {
		return "", "", ""
	}
	branch = f[0]
	if branch == "HEAD" {
		branch = "" // detached
	}
	if f[2] != f[3] { // a linked worktree has its own git dir
		worktree = filepath.Base(f[1])
	}
	return branch, worktree, f[2]
}

// MatchAgent returns the matched executable name, or "".
func MatchAgent(patterns []string, name string, argv []string) string {
	candidates := []string{name}
	if len(argv) > 0 {
		candidates = append(candidates, filepath.Base(argv[0]))
		if interpreters[filepath.Base(argv[0])] && len(argv) > 1 {
			candidates = append(candidates, filepath.Base(argv[1]))
		}
	}
	for _, c := range candidates {
		for _, p := range patterns {
			if ok, _ := path.Match(p, c); ok {
				return c
			}
		}
	}
	return ""
}

func (a *Agents) Collect(ctx context.Context) (Snapshot, error) {
	if a.matcher == nil {
		a.matcher = newProjectMatcher(a.Projects)
	}
	procs, err := allProcs(ctx)
	if err != nil {
		return nil, err
	}
	type hit struct {
		proc *process.Process
		name string
		ppid int32
	}
	hits := map[int32]hit{}
	for _, e := range procs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var p *process.Process
		proc := func() *process.Process {
			if p == nil {
				p, _ = process.NewProcessWithContext(ctx, e.pid)
			}
			return p
		}
		name := e.name
		if e.truncated && couldMatch(a.Patterns, name) && proc() != nil {
			if full, err := p.NameWithContext(ctx); err == nil {
				name = full
			}
		}
		matched := MatchAgent(a.Patterns, name, nil)
		if matched == "" && interpreters[name] && proc() != nil {
			// Only interpreters pay for reading their argv.
			argv, _ := p.CmdlineSliceWithContext(ctx)
			matched = MatchAgent(a.Patterns, name, argv)
		}
		if matched == "" || proc() == nil {
			continue
		}
		ppid := e.ppid
		if ppid < 0 {
			ppid, _ = p.PpidWithContext(ctx)
		}
		hits[e.pid] = hit{p, matched, ppid}
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	dir := a.ClaudeDir
	if dir == "" {
		dir = claudeDir()
	}
	cpuNow := map[int32]cpuSample{}
	places := map[string]place{}
	// Sub-processes of an agent (tool runners, sub-agents) are part of the
	// parent session, not sessions of their own.
	type candidate struct {
		pid  int32
		hit  hit
		info procInfo
	}
	var cands []candidate
	for pid, h := range hits {
		if _, parentIsAgent := hits[h.ppid]; !parentIsAgent {
			cands = append(cands, candidate{pid, h, inspect(ctx, h.proc)})
		}
	}
	// Transcripts named by --resume are claimed first; among the others the
	// newest process gets the newest transcript.
	sort.Slice(cands, func(i, j int) bool {
		ri, rj := sessionIDFromArgs(cands[i].info.Cmdline) != "", sessionIDFromArgs(cands[j].info.Cmdline) != ""
		if ri != rj {
			return ri
		}
		return cands[i].info.Started.After(cands[j].info.Started)
	})
	claimed := map[string]bool{}
	var agents []Agent
	for _, c := range cands {
		info := c.info
		ag := Agent{
			PID: c.pid, Name: c.hit.name, Project: a.matcher.match(info.Cwd), Cwd: info.Cwd,
			Started: info.Started, Cmdline: commandLine(info.Cmdline),
		}
		if t, err := c.hit.proc.TimesWithContext(ctx); err == nil {
			sample := cpuSample{total: t.User + t.System, at: now()}
			if prev, ok := a.cpu[c.pid]; ok && sample.at.After(prev.at) {
				ag.CPU = (sample.total - prev.total) / sample.at.Sub(prev.at).Seconds() * 100
			}
			cpuNow[c.pid] = sample
		}
		if info.Cwd != "" {
			ag.Branch, ag.Worktree = a.place(ctx, info.Cwd, now(), places)
			if path := findTranscript(transcriptDir(dir, info.Cwd), info.Cmdline, info.Started, claimed); path != "" {
				claimed[path] = true
				if p, ok := a.sessions.read(path); ok {
					s := p.session(now(), ag.CPU)
					ag.Session = &s
				}
			}
		}
		agents = append(agents, ag)
	}
	a.cpu, a.places = cpuNow, places
	sort.Slice(agents, func(i, j int) bool {
		if !agents[i].Started.Equal(agents[j].Started) {
			return agents[i].Started.Before(agents[j].Started)
		}
		return agents[i].PID < agents[j].PID
	})
	return AgentsSnapshot{Agents: agents}, nil
}
