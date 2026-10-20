package collector

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/otuletta/atalaya/internal/config"
)

// projectMatcher maps a working directory to the project that contains it.
// Paths are compared both as written and with symlinks resolved, because a
// process reports its real cwd (/private/tmp, not /tmp).
type projectMatcher struct {
	cfg  config.Config
	real config.Config
}

func newProjectMatcher(projects []config.Project) *projectMatcher {
	m := &projectMatcher{cfg: config.Config{Projects: projects}}
	for _, p := range projects {
		if p.Path == "" {
			continue
		}
		if r, err := filepath.EvalSymlinks(p.Path); err == nil {
			p.Path = r
		}
		m.real.Projects = append(m.real.Projects, p)
	}
	return m
}

func (m *projectMatcher) match(dir string) string {
	if dir == "" {
		return ""
	}
	if p, ok := m.cfg.ProjectForPath(dir); ok {
		return p.Name
	}
	if p, ok := m.real.ProjectForPath(dir); ok {
		return p.Name
	}
	return ""
}

// procInfo is what the ports and agents panels show about a process.
type procInfo struct {
	PID     int32
	PPID    int32
	Name    string
	Cmdline []string
	Cwd     string
	Started time.Time
}

// inspect reads the details of one process. Missing fields (a process that
// exited, or one we may not inspect) are left empty instead of failing.
func inspect(ctx context.Context, p *process.Process) procInfo {
	info := procInfo{PID: p.Pid}
	info.Name, _ = p.NameWithContext(ctx)
	info.PPID, _ = p.PpidWithContext(ctx)
	info.Cmdline, _ = p.CmdlineSliceWithContext(ctx)
	info.Cwd, _ = p.CwdWithContext(ctx)
	if ms, err := p.CreateTimeWithContext(ctx); err == nil {
		info.Started = time.UnixMilli(ms)
	}
	return info
}

// commandLine renders argv for display, trimmed to a sane length.
func commandLine(argv []string) string {
	s := strings.Join(argv, " ")
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
