// Package config loads and validates ~/.config/atalaya/config.toml.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Project is one registered project.
type Project struct {
	Name string `toml:"name"`
	// Path is the local checkout. "~" is expanded on load.
	Path string `toml:"path"`
	// GitHub is "owner/repo". Optional.
	GitHub string `toml:"github"`
	// Health lists HTTP(S) endpoints to check. Optional.
	Health []string `toml:"health"`
}

// Agents configures the coding-agent detector.
type Agents struct {
	// Patterns are matched against executable names, case-insensitively.
	Patterns []string `toml:"patterns"`
}

// Ports configures the listening ports panel.
type Ports struct {
	// Ignore hides processes by name (macOS system daemons by default).
	Ignore []string `toml:"ignore"`
	// ShowEphemeral shows ports in the ephemeral range (49152+) that do not
	// belong to a project. They are hidden by default because they are noise.
	ShowEphemeral bool `toml:"show_ephemeral"`
}

// GitHubSettings configures the GitHub panel.
type GitHubSettings struct {
	// BotAuthors are grouped into one row per bot instead of one row per
	// PR, so a pile of dependency bumps does not bury the real work.
	BotAuthors []string `toml:"bot_authors"`
}

// Notify configures desktop notifications. Every switch defaults to on;
// they are pointers so a missing key can be told apart from false.
type Notify struct {
	Enabled *bool `toml:"enabled"`
	// CI notifies when a CI breaks or is fixed.
	CI *bool `toml:"ci"`
	// Health notifies when an endpoint goes down or recovers.
	Health *bool `toml:"health"`
	// Agents notifies when a coding agent finishes its turn and waits for
	// you, or seems stuck on a permission prompt.
	Agents *bool `toml:"agents"`
	// Reviews notifies when someone asks you for a review.
	Reviews *bool `toml:"reviews"`
}

func on(b *bool) bool { return b == nil || *b }

// WantCI reports whether CI transitions are notified.
func (n Notify) WantCI() bool { return on(n.Enabled) && on(n.CI) }

// WantHealth reports whether endpoint transitions are notified.
func (n Notify) WantHealth() bool { return on(n.Enabled) && on(n.Health) }

// WantAgents reports whether agent transitions are notified.
func (n Notify) WantAgents() bool { return on(n.Enabled) && on(n.Agents) }

// WantReviews reports whether new review requests are notified.
func (n Notify) WantReviews() bool { return on(n.Enabled) && on(n.Reviews) }

// Plugin is an external source: a command that prints a small JSON table
// (see examples/plugins). Each one gets a section in the Plugins panel.
type Plugin struct {
	Name    string   `toml:"name"`
	Command []string `toml:"command"`
	// Interval between runs. Default 60s.
	Interval Duration `toml:"interval"`
}

// AI configures the Claude Code integration (keys a and c).
type AI struct {
	// Command is the Claude Code CLI and fixed arguments. Default: claude.
	Command []string `toml:"command"`
}

// Intervals overrides how often each collector runs.
type Intervals struct {
	Git    Duration `toml:"git"`
	Fetch  Duration `toml:"fetch"`
	GitHub Duration `toml:"github"`
	Health Duration `toml:"health"`
	Ports  Duration `toml:"ports"`
	Agents Duration `toml:"agents"`
	Usage  Duration `toml:"usage"`
	Docker Duration `toml:"docker"`
	Ollama Duration `toml:"ollama"`
}

// Config is the whole file.
type Config struct {
	// Editor opens repos with `o`. Defaults to $VISUAL, then $EDITOR, then
	// the system's default app for folders.
	Editor string `toml:"editor"`
	// OllamaHost is the Ollama API address. Defaults to $OLLAMA_HOST or
	// http://localhost:11434.
	OllamaHost string         `toml:"ollama_host"`
	Projects   []Project      `toml:"project"`
	Agents     Agents         `toml:"agents"`
	Ports      Ports          `toml:"ports"`
	GitHub     GitHubSettings `toml:"github"`
	Notify     Notify         `toml:"notify"`
	AI         AI             `toml:"ai"`
	Plugins    []Plugin       `toml:"plugin"`
	Intervals  Intervals      `toml:"intervals"`

	// Path is where the config was loaded from.
	Path string `toml:"-"`
}

