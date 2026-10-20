package collector

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/config"
)

func TestCost(t *testing.T) {
	tok := Tokens{Input: 1_000_000, Output: 1_000_000, CacheWrite5m: 1_000_000, CacheWrite1h: 1_000_000, CacheRead: 1_000_000}
	// Opus 5.5: 4 + 20 + 4·1.25 + 4·2 + 0.20
	if c, ok := Cost("claude-opus-5-5", tok, false); !ok || math.Abs(c-37.2) > 1e-9 {
		t.Errorf("opus 5.5 = %v, %v", c, ok)
	}
	if c, _ := Cost("claude-opus-5-5", tok, true); math.Abs(c-74.4) > 1e-9 {
		t.Errorf("fast mode = %v, want twice", c)
	}
	small := Tokens{Input: 10_000, Output: 1_000_000}
	if c, _ := Cost("claude-haiku-5-5", small, false); math.Abs(c-(0.001+0.5)) > 1e-9 {
		t.Errorf("haiku small prompt = %v", c)
	}
	big := Tokens{Input: 200_000}
	if c, _ := Cost("claude-haiku-5-5", big, false); math.Abs(c-0.1) > 1e-9 {
		t.Errorf("haiku long prompt = %v, want the >100K rate", c)
	}
	if _, ok := Cost("modelo-desconocido", tok, false); ok {
		t.Error("an unknown model got a price")
	}
}

// assistantLine is one transcript line of an assistant message.
func assistantLine(id, model, ts string, in, out, read, write int64) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"cwd":"/w/odomto","message":{"id":%q,"model":%q,`+
		`"content":[{"type":"tool_use","id":"toolu_1","input":{"note":"un \"usage\":{ falso"}}],`+
		`"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,`+
		`"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":0}}}}`,
		ts, id, model, in, out, read, write, write)
}

func TestUsageLineDedupesAndCounts(t *testing.T) {
	f := &fileUsage{}
	ts := "2026-10-20T10:00:00Z"
	for _, l := range []string{
		`{"type":"user","cwd":"/w/odomto","message":{"content":"hola"}}`,
		assistantLine("msg_A", "claude-opus-5-5", ts, 10, 100, 1000, 50), // thinking block
		assistantLine("msg_A", "claude-opus-5-5", ts, 10, 100, 1000, 50), // same message, tool_use block
		assistantLine("msg_B", "claude-opus-5-5", ts, 5, 200, 2000, 0),
		`{"type":"custom-title","customTitle":"Sparkline \"v2\""}`,
	} {
		usageLine(f, []byte(l))
	}
	s := f.Session
	if s.Messages != 2 || s.Tokens.Input != 15 || s.Tokens.Output != 300 || s.Tokens.CacheRead != 3000 || s.Tokens.CacheWrite5m != 50 {
		t.Errorf("session = %+v", s)
	}
	if s.Cwd != "/w/odomto" || s.Title != `Sparkline "v2"` || s.Model != "claude-opus-5-5" {
		t.Errorf("metadata = %+v", s)
	}
	want, _ := Cost("claude-opus-5-5", s.Tokens, false)
	if math.Abs(s.CostUSD-want) > 1e-12 {
		t.Errorf("cost = %v, want %v", s.CostUSD, want)
	}
	if len(f.Days) != 1 {
		t.Errorf("days = %+v", f.Days)
	}
}

func TestUsageIsIncrementalAndPersists(t *testing.T) {
	claude := t.TempDir()
	dir := filepath.Join(claude, "projects", "-w-odomto")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s1.jsonl")
	now := time.Now()
	ts := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	write := func(s string, flag int) {
		f, err := os.OpenFile(path, flag|os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	// One full line and one still being written.
	half := assistantLine("msg_B", "claude-sonnet-5-5", ts, 1, 1000, 0, 0)
	write(assistantLine("msg_A", "claude-sonnet-5-5", ts, 1, 1000, 0, 0)+"\n"+half[:40], os.O_TRUNC)
	state := filepath.Join(t.TempDir(), "usage.json")
	newUsage := func() *Usage {
		return &Usage{Projects: []config.Project{{Name: "odomto", Path: "/w/odomto"}}, ClaudeDir: claude, StatePath: state}
	}
	u := newUsage()
	snap, err := u.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s := snap.(UsageSnapshot); s.Today.Tokens.Output != 1000 || len(s.Sessions) != 1 || s.Sessions[0].Project != "odomto" {
		t.Fatalf("first read = %+v", s)
	}
	write(half[40:]+"\n", os.O_APPEND)
	snap, _ = u.Collect(context.Background())
	if s := snap.(UsageSnapshot); s.Today.Tokens.Output != 2000 {
		t.Errorf("after the line completed: output = %d, want 2000", s.Today.Tokens.Output)
	}
	// A restart reads nothing again: the state file has the totals.
	snap, _ = newUsage().Collect(context.Background())
	s := snap.(UsageSnapshot)
	if s.Today.Tokens.Output != 2000 || len(s.Projects) != 1 || s.Projects[0].Project != "odomto" {
		t.Errorf("after restart = %+v", s)
	}
	if len(s.Days) != usageWindow {
		t.Errorf("days = %d, want %d", len(s.Days), usageWindow)
	}
}

func TestUsageCountsEveryDayOfTheWindow(t *testing.T) {
	claude := t.TempDir()
	dir := filepath.Join(claude, "projects", "-w")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	var b strings.Builder
	for i := range usageWindow {
		ts := now.AddDate(0, 0, -i).Format(time.RFC3339)
		b.WriteString(assistantLine(fmt.Sprint("msg_", i), "claude-opus-5-5", ts, 0, 100, 0, 0) + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := (&Usage{ClaudeDir: claude}).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range snap.(UsageSnapshot).Days {
		if d.Tokens.Output != 100 {
			t.Errorf("%s: output = %d, want 100 (every day has one message)", d.Day, d.Tokens.Output)
		}
	}
}

// TestUsageOnRealTranscripts reads this machine's Claude Code history and
// reports how long the first (cold) read takes. Run with ATALAYA_LIVE_USAGE=1.
func TestUsageOnRealTranscripts(t *testing.T) {
	if os.Getenv("ATALAYA_LIVE_USAGE") == "" {
		t.Skip("set ATALAYA_LIVE_USAGE=1 to read ~/.claude")
	}
	u := &Usage{StatePath: filepath.Join(t.TempDir(), "usage.json")}
	start := time.Now()
	snap, err := u.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)
	start = time.Now()
	_, _ = u.Collect(context.Background())
	warm := time.Since(start)
	s := snap.(UsageSnapshot)
	var b strings.Builder
	for _, d := range s.Days {
		fmt.Fprintf(&b, "  %s  %8.2f USD  %6.1fM tokens\n", d.Day, d.CostUSD, float64(d.Tokens.Total())/1e6)
	}
	t.Logf("frío %v, caliente %v; %d sesiones; sin precio: %d tokens\n%s", cold, warm, len(s.Sessions), s.Unpriced, b.String())
}
