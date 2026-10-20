package collector

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/config"
)

func TestHealth(t *testing.T) {
	ok := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer broken.Close()
	down := httptest.NewServer(http.NotFoundHandler())
	downURL := down.URL
	down.Close()

	h := &Health{
		Projects: []config.Project{
			{Name: "api", Health: []string{ok.URL + "/health", broken.URL}},
			{Name: "old", Health: []string{downURL}},
		},
		Client: ok.Client(), // trusts the test certificate
	}
	ctx := context.Background()
	var checks []Check
	for range historySize + 5 {
		snap, err := h.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		checks = snap.(HealthSnapshot).Checks
	}
	if len(checks) != 3 {
		t.Fatalf("checks = %+v", checks)
	}

	good := checks[0]
	if !good.OK || good.Status != 200 || good.Err != "" || good.Project != "api" {
		t.Errorf("good = %+v", good)
	}
	if good.TLS == nil || good.TLS.DaysLeft <= 0 || good.TLS.NotAfter.Before(time.Now()) {
		t.Errorf("TLS = %+v", good.TLS)
	}
	if len(good.History) != historySize {
		t.Errorf("history = %d samples, want capped at %d", len(good.History), historySize)
	}

	if b := checks[1]; b.OK || b.Status != 503 || !strings.Contains(b.Err, "503") || b.TLS != nil {
		t.Errorf("broken = %+v", b)
	}
	if d := checks[2]; d.OK || d.Status != 0 || !strings.Contains(d.Err, "refused") {
		t.Errorf("down = %+v", d)
	}
}

func TestHealthHistoryIsCopied(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	h := &Health{Projects: []config.Project{{Name: "x", Health: []string{srv.URL}}}}
	first, _ := h.Collect(context.Background())
	_, _ = h.Collect(context.Background())
	if n := len(first.(HealthSnapshot).Checks[0].History); n != 1 {
		t.Errorf("an earlier snapshot changed after a later run: %d samples", n)
	}
}

func TestHealthTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	h := &Health{Projects: []config.Project{{Name: "x", Health: []string{slow.URL}}}, Timeout: 100 * time.Millisecond}
	snap, err := h.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c := snap.(HealthSnapshot).Checks[0]; c.Err != "timeout" {
		t.Errorf("err = %q, want timeout", c.Err)
	}
}

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	var samples []Sample
	add := func(ago time.Duration, ms int64, ok bool) {
		samples = append(samples, Sample{At: now.Add(-ago), LatencyMS: ms, OK: ok})
	}
	// Three days ago: a 2-check outage (an incident), then a lone failure
	// (not one).
	add(72*time.Hour, 100, false)
	add(71*time.Hour, 100, false)
	add(70*time.Hour, 100, true)
	add(69*time.Hour, 100, false)
	add(68*time.Hour, 100, true)
	// Today: 18 fast checks, 2 slow ones, then down right now.
	for i := range 20 {
		ms := int64(100)
		if i >= 18 {
			ms = 900
		}
		add(time.Duration(20-i)*time.Hour, ms, true)
	}
	add(10*time.Minute, 0, false)
	add(5*time.Minute, 0, false)
	add(8*24*time.Hour, 1, false) // outside the week: ignored

	s := summarize(samples, now)
	if got := fmt.Sprintf("%.1f", s.Uptime24h); got != "90.9" { // 20 of 22
		t.Errorf("uptime 24h = %s", got)
	}
	if got := fmt.Sprintf("%.1f", s.Uptime7d); got != "81.5" { // 22 of 27
		t.Errorf("uptime 7d = %s", got)
	}
	if s.P95MS != 900 {
		t.Errorf("p95 = %d", s.P95MS)
	}
	if len(s.Incidents) != 2 || !s.Incidents[0].Start.Equal(now.Add(-72*time.Hour)) ||
		!s.Incidents[0].End.Equal(now.Add(-70*time.Hour)) || !s.Incidents[1].End.IsZero() ||
		!s.Incidents[1].Start.Equal(now.Add(-10*time.Minute)) {
		t.Errorf("incidents = %+v", s.Incidents)
	}
	if empty := summarize(nil, now); empty.Uptime24h != -1 || empty.P95MS != 0 {
		t.Errorf("no samples = %+v", empty)
	}
}

func TestHealthHistorySurvivesRestarts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "state", "health.jsonl")
	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	// A sample from two weeks ago, to be dropped on load.
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	old := fmt.Sprintf(`{"u":%q,"t":%d,"ms":5,"ok":false}`+"\n", srv.URL, clock.Add(-14*24*time.Hour).Unix())
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	newHealth := func() *Health {
		return &Health{Projects: []config.Project{{Name: "x", Health: []string{srv.URL}}}, HistoryPath: path,
			Now: func() time.Time { return clock }}
	}
	h := newHealth()
	for range 3 {
		if _, err := h.Collect(context.Background()); err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(30 * time.Second)
	}

	// A new process: the history comes back from disk.
	snap, err := newHealth().Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := snap.(HealthSnapshot).Checks[0]
	if len(c.History) != 4 || c.Stats == nil || c.Stats.Uptime24h != 100 {
		t.Errorf("history = %d samples, stats = %+v", len(c.History), c.Stats)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), `"ms":5`) {
		t.Error("the two-week-old sample was not pruned from the file")
	}
}
