package collector

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/otuletta/atalaya/internal/config"
)

// historySize is how many latency samples each endpoint keeps for its
// sparkline: 40 samples at the default 30s interval is the last 20 minutes.
const historySize = 40

// Sample is one point of the latency history.
type Sample struct {
	At        time.Time `json:"at"`
	LatencyMS int64     `json:"latency_ms"`
	OK        bool      `json:"ok"`
}

// TLSInfo describes the leaf certificate of an HTTPS endpoint.
type TLSInfo struct {
	NotAfter time.Time `json:"not_after"`
	DaysLeft int       `json:"days_left"`
	Issuer   string    `json:"issuer"`
	Subject  string    `json:"subject"`
}

// Incident is a period an endpoint was down: two or more failed checks in
// a row. End is zero while it lasts.
type Incident struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end,omitzero"`
}

// HealthStats summarize the persisted history. Uptimes are percentages,
// -1 without samples in the window.
type HealthStats struct {
	Uptime24h float64    `json:"uptime_24h"`
	Uptime7d  float64    `json:"uptime_7d"`
	P95MS     int64      `json:"p95_ms_24h"`
	Incidents []Incident `json:"incidents_7d,omitempty"`
}

// Check is the state of one endpoint.
type Check struct {
	Project   string       `json:"project"`
	URL       string       `json:"url"`
	Status    int          `json:"status,omitempty"`
	OK        bool         `json:"ok"`
	LatencyMS int64        `json:"latency_ms"`
	TLS       *TLSInfo     `json:"tls,omitempty"`
	Err       string       `json:"error,omitempty"`
	CheckedAt time.Time    `json:"checked_at"`
	History   []Sample     `json:"history"`
	Stats     *HealthStats `json:"stats,omitempty"`
}

// HealthSnapshot is the result of the health collector.
type HealthSnapshot struct {
	Checks []Check `json:"checks"`
}

// Health checks every HTTP endpoint of every project. It keeps the latency
// history between runs, which is why it is the one stateful collector.
type Health struct {
	Projects []config.Project
	Every    time.Duration
	Timeout  time.Duration
	Client   *http.Client
	Now      func() time.Time
	// HistoryPath persists every sample (JSON lines) so uptime and the
	// sparkline survive restarts. Empty keeps history in memory only.
	HistoryPath string

	mu      sync.Mutex
	loaded  bool
	history map[string][]Sample // the last retention of samples per URL
}

// retention is how much history is kept and summarized.
const retention = 7 * 24 * time.Hour

// historyLine is a sample on disk; short keys, one line per check.
type historyLine struct {
	URL string `json:"u"`
	At  int64  `json:"t"`
	MS  int64  `json:"ms"`
	OK  bool   `json:"ok"`
}

func (h *Health) Name() string            { return "health" }
func (h *Health) Interval() time.Duration { return h.Every }

func (h *Health) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Health) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	timeout := h.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	h.Client = &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: timeout,
			// A fresh connection per check: latency then includes DNS, TCP and
			// TLS, which is what a user hitting production pays.
			DisableKeepAlives: true,
		},
	}
	return h.Client
}

func (h *Health) Collect(ctx context.Context) (Snapshot, error) {
	type target struct{ project, url string }
	var targets []target
	for _, p := range h.Projects {
		for _, u := range p.Health {
			targets = append(targets, target{p.Name, u})
		}
	}
	checks := make([]Check, len(targets))
	eg, gctx := errgroup.WithContext(ctx)
	eg.SetLimit(16)
	for i, t := range targets {
		eg.Go(func() error {
			checks[i] = h.check(gctx, t.project, t.url)
			return nil
		})
	}
	_ = eg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.loaded {
		h.loaded = true
		h.history = loadHistory(h.HistoryPath, h.now())
	}
	cutoff := h.now().Add(-retention)
	var lines []historyLine
	for i := range checks {
		c := &checks[i]
		s := Sample{At: c.CheckedAt, LatencyMS: c.LatencyMS, OK: c.OK}
		hist := h.history[c.URL]
		hist = append(hist, s)
		for len(hist) > 0 && hist[0].At.Before(cutoff) {
			hist = hist[1:]
		}
		h.history[c.URL] = hist
		// The snapshot gets its own copies: the store shares it with the UI.
		c.History = append([]Sample(nil), hist[max(len(hist)-historySize, 0):]...)
		c.Stats = summarize(hist, h.now())
		lines = append(lines, historyLine{URL: c.URL, At: s.At.Unix(), MS: s.LatencyMS, OK: s.OK})
	}
	appendHistory(h.HistoryPath, lines)
	return HealthSnapshot{Checks: checks}, nil
}

