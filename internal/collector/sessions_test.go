package collector

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTranscriptDir(t *testing.T) {
	tests := map[string]string{
		"/Users/ada/Documents/Alba Mercedes":                  "-Users-ada-Documents-Alba-Mercedes",
		"/Users/ada/Documents/ENDONSYS II":                    "-Users-ada-Documents-ENDONSYS-II",
		"/Users/ada/Documents/Financiera/.claude/worktrees/x": "-Users-ada-Documents-Financiera--claude-worktrees-x",
	}
	for cwd, want := range tests {
		if got := filepath.Base(transcriptDir("/c", cwd)); got != want {
			t.Errorf("transcriptDir(%q) = %q, want %q", cwd, got, want)
		}
	}
}

func TestSessionIDFromArgs(t *testing.T) {
	tests := []struct {
		argv []string
		want string
	}{
		{[]string{"claude", "--resume=abc-123"}, "abc-123"},
		{[]string{"claude", "--resume", "abc-123", "--verbose"}, "abc-123"},
		{[]string{"claude", "--session-id=s1"}, "s1"},
		{[]string{"claude", "--resume", "--verbose"}, ""}, // interactive picker, no id
		{[]string{"claude", "--model=opus"}, ""},
	}
	for _, tt := range tests {
		if got := sessionIDFromArgs(tt.argv); got != tt.want {
			t.Errorf("sessionIDFromArgs(%q) = %q, want %q", tt.argv, got, tt.want)
		}
	}
}

