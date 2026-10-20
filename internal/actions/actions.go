// Package actions implements what the keys o, p and x do. Destructive
// actions (pull, kill) are only reached after the UI asked for confirmation.
package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/otuletta/atalaya/internal/ai"
)

// terminalEditors take over the terminal, so the TUI must be suspended
// while they run.
var terminalEditors = map[string]bool{
	"vi": true, "vim": true, "nvim": true, "nano": true, "hx": true, "helix": true,
	"micro": true, "kak": true, "emacs": true, "mg": true, "joe": true,
}

// guiEditors are tried, in order, when nothing is configured.
var guiEditors = []string{"cursor", "code", "zed", "subl"}

// EditorCommand builds the command that opens dir. interactive is true for
// terminal editors.
func EditorCommand(configured, dir string) (cmd *exec.Cmd, interactive bool) {
	editor := configured
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if editor == "" {
			editor = os.Getenv(env)
		}
	}
	if editor == "" {
		for _, e := range guiEditors {
			if _, err := exec.LookPath(e); err == nil {
				editor = e
				break
			}
		}
	}
	if editor == "" {
		return openCmd(dir), false
	}
	f := strings.Fields(editor)
	cmd = exec.Command(f[0], append(f[1:], dir)...)
	cmd.Dir = dir
	return cmd, terminalEditors[filepath.Base(f[0])]
}

func openCmd(target string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", target)
	}
	return exec.Command("xdg-open", target)
}

// OpenURL opens a URL in the default browser.
func OpenURL(url string) error {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return fmt.Errorf("URL no válida: %q", url)
	}
	return openCmd(url).Start()
}

// Start launches a GUI command without waiting for it.
func Start(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it
	return nil
}

// Pull runs a fast-forward-only pull: it never creates merge commits or
// leaves a conflicted tree behind. It returns git's last line of output.
func Pull(ctx context.Context, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "pull", "--ff-only")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	out, err := cmd.CombinedOutput()
	msg := lastLine(string(out))
	if err != nil {
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return msg, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// ErrProcessChanged means the PID now belongs to a different process.
var ErrProcessChanged = errors.New("el proceso ya no existe (el PID se reutilizó)")

// Kill sends SIGTERM to pid, but only if it is still the process that was
// shown to the user: started is its creation time from the snapshot, so a
// recycled PID is never killed by mistake.
func Kill(ctx context.Context, pid int32, started time.Time) error {
	if pid <= 1 || int(pid) == os.Getpid() {
		return fmt.Errorf("me niego a matar el PID %d", pid)
	}
	p, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return ErrProcessChanged
	}
	if !started.IsZero() {
		ms, err := p.CreateTimeWithContext(ctx)
		if err != nil || ms != started.UnixMilli() {
			return ErrProcessChanged
		}
	}
	return p.SendSignalWithContext(ctx, syscall.SIGTERM)
}

// System implements the UI's actions with the functions above.
type System struct {
	// EditorSetting is the `editor` from the config.
	EditorSetting string
	// Assistant runs Claude Code for a and c.
	Assistant ai.Assistant
}

// failedLog checks the CLI first (no point downloading a log it cannot
// read) and then fetches the failed steps' log.
func (s System) failedLog(ctx context.Context, f ai.Failure) (string, error) {
	if err := s.Assistant.Available(); err != nil {
		return "", err
	}
	run, ok := ai.ParseRunURL(f.RunURL)
	if !ok {
		return "", errors.New("solo sé leer logs de GitHub Actions")
	}
	return ai.FailedLog(ctx, run)
}

// Diagnose asks Claude why the CI failed.
func (s System) Diagnose(ctx context.Context, f ai.Failure) (string, error) {
	log, err := s.failedLog(ctx, f)
	if err != nil {
		return "", err
	}
	return s.Assistant.Diagnose(ctx, f, log)
}

// Fix prepares an interactive Claude Code session to fix the CI.
func (s System) Fix(ctx context.Context, f ai.Failure) (*exec.Cmd, error) {
	log, err := s.failedLog(ctx, f)
	if err != nil {
		return nil, err
	}
	return s.Assistant.FixCommand(f, log)
}

func (s System) Editor(dir string) (*exec.Cmd, bool) { return EditorCommand(s.EditorSetting, dir) }
func (System) Start(cmd *exec.Cmd) error             { return Start(cmd) }
func (System) OpenURL(url string) error              { return OpenURL(url) }
func (System) Pull(ctx context.Context, dir string) (string, error) {
	return Pull(ctx, dir)
}
func (System) Kill(ctx context.Context, pid int32, started time.Time) error {
	return Kill(ctx, pid, started)
}
