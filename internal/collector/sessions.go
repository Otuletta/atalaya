package collector

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Session states of a coding agent.
const (
	// SessionWorking: the model is generating or a tool is running.
	SessionWorking = "working"
	// SessionWaiting: the turn ended; the agent waits for your answer.
	SessionWaiting = "waiting"
	// SessionBlocked: a tool call has been pending with no activity, which
	// usually means a permission prompt nobody has answered.
	SessionBlocked = "blocked"
)

// Session is what a Claude Code session is doing, read from its transcript.
type Session struct {
	ID       string    `json:"id"`
	Title    string    `json:"title,omitempty"`
	State    string    `json:"state"`
	Activity string    `json:"activity,omitempty"`
	LastAt   time.Time `json:"last_at"`
	PRNumber int       `json:"pr_number,omitempty"`
	PRURL    string    `json:"pr_url,omitempty"`
}

// blockedAfter is how long a pending tool call may sit without the
// transcript moving and with an idle process before it counts as blocked.
const blockedAfter = 30 * time.Second

// Claude Code keeps one JSONL transcript per session in
// <config>/projects/<cwd with every non-alphanumeric character as ->/.
// The format is internal to Claude Code: everything that depends on it
// lives in this file and degrades to "no session" when it does not match.
var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

func transcriptDir(claudeDir, cwd string) string {
	return filepath.Join(claudeDir, "projects", nonAlnum.ReplaceAllString(cwd, "-"))
}

// claudeDir is $CLAUDE_CONFIG_DIR or ~/.claude.
func claudeDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

var resumeRE = regexp.MustCompile(`^--(?:resume|session-id)(?:=(.+))?$`)

// sessionIDFromArgs finds `--resume <id>` or `--session-id=<id>`.
func sessionIDFromArgs(argv []string) string {
	for i, a := range argv {
		m := resumeRE.FindStringSubmatch(a)
		if m == nil {
			continue
		}
		if m[1] != "" {
			return m[1]
		}
		if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") {
			return argv[i+1]
		}
	}
	return ""
}

