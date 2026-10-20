//go:build measure

package collector

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/config"
)

// TestMeasure prints what each source costs with your config: CPU per
// collection (this process and the commands it runs) and per minute at
// its interval. It runs the real sources, GitHub included:
//
//	go test -tags measure -run TestMeasure -v ./internal/collector
func TestMeasure(t *testing.T) {
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	iv := cfg.Intervals
	cs := []Collector{
		&Git{Projects: cfg.Projects, Every: iv.Git.Duration},
		&Ports{Projects: cfg.Projects, Ignore: cfg.Ports.Ignore, ShowEphemeral: cfg.Ports.ShowEphemeral, Every: iv.Ports.Duration},
		&Agents{Projects: cfg.Projects, Patterns: cfg.Agents.Patterns, Every: iv.Agents.Duration},
		&Docker{Projects: cfg.Projects, Every: iv.Docker.Duration},
		&Ollama{Host: cfg.OllamaHost, Every: iv.Ollama.Duration},
		&Usage{Projects: cfg.Projects, Every: iv.Usage.Duration, StatePath: filepath.Join(t.TempDir(), "usage.json")},
		&Health{Projects: cfg.Projects, Every: iv.Health.Duration},
		&GitHub{Projects: cfg.Projects, Every: iv.GitHub.Duration},
	}
	for _, p := range cfg.Plugins {
		cs = append(cs, &Plugin{Plugin: p, Projects: cfg.Projects})
	}
	var total float64
	for _, c := range cs {
		_, _ = c.Collect(context.Background()) // the first one fills the caches
		const n = 4
		c0, w0 := cpuTime(), time.Now()
		for range n {
			if _, err := c.Collect(context.Background()); err != nil {
				t.Logf("%s: %v", c.Name(), err)
			}
		}
		per := (cpuTime() - c0) / n
		perMin := per.Seconds() * float64(time.Minute) / float64(c.Interval())
		total += perMin
		fmt.Fprintf(os.Stdout, "%-20s every %-7s cpu %-8s wall %-8s → %.2fs of CPU per minute\n",
			c.Name(), c.Interval(), per.Round(time.Millisecond), (time.Since(w0) / n).Round(time.Millisecond), perMin)
	}
	fmt.Fprintf(os.Stdout, "total ≈ %.2fs of CPU per minute (idle repos cost less over time)\n", total)
}

func cpuTime() time.Duration {
	var self, children syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &children)
	d := func(t syscall.Timeval) time.Duration { return time.Duration(t.Nano()) }
	return d(self.Utime) + d(self.Stime) + d(children.Utime) + d(children.Stime)
}
