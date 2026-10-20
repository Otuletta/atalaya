package collector

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/otuletta/atalaya/internal/config"
)

// Tokens counts what a model read and wrote, by price class.
type Tokens struct {
	Input        int64 `json:"input"`
	Output       int64 `json:"output"`
	CacheWrite5m int64 `json:"cache_write_5m"`
	CacheWrite1h int64 `json:"cache_write_1h"`
	CacheRead    int64 `json:"cache_read"`
}

// Total is every token, whatever its price.
func (t Tokens) Total() int64 {
	return t.Input + t.Output + t.CacheWrite5m + t.CacheWrite1h + t.CacheRead
}

func (t *Tokens) add(o Tokens) {
	t.Input += o.Input
	t.Output += o.Output
	t.CacheWrite5m += o.CacheWrite5m
	t.CacheWrite1h += o.CacheWrite1h
	t.CacheRead += o.CacheRead
}

// price is a model's rate per million tokens.
type price struct{ input, output, cacheRead float64 }

// prices are Anthropic's first-party API rates per million tokens, as
// published in October 2026. Cache writes are 1.25x input (5 minutes) and
// 2x input (1 hour). Claude Code subscriptions are not billed per token:
// the cost shown is what the same tokens would cost on the API.
var prices = map[string]price{
	"claude-fable-5-1":  {10, 50, 0.25},
	"claude-mythos-5-1": {10, 50, 0.25},
	"claude-fable-5":    {10, 50, 1},
	"claude-opus-5-5":   {4, 20, 0.20},
	"claude-opus-5":     {5, 25, 0.50},
	"claude-opus-4-8":   {5, 25, 0.50},
	"claude-opus-4-7":   {5, 25, 0.50},
	"claude-opus-4-6":   {5, 25, 0.50},
	"claude-sonnet-5-5": {2, 10, 0.20},
	"claude-sonnet-5":   {2, 10, 0.20},
	"claude-sonnet-4-6": {3, 15, 0.30},
	"claude-haiku-5-5":  {0.10, 0.50, 0.01},
	"claude-haiku-4-5":  {1, 5, 0.10},
}

// Cost estimates the API price of tokens on a model; ok is false for a
// model without a known rate. Fast mode bills twice the standard rate.
func Cost(model string, t Tokens, fast bool) (usd float64, ok bool) {
	p, ok := prices[model]
	if !ok {
		return 0, false
	}
	// Claude Haiku 5.5 charges more for prompts above 100K tokens.
	if model == "claude-haiku-5-5" && t.Input+t.CacheRead+t.CacheWrite5m+t.CacheWrite1h > 100_000 {
		p = price{0.50, 2.50, 0.05}
	}
	m := 1.0
	if fast {
		m = 2
	}
	usd = (float64(t.Input)*p.input +
		float64(t.Output)*p.output +
		float64(t.CacheWrite5m)*p.input*1.25 +
		float64(t.CacheWrite1h)*p.input*2 +
		float64(t.CacheRead)*p.cacheRead) / 1e6 * m
	return usd, true
}

// SessionUsage is what one Claude Code session spent.
type SessionUsage struct {
	ID       string `json:"id"`
	Project  string `json:"project,omitempty"`
	Cwd      string `json:"cwd,omitempty"`
	Title    string `json:"title,omitempty"`
	Model    string `json:"model,omitempty"`
	Messages int    `json:"messages"`
	Tokens   Tokens `json:"tokens"`
	// Unpriced are tokens of models without a known rate.
	Unpriced int64     `json:"unpriced_tokens,omitempty"`
	CostUSD  float64   `json:"cost_usd"`
	LastAt   time.Time `json:"last_at"`
}

