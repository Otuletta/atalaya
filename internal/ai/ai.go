// Package ai connects a broken CI with Claude Code: `a` explains why a run
// failed and `c` opens Claude Code in the repo to fix it. It shells out to
// the `claude` CLI, so it reuses the user's Claude Code login and never
// handles API keys, the same way GitHub access reuses `gh`.
package ai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Run identifies a GitHub Actions run.
type Run struct {
	Repo string // owner/name
	ID   string
}

var runURL = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/actions/runs/(\d+)`)

// ParseRunURL extracts the repo and run id from a run's URL.
func ParseRunURL(u string) (Run, bool) {
	m := runURL.FindStringSubmatch(u)
	if m == nil {
		return Run{}, false
	}
	return Run{Repo: m[1], ID: m[2]}, true
}

// Failure describes a failing CI to explain or fix.
type Failure struct {
	Project  string
	Dir      string // local checkout
	Branch   string
	Workflow string
	RunURL   string
}

// maxLogLines keeps the end of the failed jobs' log, where the error is.
const maxLogLines = 200

// FailedLog downloads the log of the failed steps of a run with gh.
func FailedLog(ctx context.Context, run Run) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "run", "view", run.ID, "--repo", run.Repo, "--log-failed")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("gh run view: %s", firstLine(msg))
		}
		return "", fmt.Errorf("gh run view: %w", err)
	}
	return condense(string(out)), nil
}

// errorContext is how many lines before each ##[error] are kept: the
// failing command's output sits right above GitHub's error marker, while the
// end of the log is the runner cleaning up.
const errorContext = 80

var logPrefix = regexp.MustCompile(`^([^\t]*)\t[^\t]*\t\S+Z ?`)

// condense keeps what explains the failure: the lines before each error
// marker (or the end of the log if there is none), without the
// "job<TAB>step<TAB>timestamp" prefix of every line.
func condense(log string) string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	for i, l := range lines {
		if m := logPrefix.FindStringSubmatch(l); m != nil {
			lines[i] = m[1] + " | " + l[len(m[0]):]
		}
	}
	keep := make([]bool, len(lines))
	found := false
	for i, l := range lines {
		if strings.Contains(l, "##[error]") {
			found = true
			for j := max(i-errorContext, 0); j <= i; j++ {
				keep[j] = true
			}
		}
	}
	if !found {
		return tail(strings.Join(lines, "\n"), maxLogLines)
	}
	var out []string
	for i, l := range lines {
		if keep[i] {
			if i > 0 && !keep[i-1] && len(out) > 0 {
				out = append(out, "[…]")
			}
			out = append(out, l)
		}
	}
	return tail(strings.Join(out, "\n"), maxLogLines)
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{fmt.Sprintf("[… %d líneas anteriores omitidas]", len(lines)-n)}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// Assistant runs the Claude Code CLI.
type Assistant struct {
	// Command is the CLI and any fixed arguments, from [ai] command.
	Command []string
}

// ErrNoCLI explains how to get the CLI when it is missing.
var ErrNoCLI = errors.New("no encuentro la CLI de Claude Code: instálala (https://docs.claude.com/claude-code) o define [ai] command")

func (a Assistant) bin() (string, []string, error) {
	cmd := a.Command
	if len(cmd) == 0 {
		cmd = []string{"claude"}
	}
	path, err := exec.LookPath(cmd[0])
	if err != nil {
		return "", nil, ErrNoCLI
	}
	return path, cmd[1:], nil
}

// Available reports whether the CLI can be run.
func (a Assistant) Available() error {
	_, _, err := a.bin()
	return err
}

func diagnosePrompt(f Failure) string {
	return fmt.Sprintf("Por la entrada estándar te paso el log de los pasos que fallaron en el CI de %s "+
		"(rama %s, workflow %s, %s). Explica en español, en 6 líneas como máximo y sin preámbulo, "+
		"la causa más probable y cómo arreglarla. Puedes leer el código del repo para confirmarlo.",
		f.Project, f.Branch, f.Workflow, f.RunURL)
}

// Diagnose asks Claude, in print mode and with read-only tools, why the run
// failed. It runs in the repo so Claude can look at the code.
func (a Assistant) Diagnose(ctx context.Context, f Failure, log string) (string, error) {
	bin, args, err := a.bin()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	args = append(args, "-p", diagnosePrompt(f), "--allowedTools", "Read,Grep,Glob")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = f.Dir
	cmd.Stdin = strings.NewReader(log)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", errors.New("claude tardó demasiado en responder")
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("claude: %s", firstLine(msg))
		}
		return "", fmt.Errorf("claude: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// FixCommand opens an interactive Claude Code session in the repo, primed
// with the failure. The log goes to a file the prompt points at: a CI log
// does not fit in a command line.
func (a Assistant) FixCommand(f Failure, log string) (*exec.Cmd, error) {
	bin, args, err := a.bin()
	if err != nil {
		return nil, err
	}
	file, err := os.CreateTemp("", "atalaya-ci-*.log")
	if err != nil {
		return nil, err
	}
	if _, err := file.WriteString(log); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	prompt := fmt.Sprintf("El CI de %s falla en la rama %s (workflow %s, %s). "+
		"El log de los pasos que fallaron está en %s. Encuentra la causa y arréglala; "+
		"antes de cambiar nada, explícame el plan.", f.Project, f.Branch, f.Workflow, f.RunURL, file.Name())
	cmd := exec.Command(bin, append(args, prompt)...)
	cmd.Dir = f.Dir
	return cmd, nil
}
