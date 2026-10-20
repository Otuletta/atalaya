package config

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const example = `
[[project]]
name = "odomto"
path = "~/dev/odomto"
github = "otuletta/odomto"
health = ["https://api.odomto.com/health"]

[[project]]
name = "kdx"
path = "~/dev/kdx"

[agents]
patterns = ["claude", "cline"]
`

func TestParseExample(t *testing.T) {
	cfg, err := Parse([]byte(example))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Projects) != 2 {
		t.Fatalf("projects = %d", len(cfg.Projects))
	}
	home, _ := os.UserHomeDir()
	if got, want := cfg.Projects[0].Path, filepath.Join(home, "dev/odomto"); got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	if cfg.Projects[0].GitHub != "otuletta/odomto" || len(cfg.Projects[0].Health) != 1 {
		t.Errorf("project = %+v", cfg.Projects[0])
	}
	if cfg.Intervals.Git.Duration != 5*time.Second || cfg.Intervals.GitHub.Duration != time.Minute {
		t.Errorf("default intervals not applied: %+v", cfg.Intervals)
	}
	if len(cfg.Ports.Ignore) == 0 {
		t.Error("default ignored processes not applied")
	}
}

func TestIntervalsOverride(t *testing.T) {
	cfg, err := Parse([]byte("[intervals]\ngit = \"2s\"\nfetch = \"30m\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Intervals.Git.Duration != 2*time.Second || cfg.Intervals.Fetch.Duration != 30*time.Minute {
		t.Errorf("intervals = %+v", cfg.Intervals)
	}
	if cfg.Intervals.Ports.Duration != 3*time.Second {
		t.Errorf("unset interval lost its default: %v", cfg.Intervals.Ports)
	}
}

func TestValidateReportsEverything(t *testing.T) {
	_, err := Parse([]byte(`
[[project]]
path = "relative/path"
github = "not a repo"
health = ["ftp://nope", "https://ok.example.com"]

[[project]]
name = "a"

[[project]]
name = "a"
path = "/tmp"

[intervals]
ports = "10ms"
`))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"name is required",
		"path must be absolute",
		"github must be owner/repo",
		`health must be an http(s) URL: "ftp://nope"`,
		"needs at least one of path, github or health",
		"duplicate name",
		"intervals.ports: must be at least 1s",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "ok.example.com") {
		t.Error("valid URL reported as invalid")
	}
}

