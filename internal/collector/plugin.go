package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/otuletta/atalaya/internal/config"
)

// Plugin row statuses.
const (
	PluginOK    = "ok"
	PluginWarn  = "warn"
	PluginError = "error"
	PluginInfo  = "info"
)

// PluginRow is one line of a plugin's table.
type PluginRow struct {
	Cells   []string `json:"cells"`
	Status  string   `json:"status,omitempty"`
	URL     string   `json:"url,omitempty"`
	Project string   `json:"project,omitempty"`
	Detail  string   `json:"detail,omitempty"`
}

// PluginSnapshot is what a plugin prints on stdout. The same shape is the
// protocol: any program that prints it is a source.
type PluginSnapshot struct {
	Name    string      `json:"name"`
	Title   string      `json:"title"`
	Summary string      `json:"summary,omitempty"`
	Columns []string    `json:"columns"`
	Rows    []PluginRow `json:"rows"`
}

// maxPluginOutput and maxPluginRows keep a misbehaving plugin from eating
// memory or the screen.
const (
	maxPluginOutput = 1 << 20
	maxPluginRows   = 200
)

// Plugin runs an external command and reads its table. The command gets
// the projects in $ATALAYA_PROJECTS (JSON), so it can look at each one.
type Plugin struct {
	Plugin   config.Plugin
	Projects []config.Project
}

// PluginSource is the store key of a plugin.
func PluginSource(name string) string { return "plugin:" + name }

func (p *Plugin) Name() string            { return PluginSource(p.Plugin.Name) }
func (p *Plugin) Interval() time.Duration { return p.Plugin.Interval.Duration }

type pluginProject struct {
	Name   string   `json:"name"`
	Path   string   `json:"path,omitempty"`
	GitHub string   `json:"github,omitempty"`
	Health []string `json:"health,omitempty"`
}

func (p *Plugin) Collect(ctx context.Context) (Snapshot, error) {
	if len(p.Plugin.Command) == 0 {
		return nil, errors.New("plugin sin command")
	}
	projects := make([]pluginProject, len(p.Projects))
	for i, pr := range p.Projects {
		projects[i] = pluginProject{pr.Name, pr.Path, pr.GitHub, pr.Health}
	}
	env, _ := json.Marshal(projects)
	cmd := exec.CommandContext(ctx, p.Plugin.Command[0], p.Plugin.Command[1:]...)
	cmd.Env = append(os.Environ(), "ATALAYA_PROJECTS="+string(env))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitedWriter{w: &stdout, n: maxPluginOutput}
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// The source name (plugin:<name>) already says which plugin.
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			line, _, _ := strings.Cut(msg, "\n")
			return nil, errors.New(line)
		}
		return nil, err
	}
	return parsePluginOutput(p.Plugin.Name, stdout.Bytes())
}

// parsePluginOutput reads and normalizes a plugin's JSON.
func parsePluginOutput(name string, out []byte) (PluginSnapshot, error) {
	var s PluginSnapshot
	if err := json.Unmarshal(out, &s); err != nil {
		return s, fmt.Errorf("la salida no es el JSON esperado: %w", err)
	}
	s.Name = name
	if s.Title == "" {
		s.Title = name
	}
	if len(s.Rows) > maxPluginRows {
		s.Rows = s.Rows[:maxPluginRows]
	}
	if s.Rows == nil {
		s.Rows = []PluginRow{}
	}
	width := len(s.Columns)
	for i := range s.Rows {
		r := &s.Rows[i]
		switch r.Status {
		case PluginOK, PluginWarn, PluginError, PluginInfo:
		default:
			r.Status = PluginInfo
		}
		width = max(width, len(r.Cells))
	}
	for len(s.Columns) < width {
		s.Columns = append(s.Columns, "")
	}
	return s, nil
}

// limitedWriter keeps the first n bytes and drops the rest.
type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	total := len(p)
	if l.n <= 0 {
		return total, nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	n, err := l.w.Write(p)
	l.n -= n
	if err != nil {
		return n, err
	}
	return total, nil
}
