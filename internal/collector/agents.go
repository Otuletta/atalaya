package collector

import (
	"context"
	"path"
	"path/filepath"
	"sort"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/otuletta/atalaya/internal/config"
)

// Agent is a running coding-agent session.
type Agent struct {
	PID     int32     `json:"pid"`
	Name    string    `json:"name"`
	Project string    `json:"project,omitempty"`
	Cwd     string    `json:"cwd,omitempty"`
	Started time.Time `json:"started"`
	Cmdline string    `json:"cmdline,omitempty"`
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

	matcher *projectMatcher
}

func (a *Agents) Name() string            { return "agents" }
func (a *Agents) Interval() time.Duration { return a.Every }

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
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	type hit struct {
		proc *process.Process
		name string
		ppid int32
	}
	hits := map[int32]hit{}
	for _, p := range procs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		name, err := p.NameWithContext(ctx)
		if err != nil {
			continue
		}
		var argv []string
		matched := MatchAgent(a.Patterns, name, nil)
		if matched == "" && interpreters[name] {
			// Only interpreters pay for reading their argv.
			argv, _ = p.CmdlineSliceWithContext(ctx)
			matched = MatchAgent(a.Patterns, name, argv)
		}
		if matched == "" {
			continue
		}
		ppid, _ := p.PpidWithContext(ctx)
		hits[p.Pid] = hit{p, matched, ppid}
	}
	var agents []Agent
	for pid, h := range hits {
		// Sub-processes of an agent (tool runners, sub-agents) are part of
		// the parent session, not sessions of their own.
		if _, parentIsAgent := hits[h.ppid]; parentIsAgent {
			continue
		}
		info := inspect(ctx, h.proc)
		agents = append(agents, Agent{
			PID: pid, Name: h.name, Project: a.matcher.match(info.Cwd), Cwd: info.Cwd,
			Started: info.Started, Cmdline: commandLine(info.Cmdline),
		})
	}
	sort.Slice(agents, func(i, j int) bool {
		if !agents[i].Started.Equal(agents[j].Started) {
			return agents[i].Started.Before(agents[j].Started)
		}
		return agents[i].PID < agents[j].PID
	})
	return AgentsSnapshot{Agents: agents}, nil
}
