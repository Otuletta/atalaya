package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
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
