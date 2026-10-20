//go:build !darwin

package collector

import (
	"context"

	"github.com/shirou/gopsutil/v4/process"
)

// allProcs lists every process with its name; the parent is read later,
// only for the processes that matter.
func allProcs(ctx context.Context) ([]procEntry, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]procEntry, 0, len(procs))
	for _, p := range procs {
		if name, err := p.NameWithContext(ctx); err == nil {
			out = append(out, procEntry{pid: p.Pid, ppid: -1, name: name})
		}
	}
	return out, nil
}