func TestBotAuthors(t *testing.T) {
	cfg, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	for login, want := range map[string]bool{
		"dependabot": true, "dependabot[bot]": true, "Renovate": true, "github-actions": true,
		"some-app[bot]": true, "otuletta": false, "dependabot-fan": false, "": false,
	} {
		if got := cfg.IsBot(login); got != want {
			t.Errorf("IsBot(%q) = %v, want %v", login, got, want)
		}
	}

	cfg, err = Parse([]byte("[github]\nbot_authors = [\"mergify\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsBot("mergify") || cfg.IsBot("dependabot") {
		t.Error("bot_authors does not replace the defaults")
	}
	if !cfg.IsBot("whatever[bot]") {
		t.Error("[bot] accounts are always bots")
	}

	if _, err := Parse([]byte("[github]\nbot_authors = [\"\"]\n")); err == nil || !strings.Contains(err.Error(), "bot_authors") {
		t.Errorf("empty bot author: err = %v", err)
	}
}

func TestNotify(t *testing.T) {
	tests := map[string]struct{ ci, health bool }{
		"":                                    {true, true},
		"[notify]\nenabled = false\n":         {false, false},
		"[notify]\nci = false\n":              {false, true},
		"[notify]\nhealth = false\n":          {true, false},
		"[notify]\nenabled = true\nci = true": {true, true},
	}
	for in, want := range tests {
		cfg, err := Parse([]byte(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if cfg.Notify.WantAgents() != (want.ci || want.health) {
			t.Errorf("%q: agents=%v", in, cfg.Notify.WantAgents())
		}
		if cfg.Notify.WantCI() != want.ci || cfg.Notify.WantHealth() != want.health {
			t.Errorf("%q: ci=%v health=%v, want %+v", in, cfg.Notify.WantCI(), cfg.Notify.WantHealth(), want)
		}
	}
	if _, err := Parse([]byte("[notify]\nenabled = \"sí\"\n")); err == nil {
		t.Error("a non-boolean enabled was accepted")
	}
}

func TestPlugins(t *testing.T) {
	cfg, err := Parse([]byte(`
[[plugin]]
name = "disk"
command = ["~/bin/disk.sh", "--json"]

[[plugin]]
name = "npm-audit"
command = ["python3", "~/plugins/audit.py"]
interval = "30m"
`))
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if p := cfg.Plugins[0]; p.Command[0] != filepath.Join(home, "bin/disk.sh") || p.Interval.Duration != time.Minute {
		t.Errorf("plugin 0 = %+v", p)
	}
	if p := cfg.Plugins[1]; p.Interval.Duration != 30*time.Minute || p.Command[1] != filepath.Join(home, "plugins/audit.py") {
		t.Errorf("plugin 1 = %+v", p)
	}
	_, err = Parse([]byte(`
[[plugin]]
name = "con espacio"
command = []

[[plugin]]
name = "x"
command = ["a"]
interval = "1ms"

[[plugin]]
name = "x"
command = ["b"]
`))
	for _, want := range []string{"name must be letters", "command is required", "interval must be at least 1s", "duplicate name"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestUnknownKeys(t *testing.T) {
	_, err := Parse([]byte("[[project]]\nname = \"x\"\npath = \"/x\"\nhelth = [\"https://x\"]\n"))
	if err == nil || !strings.Contains(err.Error(), "project.helth") {
		t.Errorf("err = %v, want unknown key project.helth", err)
	}
}

func TestLoadNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestOllamaHost(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	cfg, _ := Parse(nil)
	if cfg.OllamaHost != "http://localhost:11434" {
		t.Errorf("default = %q", cfg.OllamaHost)
	}
	t.Setenv("OLLAMA_HOST", "0.0.0.0:9999")
	cfg, _ = Parse(nil)
	if cfg.OllamaHost != "http://0.0.0.0:9999" {
		t.Errorf("from env = %q", cfg.OllamaHost)
	}
}

func TestProjectForPath(t *testing.T) {
	cfg := &Config{Projects: []Project{
		{Name: "outer", Path: "/w/outer"},
		{Name: "inner", Path: "/w/outer/packages/inner"},
		{Name: "remote-only", GitHub: "a/b"},
	}}
	tests := map[string]string{
		"/w/outer":                        "outer",
		"/w/outer/src":                    "outer",
		"/w/outer/packages/inner/x":       "inner",
		"/w/outerside":                    "",
		"/elsewhere":                      "",
		"/w/outer/packages/inner-sibling": "outer",
	}
	for dir, want := range tests {
		p, ok := cfg.ProjectForPath(dir)
		if got := p.Name; got != want || ok != (want != "") {
			t.Errorf("ProjectForPath(%q) = %q, %v; want %q", dir, got, ok, want)
		}
	}
}

func TestParseGitHubRemote(t *testing.T) {
	tests := map[string]string{
		"git@github.com:otuletta/atalaya.git":       "otuletta/atalaya",
		"https://github.com/otuletta/atalaya.git":   "otuletta/atalaya",
		"https://github.com/otuletta/atalaya":       "otuletta/atalaya",
		"https://token@github.com/o-t/my.repo.git":  "o-t/my.repo",
		"ssh://git@github.com/otuletta/atalaya.git": "otuletta/atalaya",
		"https://gitlab.com/otuletta/atalaya.git":   "",
		"git@github.com:otuletta/atalaya/extra.git": "",
		"": "",
	}
	for in, want := range tests {
		if got := ParseGitHubRemote(in); got != want {
			t.Errorf("ParseGitHubRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func gitInit(t *testing.T, dir, origin string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if origin != "" {
		run("remote", "add", "origin", origin)
	}
}

func TestInitCreatesThenAppends(t *testing.T) {
	root := t.TempDir()
	gitInit(t, filepath.Join(root, "alpha"), "git@github.com:me/alpha.git")
	gitInit(t, filepath.Join(root, "group", "beta"), "")
	gitInit(t, filepath.Join(root, "node_modules", "skipme"), "")
	gitInit(t, filepath.Join(root, ".hidden", "skipme"), "")
	gitInit(t, filepath.Join(root, "a", "b", "c", "toodeep"), "")

	cfgPath := filepath.Join(t.TempDir(), "atalaya", "config.toml")
	res, err := Init(context.Background(), cfgPath, root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Created || len(res.Added) != 2 {
		t.Fatalf("result = %+v", res)
	}
	cfg, err := Load(cfgPath)
	if err != nil {
		t.Fatalf("generated config does not load: %v", err)
	}
	if cfg.Projects[0].Name != "alpha" || cfg.Projects[0].GitHub != "me/alpha" {
		t.Errorf("alpha = %+v", cfg.Projects[0])
	}
	if cfg.Projects[1].Name != "beta" || cfg.Projects[1].GitHub != "" {
		t.Errorf("beta = %+v", cfg.Projects[1])
	}

	// A second run with a new repo appends it and keeps the rest untouched.
	before, _ := os.ReadFile(cfgPath)
	gitInit(t, filepath.Join(root, "gamma"), "https://github.com/me/gamma")
	res, err = Init(context.Background(), cfgPath, root, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Created || len(res.Added) != 1 || res.Skipped != 2 {
		t.Fatalf("second result = %+v", res)
	}
	after, _ := os.ReadFile(cfgPath)
	if !strings.HasPrefix(string(after), string(before)) {
		t.Error("existing content was rewritten instead of appended to")
	}
	cfg, err = Load(cfgPath)
	if err != nil {
		t.Fatalf("appended config does not load: %v", err)
	}
	if len(cfg.Projects) != 3 || cfg.Projects[2].GitHub != "me/gamma" {
		t.Errorf("projects = %+v", cfg.Projects)
	}
}

func TestDedupeNames(t *testing.T) {
	repos := []FoundRepo{{Name: "api", Path: "/w/one/api"}, {Name: "api", Path: "/w/two/api"}, {Name: "web", Path: "/w/web"}}
	dedupeNames(repos)
	if repos[0].Name != "one/api" || repos[1].Name != "two/api" || repos[2].Name != "web" {
		t.Errorf("names = %+v", repos)
	}
}