// Duration is a time.Duration written as "5s" or "10m" in TOML.
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Default intervals, from the design doc.
var defaultIntervals = Intervals{
	Git:    Duration{5 * time.Second},
	Fetch:  Duration{10 * time.Minute},
	GitHub: Duration{60 * time.Second},
	Health: Duration{30 * time.Second},
	Ports:  Duration{3 * time.Second},
	Agents: Duration{5 * time.Second},
	Usage:  Duration{time.Minute},
	Docker: Duration{10 * time.Second},
	Ollama: Duration{10 * time.Second},
}

// DefaultAgentPatterns are used when [agents] is missing.
var DefaultAgentPatterns = []string{"claude", "cline"}

// DefaultBotAuthors are used when [github] bot_authors is missing.
var DefaultBotAuthors = []string{"dependabot", "renovate", "github-actions"}

// DefaultIgnoredProcesses are macOS daemons that always listen on ports.
var DefaultIgnoredProcesses = []string{
	"rapportd", "ControlCenter", "ARDAgent", "sharingd", "identityservicesd", "WiFiAgent", "AirPlayXPCHelper",
}

// DefaultPath returns $XDG_CONFIG_HOME/atalaya/config.toml or
// ~/.config/atalaya/config.toml. macOS's ~/Library is deliberately not used:
// a dotfile is easier to find, edit and version.
func DefaultPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "atalaya", "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "atalaya", "config.toml"), nil
}

// StateDir is where Atalaya keeps what it learns, not what you edit: the
// activity log and the health history. $XDG_STATE_HOME/atalaya or
// ~/.local/state/atalaya.
func StateDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "atalaya"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "atalaya"), nil
}

// ErrNotFound is returned by Load when the file does not exist.
var ErrNotFound = errors.New("config not found")

// Load reads, defaults and validates the config at path.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Path = path
	return cfg, nil
}

// Parse decodes, defaults and validates a config.
func Parse(b []byte) (*Config, error) {
	var cfg Config
	md, err := toml.Decode(string(b), &cfg)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("unknown keys: %s", strings.Join(keys, ", "))
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	for i := range c.Projects {
		c.Projects[i].Path = ExpandHome(c.Projects[i].Path)
	}
	if c.Agents.Patterns == nil {
		c.Agents.Patterns = DefaultAgentPatterns
	}
	if c.Ports.Ignore == nil {
		c.Ports.Ignore = DefaultIgnoredProcesses
	}
	if c.GitHub.BotAuthors == nil {
		c.GitHub.BotAuthors = DefaultBotAuthors
	}
	for i := range c.Plugins {
		p := &c.Plugins[i]
		if p.Interval.Duration == 0 {
			p.Interval.Duration = time.Minute
		}
		// Without a shell nobody expands ~, in the program or in the
		// script it runs: `["python3", "~/plugins/audit.py"]`.
		for j, arg := range p.Command {
			p.Command[j] = ExpandHome(arg)
		}
	}
	if c.OllamaHost == "" {
		c.OllamaHost = os.Getenv("OLLAMA_HOST")
	}
	if c.OllamaHost == "" {
		c.OllamaHost = "http://localhost:11434"
	}
	if !strings.Contains(c.OllamaHost, "://") {
		c.OllamaHost = "http://" + c.OllamaHost
	}
	d := &c.Intervals
	for _, p := range []struct{ v, def *Duration }{
		{&d.Git, &defaultIntervals.Git},
		{&d.Fetch, &defaultIntervals.Fetch},
		{&d.GitHub, &defaultIntervals.GitHub},
		{&d.Health, &defaultIntervals.Health},
		{&d.Ports, &defaultIntervals.Ports},
		{&d.Agents, &defaultIntervals.Agents},
		{&d.Usage, &defaultIntervals.Usage},
		{&d.Docker, &defaultIntervals.Docker},
		{&d.Ollama, &defaultIntervals.Ollama},
	} {
		if p.v.Duration == 0 {
			*p.v = *p.def
		}
	}
}