// findTranscript picks the transcript of a process: the one named by its
// --resume flag, else the most recently written one in its folder that has
// changed since the process started and that no other agent claimed.
func findTranscript(dir string, argv []string, started time.Time, claimed map[string]bool) string {
	if id := sessionIDFromArgs(argv); id != "" {
		p := filepath.Join(dir, id+".jsonl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var best string
	var bestAt time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil || claimed[p] || info.ModTime().Before(started.Add(-time.Minute)) {
			continue
		}
		if info.ModTime().After(bestAt) {
			best, bestAt = p, info.ModTime()
		}
	}
	return best
}

// transcriptEntry is the subset of a transcript line that matters here.
type transcriptEntry struct {
	Type        string    `json:"type"`
	IsSidechain bool      `json:"isSidechain"`
	Timestamp   time.Time `json:"timestamp"`
	SessionID   string    `json:"sessionId"`
	CustomTitle string    `json:"customTitle"`
	AgentName   string    `json:"agentName"`
	PRNumber    int       `json:"prNumber"`
	PRURL       string    `json:"prUrl"`
	Message     struct {
		StopReason string          `json:"stop_reason"`
		Content    json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type  string         `json:"type"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// parsed is what a transcript says, before time and CPU decide whether a
// pending tool call is still running.
type parsed struct {
	id, title    string
	prNumber     int
	prURL        string
	pendingTool  bool
	ended        bool
	activity     string
	lastAt       time.Time
	size         int64
	modTime      time.Time
	foundLastMsg bool
}

// sessionReader caches parsed transcripts by size and modification time,
// so idle sessions cost a stat every poll.
type sessionReader struct {
	mu    sync.Mutex
	cache map[string]parsed
}

// tailWindow is how much of the end of a transcript is read. A turn is
// rarely longer; when the last message is not in it, the state is unknown.
const tailWindow = 512 << 10

func (r *sessionReader) read(path string) (parsed, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return parsed{}, false
	}
	r.mu.Lock()
	if c, ok := r.cache[path]; ok && c.size == info.Size() && c.modTime.Equal(info.ModTime()) {
		r.mu.Unlock()
		return c, c.foundLastMsg
	}
	r.mu.Unlock()

	f, err := os.Open(path)
	if err != nil {
		return parsed{}, false
	}
	defer f.Close()
	start := max(info.Size()-tailWindow, 0)
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return parsed{}, false
	}
	if start > 0 { // drop the partial first line
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	p := parseTranscript(buf)
	p.size, p.modTime = info.Size(), info.ModTime()
	if p.id == "" {
		p.id = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]parsed{}
	}
	r.cache[path] = p
	r.mu.Unlock()
	return p, p.foundLastMsg
}

// parseTranscript walks the lines backwards: the last main-thread message
// decides the state; titles and PR links are the last ones seen.
func parseTranscript(buf []byte) parsed {
	var p parsed
	lines := bytes.Split(bytes.TrimRight(buf, "\n"), []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		var e transcriptEntry
		if json.Unmarshal(lines[i], &e) != nil {
			continue
		}
		if p.id == "" {
			p.id = e.SessionID
		}
		switch e.Type {
		case "custom-title":
			if p.title == "" {
				p.title = e.CustomTitle
			}
		case "agent-name":
			if p.title == "" {
				p.title = e.AgentName
			}
		case "pr-link":
			if p.prNumber == 0 {
				p.prNumber, p.prURL = e.PRNumber, e.PRURL
			}
		case "assistant", "user":
			if p.foundLastMsg {
				continue
			}
			p.foundLastMsg, p.lastAt = true, e.Timestamp
			if e.IsSidechain {
				p.activity = "subagente trabajando"
				continue
			}
			describe(&p, e)
		}
	}
	return p
}

func describe(p *parsed, e transcriptEntry) {
	var blocks []contentBlock
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		// A plain string: a prompt just sent.
		p.activity = "pensando"
		return
	}
	if e.Type == "user" {
		p.activity = "pensando" // a tool result went back to the model
		return
	}
	for i := len(blocks) - 1; i >= 0; i-- {
		if b := blocks[i]; b.Type == "tool_use" {
			p.pendingTool, p.activity = true, describeTool(b.Name, b.Input)
			return
		}
	}
	if e.Message.StopReason == "end_turn" {
		p.ended = true
		return
	}
	p.activity = "pensando"
}

// describeTool turns a tool call into a few words: "editando model.go".
func describeTool(name string, in map[string]any) string {
	str := func(k string) string {
		s, _ := in[k].(string)
		s = strings.Join(strings.Fields(s), " ")
		if len([]rune(s)) > 60 {
			s = string([]rune(s)[:59]) + "…"
		}
		return s
	}
	base := func(k string) string { return filepath.Base(str(k)) }
	switch name {
	case "Bash":
		if d := str("description"); d != "" {
			return "Bash: " + d
		}
		return "Bash: " + str("command")
	case "Edit", "MultiEdit":
		return "editando " + base("file_path")
	case "Write":
		return "escribiendo " + base("file_path")
	case "Read":
		return "leyendo " + base("file_path")
	case "NotebookEdit":
		return "editando " + base("notebook_path")
	case "Grep", "Glob":
		return "buscando " + str("pattern")
	case "WebFetch":
		if u, err := url.Parse(str("url")); err == nil && u.Host != "" {
			return "leyendo " + u.Host
		}
		return "leyendo la web"
	case "WebSearch":
		return "buscando en la web: " + str("query")
	case "Task", "Agent":
		return "subagente: " + str("description")
	case "TodoWrite":
		return "actualizando sus tareas"
	}
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.Split(name, "__")
		return "mcp " + parts[len(parts)-1]
	}
	return name
}

// session turns a parsed transcript into a state. A pending tool call is
// running while the transcript moves or the process uses CPU; when both
// stop for blockedAfter it is most likely a permission prompt.
func (p parsed) session(now time.Time, cpu float64) Session {
	s := Session{ID: p.id, Title: p.title, Activity: p.activity, LastAt: p.lastAt, PRNumber: p.prNumber, PRURL: p.prURL}
	switch {
	case p.ended:
		s.State = SessionWaiting
	case p.pendingTool && now.Sub(p.modTime) > blockedAfter && cpu < 2:
		s.State = SessionBlocked
	default:
		s.State = SessionWorking
	}
	return s
}
