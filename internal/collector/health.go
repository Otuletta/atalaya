package collector

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
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

// Check is the state of one endpoint.
type Check struct {
	Project   string    `json:"project"`
	URL       string    `json:"url"`
	Status    int       `json:"status,omitempty"`
	OK        bool      `json:"ok"`
	LatencyMS int64     `json:"latency_ms"`
	TLS       *TLSInfo  `json:"tls,omitempty"`
	Err       string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
	History   []Sample  `json:"history"`
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

	mu      sync.Mutex
	history map[string][]Sample
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
	if h.history == nil {
		h.history = map[string][]Sample{}
	}
	for i := range checks {
		c := &checks[i]
		hist := h.history[c.URL]
		hist = append(hist, Sample{At: c.CheckedAt, LatencyMS: c.LatencyMS, OK: c.OK})
		if len(hist) > historySize {
			hist = hist[len(hist)-historySize:]
		}
		h.history[c.URL] = hist
		// The snapshot gets its own copy: the store shares it with the UI.
		c.History = append([]Sample(nil), hist...)
	}
	return HealthSnapshot{Checks: checks}, nil
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
