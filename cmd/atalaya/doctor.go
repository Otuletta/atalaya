package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/otuletta/atalaya/internal/ai"
	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/summary"
)

// prompt prints a status for shell prompts and menu bars from the
// status.json of a running Atalaya, in milliseconds. Inside a project it
// is that project's status; elsewhere, the global counters. A stale file
// (Atalaya closed) prints nothing.
func prompt(format string, maxAge time.Duration, out io.Writer) error {
	dir, err := config.StateDir()
	if err != nil {
		return err
	}
	s, err := summary.Read(filepath.Join(dir, "status.json"))
	stale := err != nil || time.Since(s.UpdatedAt) > maxAge
	switch format {
	case "json":
		if stale {
			_, err := fmt.Fprintln(out, `{"stale": true}`)
			return err
		}
		return writeJSON(out, s)
	case "swiftbar":
		self, _ := os.Executable()
		if stale {
			_, err := fmt.Fprintf(out, "◆ ?\n---\nAtalaya no está abierto\nAbrir Atalaya | bash=%s terminal=true\n", self)
			return err
		}
		_, err := io.WriteString(out, s.SwiftBar(self))
		return err
	}
	if stale {
		return nil
	}
	line := s.Prompt()
	if cwd, err := os.Getwd(); err == nil {
		if p, ok := s.ProjectFor(cwd); ok {
			line = p.Prompt()
		}
	}
	if line != "" {
		_, err = fmt.Fprintln(out, line)
	}
	return err
}

// check is one line of `atalaya doctor`.
type check struct {
	level string // ok, warn (optional feature), fail (core)
	what  string
	note  string
}

// doctor checks everything Atalaya depends on and says how to fix what is
// missing. Only core problems (config, git) make it fail.
func doctor(ctx context.Context, path string, out io.Writer) error {
	var checks []check
	add := func(level, what, note string) { checks = append(checks, check{level, what, note}) }

	cfg, err := config.Load(path)
	switch {
	case errors.Is(err, config.ErrNotFound):
		add("fail", "config", "no existe "+config.ContractHome(path)+": créala con atalaya init ~/tus/proyectos")
	case err != nil:
		add("fail", "config", err.Error())
	default:
		add("ok", "config", fmt.Sprintf("%s · %d proyectos", config.ContractHome(path), len(cfg.Projects)))
	}

	if out, err := exec.CommandContext(ctx, "git", "version").Output(); err != nil {
		add("fail", "git", "no está instalado")
	} else {
		v := strings.TrimSpace(string(out))
		if gitTooOld(v) {
			add("fail", "git", v+": hace falta 2.35 o más (git status --show-stash)")
		} else {
			add("ok", "git", v)
		}
	}
	if cfg != nil {
		for _, p := range cfg.Projects {
			if p.Path == "" {
				continue
			}
			if _, err := os.Stat(filepath.Join(p.Path, ".git")); err != nil {
				add("warn", "proyecto "+p.Name, config.ContractHome(p.Path)+" no es un repo git")
			}
		}
	}

	switch _, err := exec.LookPath("gh"); {
	case err != nil:
		add("warn", "GitHub", "falta la CLI gh (brew install gh): sin ella no hay panel de GitHub")
	case cfg != nil:
		checks = append(checks, githubCheck(ctx, cfg))
	}

	if _, err := exec.LookPath("docker"); err != nil {
		add("warn", "Docker", "no está instalado (opcional)")
	} else {
		add("ok", "Docker", "instalado")
	}
	if cfg != nil {
		o := &collector.Ollama{Host: cfg.OllamaHost}
		if snap, err := o.Collect(ctx); err == nil && snap.(collector.OllamaSnapshot).Available {
			add("ok", "Ollama", cfg.OllamaHost)
		} else {
			add("warn", "Ollama", "no está corriendo en "+cfg.OllamaHost+" (opcional)")
		}
		if err := (ai.Assistant{Command: cfg.AI.Command}).Available(); err != nil {
			add("warn", "Claude Code", "sin la CLI, a y c no funcionan (https://docs.claude.com/claude-code)")
		} else {
			add("ok", "Claude Code", "CLI disponible para a y c")
		}
		for _, p := range cfg.Plugins {
			if _, err := exec.LookPath(p.Command[0]); err != nil {
				add("warn", "plugin "+p.Name, "no encuentro "+p.Command[0])
			} else {
				add("ok", "plugin "+p.Name, strings.Join(p.Command, " "))
			}
		}
	}
	notifier := map[string]string{"darwin": "osascript", "linux": "notify-send"}[runtime.GOOS]
	if _, err := exec.LookPath(notifier); notifier == "" || err != nil {
		add("warn", "notificaciones", "no hay "+notifier+" para avisos del sistema")
	} else {
		add("ok", "notificaciones", notifier)
	}
	if dir, err := config.StateDir(); err != nil || os.MkdirAll(dir, 0o755) != nil {
		add("fail", "estado", "no puedo escribir la carpeta de estado")
	} else {
		add("ok", "estado", config.ContractHome(dir))
	}

	icons := map[string]string{"ok": "✓", "warn": "!", "fail": "✗"}
	failed := 0
	for _, c := range checks {
		if c.level == "fail" {
			failed++
		}
		fmt.Fprintf(out, "%s %-16s %s\n", icons[c.level], c.what, c.note)
	}
	if failed > 0 {
		return fmt.Errorf("%d problema(s) que impiden usar Atalaya", failed)
	}
	return nil
}

// githubCheck runs the real GitHub query once: it proves the token works
// and that every repo in the config is reachable.
func githubCheck(ctx context.Context, cfg *config.Config) check {
	n := 0
	for _, p := range cfg.Projects {
		if p.GitHub != "" {
			n++
		}
	}
	if n == 0 {
		return check{"ok", "GitHub", "ningún proyecto con github = \"owner/repo\""}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	snap, err := (&collector.GitHub{Projects: cfg.Projects}).Collect(ctx)
	if err != nil && snap == nil {
		return check{"warn", "GitHub", err.Error()}
	}
	s := snap.(collector.GitHubSnapshot)
	var bad []string
	for _, r := range s.Repos {
		if r.Err != "" {
			bad = append(bad, r.Repo)
		}
	}
	if len(bad) > 0 {
		return check{"warn", "GitHub", fmt.Sprintf("@%s, sin acceso a: %s", s.Viewer, strings.Join(bad, ", "))}
	}
	return check{"ok", "GitHub", fmt.Sprintf("@%s · %d repos · API %d/%d", s.Viewer, n, s.RateLimit.Remaining, s.RateLimit.Limit)}
}

// gitTooOld reports a git older than 2.35 ("git version 2.34.1").
func gitTooOld(version string) bool {
	f := strings.Fields(version)
	if len(f) < 3 {
		return false
	}
	parts := strings.SplitN(f[2], ".", 3)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	return major < 2 || (major == 2 && minor < 35)
}