// loadHistory reads the samples of the last week. When the file holds
// older ones it is rewritten without them.
func loadHistory(path string, now time.Time) map[string][]Sample {
	hist := map[string][]Sample{}
	if path == "" {
		return hist
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return hist
	}
	cutoff := now.Add(-retention)
	var kept []historyLine
	stale := 0
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		var l historyLine
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			stale++
			continue
		}
		at := time.Unix(l.At, 0)
		if at.Before(cutoff) {
			stale++
			continue
		}
		kept = append(kept, l)
		hist[l.URL] = append(hist[l.URL], Sample{At: at, LatencyMS: l.MS, OK: l.OK})
	}
	if stale > 0 {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		for _, l := range kept {
			_ = enc.Encode(l)
		}
		if os.WriteFile(path+".tmp", buf.Bytes(), 0o644) == nil {
			_ = os.Rename(path+".tmp", path)
		}
	}
	return hist
}

// appendHistory adds this round of samples to the file. Failing to persist
// never fails the check.
func appendHistory(path string, lines []historyLine) {
	if path == "" || len(lines) == 0 {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	enc := json.NewEncoder(f)
	for _, l := range lines {
		_ = enc.Encode(l)
	}
	_ = f.Close()
}

// summarize computes uptime, p95 and incidents from a week of samples.
func summarize(samples []Sample, now time.Time) *HealthStats {
	s := &HealthStats{Uptime24h: -1, Uptime7d: -1}
	var n24, ok24, n7, ok7 int
	var lat []int64
	var open *Incident
	var firstFail time.Time
	fails := 0
	for _, x := range samples {
		if now.Sub(x.At) > retention {
			continue
		}
		n7++
		day := now.Sub(x.At) <= 24*time.Hour
		if day {
			n24++
		}
		if x.OK {
			ok7++
			if day {
				ok24++
				lat = append(lat, x.LatencyMS)
			}
			if open != nil {
				open.End = x.At
				s.Incidents = append(s.Incidents, *open)
				open = nil
			}
			fails = 0
			continue
		}
		fails++
		switch fails {
		case 1:
			firstFail = x.At
		case 2:
			open = &Incident{Start: firstFail}
		}
	}
	if open != nil {
		s.Incidents = append(s.Incidents, *open)
	}
	if n24 > 0 {
		s.Uptime24h = 100 * float64(ok24) / float64(n24)
	}
	if n7 > 0 {
		s.Uptime7d = 100 * float64(ok7) / float64(n7)
	}
	if len(lat) > 0 {
		slices.Sort(lat)
		s.P95MS = lat[int(math.Ceil(0.95*float64(len(lat))))-1]
	}
	return s
}

func (h *Health) check(ctx context.Context, project, url string) Check {
	c := Check{Project: project, URL: url}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		c.Err, c.CheckedAt = err.Error(), h.now()
		return c
	}
	req.Header.Set("User-Agent", "atalaya-healthcheck")
	start := time.Now()
	resp, err := h.client().Do(req)
	c.CheckedAt = h.now()
	if err != nil {
		c.LatencyMS = time.Since(start).Milliseconds()
		c.Err = shortNetErr(err)
		return c
	}
	defer resp.Body.Close()
	// Latency is time to first byte; the body is drained but not timed.
	c.LatencyMS = time.Since(start).Milliseconds()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	c.Status = resp.StatusCode
	c.OK = resp.StatusCode < 400
	if !c.OK {
		c.Err = resp.Status
	}
	c.TLS = tlsInfo(resp.TLS, c.CheckedAt)
	return c
}

func tlsInfo(st *tls.ConnectionState, now time.Time) *TLSInfo {
	if st == nil || len(st.PeerCertificates) == 0 {
		return nil
	}
	leaf := st.PeerCertificates[0]
	return &TLSInfo{
		NotAfter: leaf.NotAfter,
		DaysLeft: int(leaf.NotAfter.Sub(now).Hours() / 24),
		Issuer:   leaf.Issuer.CommonName,
		Subject:  leaf.Subject.CommonName,
	}
}

// shortNetErr trims Go's verbose `Get "url": dial tcp …` errors down to the
// part a human needs.
func shortNetErr(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "DNS: " + dns.Err
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Err != nil {
		return op.Err.Error()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}