var (
	githubRepoRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	pluginNameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// Validate reports every problem at once, so a broken file can be fixed in
// one pass.
func (c *Config) Validate() error {
	var errs []error
	seen := map[string]bool{}
	for i, p := range c.Projects {
		where := fmt.Sprintf("project #%d", i+1)
		if p.Name != "" {
			where = fmt.Sprintf("project %q", p.Name)
		}
		switch {
		case p.Name == "":
			errs = append(errs, fmt.Errorf("%s: name is required", where))
		case seen[p.Name]:
			errs = append(errs, fmt.Errorf("%s: duplicate name", where))
		}
		seen[p.Name] = true
		if p.Path == "" && p.GitHub == "" && len(p.Health) == 0 {
			errs = append(errs, fmt.Errorf("%s: needs at least one of path, github or health", where))
		}
		if p.Path != "" && !filepath.IsAbs(p.Path) {
			errs = append(errs, fmt.Errorf("%s: path must be absolute or start with ~: %s", where, p.Path))
		}
		if p.GitHub != "" && !githubRepoRE.MatchString(p.GitHub) {
			errs = append(errs, fmt.Errorf("%s: github must be owner/repo: %q", where, p.GitHub))
		}
		for _, h := range p.Health {
			u, err := url.Parse(h)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s: health must be an http(s) URL: %q", where, h))
			}
		}
	}
	for _, p := range c.Agents.Patterns {
		if strings.TrimSpace(p) == "" {
			errs = append(errs, errors.New("agents: empty pattern"))
		}
	}
	for _, b := range c.GitHub.BotAuthors {
		if strings.TrimSpace(b) == "" {
			errs = append(errs, errors.New("github.bot_authors: empty author"))
		}
	}
	plugins := map[string]bool{}
	for i, p := range c.Plugins {
		where := fmt.Sprintf("plugin #%d", i+1)
		switch {
		case !pluginNameRE.MatchString(p.Name):
			errs = append(errs, fmt.Errorf("%s: name must be letters, digits, - or _: %q", where, p.Name))
		case plugins[p.Name]:
			errs = append(errs, fmt.Errorf("plugin %q: duplicate name", p.Name))
		}
		plugins[p.Name] = true
		if len(p.Command) == 0 || strings.TrimSpace(p.Command[0]) == "" {
			errs = append(errs, fmt.Errorf("plugin %q: command is required", p.Name))
		}
		if p.Interval.Duration < time.Second {
			errs = append(errs, fmt.Errorf("plugin %q: interval must be at least 1s", p.Name))
		}
	}
	d := c.Intervals
	for name, v := range map[string]Duration{
		"git": d.Git, "fetch": d.Fetch, "github": d.GitHub, "health": d.Health,
		"ports": d.Ports, "agents": d.Agents, "usage": d.Usage, "docker": d.Docker, "ollama": d.Ollama,
	} {
		if v.Duration < time.Second {
			errs = append(errs, fmt.Errorf("intervals.%s: must be at least 1s, got %s", name, v))
		}
	}
	return errors.Join(errs...)
}

// IsBot reports whether a PR author is a bot: any "…[bot]" account, or one
// of bot_authors with or without the suffix (GraphQL reports dependabot as
// "dependabot", REST as "dependabot[bot]").
func (c *Config) IsBot(login string) bool {
	name, isApp := strings.CutSuffix(strings.ToLower(login), "[bot]")
	if isApp {
		return true
	}
	for _, b := range c.GitHub.BotAuthors {
		if strings.EqualFold(strings.TrimSuffix(b, "[bot]"), name) {
			return true
		}
	}
	return false
}

// ProjectForPath returns the project whose path contains dir, preferring the
// deepest match (a project nested inside another).
func (c *Config) ProjectForPath(dir string) (Project, bool) {
	var best Project
	found := false
	for _, p := range c.Projects {
		if p.Path == "" {
			continue
		}
		if dir == p.Path || strings.HasPrefix(dir, p.Path+string(filepath.Separator)) {
			if !found || len(p.Path) > len(best.Path) {
				best, found = p, true
			}
		}
	}
	return best, found
}

// ExpandHome replaces a leading ~ with the home directory.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

// ContractHome replaces the home directory with ~ for display.
func ContractHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}