// DayUsage is the spend of one day, local time.
type DayUsage struct {
	Day     string  `json:"day"` // 2006-01-02
	Tokens  Tokens  `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// ProjectUsage is a project's spend over the window.
type ProjectUsage struct {
	Project string  `json:"project"`
	Tokens  Tokens  `json:"tokens"`
	CostUSD float64 `json:"cost_usd"`
}

// UsageSnapshot is the result of the usage collector.
type UsageSnapshot struct {
	Today    DayUsage       `json:"today"`
	Days     []DayUsage     `json:"days"` // oldest first, the whole window
	Projects []ProjectUsage `json:"projects"`
	Sessions []SessionUsage `json:"sessions"` // most recent first
	// Unpriced counts tokens of models without a known rate.
	Unpriced int64 `json:"unpriced_tokens,omitempty"`
}

// usageWindow is how far back usage is summed.
const usageWindow = 7

// fileUsage is what has been read of one transcript; it persists so a
// restart only reads what was appended since.
type fileUsage struct {
	Size    int64               `json:"size"`
	ModTime time.Time           `json:"mtime"`
	Offset  int64               `json:"offset"`
	LastID  string              `json:"last_id"`
	Session SessionUsage        `json:"session"`
	Days    map[string]DayUsage `json:"days"`
}

// Usage sums what Claude Code sessions spend, from their transcripts. It
// reads each file incrementally (transcripts only grow) and keeps its
// progress in StatePath, so even gigabytes of history are read once.
type Usage struct {
	Projects  []config.Project
	ClaudeDir string
	StatePath string
	Every     time.Duration
	Now       func() time.Time

	mu      sync.Mutex
	loaded  bool
	files   map[string]*fileUsage
	matcher *projectMatcher
}

func (u *Usage) Name() string            { return "usage" }
func (u *Usage) Interval() time.Duration { return u.Every }

func (u *Usage) now() time.Time {
	if u.Now != nil {
		return u.Now()
	}
	return time.Now()
}

func (u *Usage) Collect(ctx context.Context) (Snapshot, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.loaded {
		u.loaded = true
		u.files = map[string]*fileUsage{}
		if b, err := os.ReadFile(u.StatePath); err == nil && u.StatePath != "" {
			_ = json.Unmarshal(b, &u.files)
		}
		u.matcher = newProjectMatcher(u.Projects)
	}
	dir := u.ClaudeDir
	if dir == "" {
		dir = claudeDir()
	}
	now := u.now()
	cutoff := now.AddDate(0, 0, -usageWindow)
	paths, _ := filepath.Glob(filepath.Join(dir, "projects", "*", "*.jsonl"))
	seen := map[string]bool{}
	changed := false
	for _, path := range paths {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		info, err := os.Stat(path)
		if err != nil || info.ModTime().Before(cutoff) {
			continue
		}
		seen[path] = true
		f := u.files[path]
		if f != nil && f.Size == info.Size() && f.ModTime.Equal(info.ModTime()) {
			continue
		}
		if f == nil || info.Size() < f.Offset { // new, or rewritten
			f = &fileUsage{Days: map[string]DayUsage{}}
			f.Session.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
			u.files[path] = f
		}
		if err := readUsage(ctx, path, f); err != nil {
			continue
		}
		f.Size, f.ModTime = info.Size(), info.ModTime()
		changed = true
	}
	for path := range u.files {
		if !seen[path] {
			delete(u.files, path)
			changed = true
		}
	}
	if changed && u.StatePath != "" {
		if b, err := json.Marshal(u.files); err == nil {
			_ = os.MkdirAll(filepath.Dir(u.StatePath), 0o755)
			if os.WriteFile(u.StatePath+".tmp", b, 0o644) == nil {
				_ = os.Rename(u.StatePath+".tmp", u.StatePath)
			}
		}
	}
	return u.summarize(now), nil
}

// summarize adds up the files into days, projects and sessions.
func (u *Usage) summarize(now time.Time) UsageSnapshot {
	var s UsageSnapshot
	// Allocated up front: days holds pointers into it, which an append
	// that grows the slice would leave pointing at a stale copy.
	s.Days = make([]DayUsage, usageWindow)
	days := map[string]*DayUsage{}
	for i := range s.Days {
		d := now.AddDate(0, 0, i-usageWindow+1).Format("2006-01-02")
		s.Days[i].Day = d
		days[d] = &s.Days[i]
	}
	projects := map[string]*ProjectUsage{}
	for _, f := range u.files {
		sess := f.Session
		sess.Project = u.matcher.match(sess.Cwd)
		if sess.Messages == 0 {
			continue
		}
		s.Sessions = append(s.Sessions, sess)
		var weekTokens Tokens
		var weekCost float64
		for day, du := range f.Days {
			if d := days[day]; d != nil {
				d.Tokens.add(du.Tokens)
				d.CostUSD += du.CostUSD
				weekTokens.add(du.Tokens)
				weekCost += du.CostUSD
			}
		}
		name := sess.Project
		if name == "" {
			name = "sin proyecto"
		}
		p := projects[name]
		if p == nil {
			p = &ProjectUsage{Project: name}
			projects[name] = p
		}
		p.Tokens.add(weekTokens)
		p.CostUSD += weekCost
		s.Unpriced += sess.Unpriced
	}
	s.Today = s.Days[len(s.Days)-1]
	for _, p := range projects {
		if p.Tokens.Total() > 0 {
			s.Projects = append(s.Projects, *p)
		}
	}
	sort.Slice(s.Projects, func(i, j int) bool { return s.Projects[i].CostUSD > s.Projects[j].CostUSD })
	sort.Slice(s.Sessions, func(i, j int) bool { return s.Sessions[i].LastAt.After(s.Sessions[j].LastAt) })
	if len(s.Sessions) > 50 {
		s.Sessions = s.Sessions[:50]
	}
	return s
}

// The keys looked for in each line. A quote inside a JSON string is
// escaped (\"), so these unescaped forms only match real keys.
var (
	keyAssistant = []byte(`"type":"assistant"`)
	keyUsage     = []byte(`"usage":{`)
	keyMsgID     = []byte(`"id":"msg_`)
	keyModel     = []byte(`"model":"`)
	keyTime      = []byte(`"timestamp":"`)
	keyCwd       = []byte(`"cwd":"`)
	keyTitle     = []byte(`"customTitle":"`)
)

// readUsage reads the complete lines appended since f.Offset. A line being
// written (no newline yet) is left for the next read.
func readUsage(ctx context.Context, path string, f *fileUsage) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(f.Offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(file, 1<<20)
	var long []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, chunk...)
			continue
		}
		if err != nil { // EOF: an incomplete line stays for later
			return nil
		}
		line := chunk
		if long != nil {
			long = append(long, chunk...)
			line, long = long, nil
		}
		f.Offset += int64(len(line))
		usageLine(f, line)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// usageLine accounts one transcript line. Only assistant messages carry