// line builds one transcript entry.
func line(t *testing.T, typ string, msg map[string]any, extra map[string]any) string {
	t.Helper()
	e := map[string]any{"type": typ, "sessionId": "s-1", "timestamp": "2026-10-20T10:00:00Z", "isSidechain": false}
	if msg != nil {
		e["message"] = msg
	}
	for k, v := range extra {
		e[k] = v
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func assistant(stop string, blocks ...map[string]any) map[string]any {
	return map[string]any{"role": "assistant", "stop_reason": stop, "content": blocks}
}

func toolUse(name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "name": name, "input": input}
}

func TestParseTranscript(t *testing.T) {
	text := map[string]any{"type": "text", "text": "listo"}
	result := map[string]any{"role": "user", "content": []map[string]any{{"type": "tool_result"}}}
	tests := []struct {
		name        string
		lines       []string
		ended, tool bool
		activity    string
		title       string
		prNumber    int
	}{
		{name: "turn ended: waiting for you", ended: true, lines: []string{
			line(t, "user", map[string]any{"role": "user", "content": "arregla el CI"}, nil),
			line(t, "assistant", assistant("end_turn", text), nil),
			line(t, "custom-title", nil, map[string]any{"customTitle": "Arreglar CI"}),
			line(t, "system", nil, map[string]any{"subtype": "stop_hook_summary"}),
		}, title: "Arreglar CI"},
		{name: "a tool is pending", tool: true, activity: "editando model.go", lines: []string{
			line(t, "assistant", assistant("tool_use", text, toolUse("Edit", map[string]any{"file_path": "/w/internal/ui/model.go"})), nil),
			line(t, "attachment", nil, nil),
		}},
		{name: "a tool result went back: thinking", activity: "pensando", lines: []string{
			line(t, "assistant", assistant("tool_use", toolUse("Bash", map[string]any{"command": "go test ./..."})), nil),
			line(t, "user", result, nil),
		}},
		{name: "a sub-agent is the last to speak", activity: "subagente trabajando", lines: []string{
			line(t, "assistant", assistant("end_turn", text), nil),
			line(t, "assistant", assistant("tool_use", toolUse("Read", nil)), map[string]any{"isSidechain": true}),
		}},
		{name: "pr link and agent name", ended: true, title: "kdx-ci", prNumber: 12, lines: []string{
			line(t, "assistant", assistant("end_turn", text), nil),
			line(t, "agent-name", nil, map[string]any{"agentName": "kdx-ci"}),
			line(t, "pr-link", nil, map[string]any{"prNumber": 12, "prUrl": "https://github.com/a/b/pull/12"}),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := parseTranscript([]byte(strings.Join(tt.lines, "\n") + "\n"))
			if !p.foundLastMsg || p.ended != tt.ended || p.pendingTool != tt.tool || p.activity != tt.activity ||
				p.title != tt.title || p.prNumber != tt.prNumber || p.id != "s-1" {
				t.Errorf("parsed = %+v", p)
			}
		})
	}
}

func TestSessionState(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	pending := parsed{pendingTool: true, activity: "Bash: tests", modTime: now.Add(-time.Minute)}
	if s := pending.session(now, 0.5); s.State != SessionBlocked {
		t.Errorf("idle pending tool = %s, want blocked", s.State)
	}
	if s := pending.session(now, 40); s.State != SessionWorking {
		t.Errorf("busy pending tool = %s, want working (a long command)", s.State)
	}
	pending.modTime = now.Add(-5 * time.Second)
	if s := pending.session(now, 0); s.State != SessionWorking {
		t.Errorf("fresh pending tool = %s, want working", s.State)
	}
	if s := (parsed{ended: true}).session(now, 0); s.State != SessionWaiting {
		t.Errorf("ended turn = %s, want waiting", s.State)
	}
}

func TestDescribeTool(t *testing.T) {
	long := strings.Repeat("x", 100)
	tests := []struct {
		name string
		in   map[string]any
		want string
	}{
		{"Bash", map[string]any{"description": "Run tests", "command": "go test"}, "Bash: Run tests"},
		{"Bash", map[string]any{"command": "go   test\n./..."}, "Bash: go test ./..."},
		{"Write", map[string]any{"file_path": "/a/b/README.md"}, "escribiendo README.md"},
		{"Grep", map[string]any{"pattern": long}, "buscando " + strings.Repeat("x", 59) + "…"},
		{"WebFetch", map[string]any{"url": "https://go.dev/doc"}, "leyendo go.dev"},
		{"Agent", map[string]any{"description": "revisar"}, "subagente: revisar"},
		{"mcp__github__create_pr", nil, "mcp create_pr"},
		{"SomethingNew", nil, "SomethingNew"},
	}
	for _, tt := range tests {
		if got := describeTool(tt.name, tt.in); got != tt.want {
			t.Errorf("describeTool(%s) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestSessionReaderTailAndCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	// A huge first line (a big tool output) that falls outside the window.
	big := line(t, "user", map[string]any{"role": "user", "content": strings.Repeat("y", tailWindow+1000)}, nil)
	last := line(t, "assistant", assistant("end_turn", map[string]any{"type": "text"}), nil)
	if err := os.WriteFile(path, []byte(big+"\n"+last+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var r sessionReader
	p, ok := r.read(path)
	if !ok || !p.ended {
		t.Fatalf("parsed = %+v, %v", p, ok)
	}
	// Unchanged file: served from the cache even if it became unreadable.
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(path, 0o644) }()
	if p2, ok := r.read(path); !ok || !p2.ended {
		t.Errorf("cache miss on an unchanged transcript: %+v", p2)
	}
}

func TestFindTranscript(t *testing.T) {
	dir := t.TempDir()
	started := time.Now().Add(-time.Hour)
	write := func(name string, age time.Duration) string {
		p := filepath.Join(dir, name+".jsonl")
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-age)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		return p
	}
	old := write("old", 3*time.Hour) // before the process started
	a := write("a", 10*time.Minute)
	b := write("b", time.Minute)

	if got := findTranscript(dir, []string{"claude", "--resume=old"}, started, nil); got != old {
		t.Errorf("--resume = %q, want the named file even if old", got)
	}
	if got := findTranscript(dir, []string{"claude"}, started, map[string]bool{}); got != b {
		t.Errorf("newest = %q, want %q", got, b)
	}
	if got := findTranscript(dir, []string{"claude"}, started, map[string]bool{b: true}); got != a {
		t.Errorf("with b claimed = %q, want %q", got, a)
	}
	if got := findTranscript(dir, []string{"claude"}, started, map[string]bool{a: true, b: true}); got != "" {
		t.Errorf("only an old file left = %q, want none", got)
	}
	if got := findTranscript(filepath.Join(dir, "missing"), nil, started, nil); got != "" {
		t.Errorf("missing dir = %q", got)
	}
}

func TestAgentsReadTheirSession(t *testing.T) {
	cwd, _ := os.Getwd()
	claude := t.TempDir()
	tdir := transcriptDir(claude, cwd)
	if err := os.MkdirAll(tdir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := line(t, "assistant", assistant("end_turn", map[string]any{"type": "text"}), nil) + "\n" +
		line(t, "custom-title", nil, map[string]any{"customTitle": "Prueba"}) + "\n"
	if err := os.WriteFile(filepath.Join(tdir, "s-1.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	a := &Agents{Patterns: []string{filepath.Base(self)}, ClaudeDir: claude}
	for range 2 { // the second run also measures CPU
		snap, err := a.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, ag := range snap.(AgentsSnapshot).Agents {
			if ag.PID != int32(os.Getpid()) {
				continue
			}
			if ag.Session == nil || ag.Session.State != SessionWaiting || ag.Session.Title != "Prueba" {
				t.Fatalf("session = %+v", ag.Session)
			}
		}
	}
}