// usage, and a message with several blocks is written as several lines
// with the same id and usage: it counts once.
func usageLine(f *fileUsage, line []byte) {
	if f.Session.Cwd == "" {
		f.Session.Cwd = stringAfter(line, keyCwd)
	}
	if t := stringAfter(line, keyTitle); t != "" {
		f.Session.Title = t
	}
	if !bytes.Contains(line, keyAssistant) {
		return
	}
	i := bytes.Index(line, keyUsage)
	if i < 0 {
		return
	}
	if id := messageID(line); id != "" {
		if id == f.LastID {
			return
		}
		f.LastID = id
	}
	var raw struct {
		Input         int64  `json:"input_tokens"`
		Output        int64  `json:"output_tokens"`
		CacheRead     int64  `json:"cache_read_input_tokens"`
		CacheCreation int64  `json:"cache_creation_input_tokens"`
		Speed         string `json:"speed"`
		ByTTL         *struct {
			M5 int64 `json:"ephemeral_5m_input_tokens"`
			H1 int64 `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
	}
	if json.NewDecoder(bytes.NewReader(line[i+len(`"usage":`):])).Decode(&raw) != nil {
		return
	}
	t := Tokens{Input: raw.Input, Output: raw.Output, CacheRead: raw.CacheRead, CacheWrite5m: raw.CacheCreation}
	if raw.ByTTL != nil && raw.ByTTL.M5+raw.ByTTL.H1 > 0 {
		t.CacheWrite5m, t.CacheWrite1h = raw.ByTTL.M5, raw.ByTTL.H1
	}
	model := stringAfter(line, keyModel)
	at, _ := time.Parse(time.RFC3339Nano, stringAfter(line, keyTime))
	cost, priced := Cost(model, t, raw.Speed == "fast")

	f.Session.Messages++
	f.Session.Tokens.add(t)
	f.Session.CostUSD += cost
	if !priced {
		f.Session.Unpriced += t.Total()
	}
	// "<synthetic>" marks messages Claude Code writes itself.
	if model != "" && !strings.HasPrefix(model, "<") {
		f.Session.Model = model
	}
	if at.After(f.Session.LastAt) {
		f.Session.LastAt = at
	}
	if f.Days == nil {
		f.Days = map[string]DayUsage{}
	}
	day := at.Local().Format("2006-01-02")
	du := f.Days[day]
	du.Day = day
	du.Tokens.add(t)
	du.CostUSD += cost
	f.Days[day] = du
}

// messageID is the API message id ("msg_…") of an assistant line. Tool
// calls also have "id" keys, so the "msg_" prefix is part of the search.
func messageID(line []byte) string {
	i := bytes.Index(line, keyMsgID)
	if i < 0 {
		return ""
	}
	rest := line[i+len(`"id":"`):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return string(rest[:j])
}

// stringAfter decodes the JSON string that starts right after key (which
// ends with the opening quote), or "".
func stringAfter(line, key []byte) string {
	i := bytes.Index(line, key)
	if i < 0 {
		return ""
	}
	start := i + len(key) - 1 // the opening quote
	end := start + 1
	for end < len(line) {
		if line[end] == '\\' {
			end += 2
			continue
		}
		if line[end] == '"' {
			break
		}
		end++
	}
	if end >= len(line) {
		return ""
	}
	var s string
	if json.Unmarshal(line[start:end+1], &s) != nil {
		return ""
	}
	return s
}
