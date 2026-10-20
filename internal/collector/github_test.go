package collector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otuletta/atalaya/internal/config"
)

var ghProjects = []config.Project{
	{Name: "odomto", GitHub: "otuletta/odomto"},
	{Name: "ghost", GitHub: "otuletta/ghost"},
	{Name: "local-only", Path: "/tmp/x"},
}

func newGitHub(t *testing.T, handler http.HandlerFunc) (*GitHub, *atomic.Int32) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	var tokenCalls atomic.Int32
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	return &GitHub{
		Projects: ghProjects,
		Endpoint: srv.URL,
		Token: func(context.Context) (string, error) {
			tokenCalls.Add(1)
			return "test-token", nil
		},
		Now: func() time.Time { return now },
	}, &tokenCalls
}

func TestGitHubSingleQuery(t *testing.T) {
	fixture, err := os.ReadFile("testdata/github_response.json")
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	g, _ := newGitHub(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var body struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, want := range []string{
			`r0: repository(owner: "otuletta", name: "odomto")`,
			`r1: repository(owner: "otuletta", name: "ghost")`,
			`search(query: "is:open is:pr review-requested:@me archived:false", type: ISSUE, first: 50)`,
			`rateLimit{limit,remaining,cost,resetAt}`,
			`... on CheckRun{name,status,conclusion,detailsUrl}`,
		} {
			if !strings.Contains(body.Query, want) {
				t.Errorf("query does not contain %s:\n%s", want, body.Query)
			}
		}
		if strings.Contains(body.Query, "r2:") {
			t.Error("a project without github was queried")
		}
		_, _ = w.Write(fixture)
	})

	s, err := g.Collect(context.Background())
	if err != nil {
		t.Fatalf("partial GraphQL errors must not fail the whole collection: %v", err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("%d requests, want exactly 1 for every repo", n)
	}
	snap := s.(GitHubSnapshot)
	if snap.Viewer != "otuletta" || snap.RateLimit.Remaining != 4990 {
		t.Errorf("viewer/rate = %q %+v", snap.Viewer, snap.RateLimit)
	}
	if len(snap.Repos) != 2 {
		t.Fatalf("repos = %d", len(snap.Repos))
	}

	r := snap.Repos[0]
	if r.Err != "" || r.DefaultBranch != "main" || r.OpenPRs != 2 || len(r.PRs) != 2 {
		t.Errorf("repo = %+v", r)
	}
	ci := r.CI
	if ci == nil {
		t.Fatal("no CI")
	}
	// The last Actions run is Test (10:00:05 → 10:03:20); the queued suite
	// of a non-Actions app is ignored.
	if ci.State != CIFailure || ci.Workflow != "Test" || ci.DurationSec != 195 ||
		ci.URL != "https://github.com/otuletta/odomto/actions/runs/11" || ci.Commit != "abcdef1" {
		t.Errorf("ci = %+v", ci)
	}
	if len(r.FailedChecks) != 2 || r.FailedChecks[0].Name != "test" || r.FailedChecks[1].Name != "vercel" {
		t.Errorf("failed checks = %+v", r.FailedChecks)
	}
	if r.PRs[0].Checks != CIFailure || r.PRs[1].Checks != "" || !r.PRs[1].Draft || r.PRs[1].Author != "" {
		t.Errorf("prs = %+v", r.PRs)
	}
	if r.FailingPRs() != 1 {
		t.Errorf("FailingPRs = %d", r.FailingPRs())
	}
	if len(r.ReviewRequests) != 1 || r.ReviewRequests[0].Number != 41 || r.ReviewRequests[0].Author != "colega" {
		t.Errorf("review requests = %+v", r.ReviewRequests)
	}

	if ghost := snap.Repos[1]; ghost.Err == "" || ghost.CI != nil {
		t.Errorf("ghost = %+v, want an error", ghost)
	}
	if len(snap.OtherReviewRequests) != 1 || snap.OtherReviewRequests[0].Repo != "acme/other" {
		t.Errorf("other review requests = %+v", snap.OtherReviewRequests)
	}
}

func TestLastRun(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 19, h, m, 0, 0, time.UTC) }
	wf := func(name string) *struct {
		URL      string `graphql:"url"`
		Workflow struct{ Name string }
	} {
		w := &struct {
			URL      string `graphql:"url"`
			Workflow struct{ Name string }
		}{URL: "https://x/" + name}
		w.Workflow.Name = name
		return w
	}
	tests := []struct {
		name   string
		suites []suiteNode
		want   CIRun
	}{
		{"scheduled runs on an old commit: only the newest counts", []suiteNode{
			{Status: "COMPLETED", Conclusion: "FAILURE", CreatedAt: at(1, 0), UpdatedAt: at(1, 5), WorkflowRun: wf("Nightly")},
			{Status: "COMPLETED", Conclusion: "SUCCESS", CreatedAt: at(11, 0), UpdatedAt: at(11, 2), WorkflowRun: wf("Nightly")},
		}, CIRun{State: CISuccess, Workflow: "Nightly", URL: "https://x/Nightly", StartedAt: at(11, 0), DurationSec: 120}},
		{"a passing bot workflow does not hide a broken CI", []suiteNode{
			{Status: "COMPLETED", Conclusion: "FAILURE", CreatedAt: at(9, 0), UpdatedAt: at(9, 1), WorkflowRun: wf("CI")},
			{Status: "COMPLETED", Conclusion: "SUCCESS", CreatedAt: at(11, 0), UpdatedAt: at(11, 5), WorkflowRun: wf("Dependabot Updates")},
		}, CIRun{State: CIFailure, Workflow: "CI", URL: "https://x/CI", StartedAt: at(9, 0), DurationSec: 60}},
		{"a CI that was fixed is green again", []suiteNode{
			{Status: "COMPLETED", Conclusion: "FAILURE", CreatedAt: at(9, 0), UpdatedAt: at(9, 1), WorkflowRun: wf("CI")},
			{Status: "COMPLETED", Conclusion: "SUCCESS", CreatedAt: at(10, 0), UpdatedAt: at(10, 3), WorkflowRun: wf("CI")},
		}, CIRun{State: CISuccess, Workflow: "CI", URL: "https://x/CI", StartedAt: at(10, 0), DurationSec: 180}},
		{"in progress grows until now", []suiteNode{
			{Status: "IN_PROGRESS", CreatedAt: at(11, 50), UpdatedAt: at(11, 51), WorkflowRun: wf("CI")},
		}, CIRun{State: CIPending, Workflow: "CI", URL: "https://x/CI", StartedAt: at(11, 50), DurationSec: 600}},
		{"cancelled", []suiteNode{
			{Status: "COMPLETED", Conclusion: "CANCELLED", CreatedAt: at(11, 0), UpdatedAt: at(11, 1), WorkflowRun: wf("CI")},
		}, CIRun{State: CICancelled, Workflow: "CI", URL: "https://x/CI", StartedAt: at(11, 0), DurationSec: 60}},
		{"no Actions: rollup state is kept", []suiteNode{
			{Status: "QUEUED", CreatedAt: at(11, 0), UpdatedAt: at(11, 0)},
		}, CIRun{State: CISuccess}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CIRun{State: CISuccess}
			lastRun(&got, tt.suites, now)
			if got != tt.want {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestGitHubRateLimitExhausted(t *testing.T) {
	reset := time.Date(2026, 10, 19, 13, 0, 0, 0, time.UTC)
	g, _ := newGitHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"API rate limit exceeded"}`)
	})
	_, err := g.Collect(context.Background())
	var retry *RetryAfterError
	if !errors.As(err, &retry) || !retry.At.Equal(reset) {
		t.Fatalf("err = %v, want RetryAfterError at %v", err, reset)
	}
}

func TestGitHubSecondaryRateLimit(t *testing.T) {
	g, _ := newGitHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "90")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := g.Collect(context.Background())
	var retry *RetryAfterError
	if !errors.As(err, &retry) || !retry.At.Equal(g.now().Add(90*time.Second)) {
		t.Fatalf("err = %v, want retry in 90s", err)
	}
}

func TestGitHubLowBudgetPausesButKeepsData(t *testing.T) {
	g, _ := newGitHub(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"data":{"viewer":{"login":"me"},
			"rateLimit":{"limit":5000,"remaining":3,"cost":1,"resetAt":"2026-10-19T13:00:00Z"},
			"search":{"nodes":[]},"r0":{"nameWithOwner":"otuletta/odomto","pullRequests":{"totalCount":0,"nodes":[]}},"r1":null}}`)
	})
	snap, err := g.Collect(context.Background())
	var retry *RetryAfterError
	if !errors.As(err, &retry) || snap == nil {
		t.Fatalf("snap = %v, err = %v; want data and a retry", snap, err)
	}
}

func TestGitHubUnauthorizedRefreshesToken(t *testing.T) {
	g, tokenCalls := newGitHub(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	for range 2 {
		_, err := g.Collect(context.Background())
		if err == nil || !strings.Contains(err.Error(), "gh auth login") {
			t.Fatalf("err = %v", err)
		}
	}
	if n := tokenCalls.Load(); n != 2 {
		t.Errorf("token fetched %d times, want once per attempt after a 401", n)
	}
}

func TestGitHubNoProjects(t *testing.T) {
	g := &GitHub{Projects: []config.Project{{Name: "x", Path: "/x"}}, Token: func(context.Context) (string, error) {
		t.Fatal("token requested without any github project")
		return "", nil
	}}
	snap, err := g.Collect(context.Background())
	if err != nil || len(snap.(GitHubSnapshot).Repos) != 0 {
		t.Errorf("snap = %+v, err = %v", snap, err)
	}
}

func TestGitHubNetworkError(t *testing.T) {
	g, _ := newGitHub(t, func(http.ResponseWriter, *http.Request) {})
	g.Endpoint = "http://127.0.0.1:1"
	if _, err := g.Collect(context.Background()); err == nil {
		t.Fatal("expected a network error")
	}
}

// TestGitHubLive runs the real query against api.github.com with the gh CLI
// token. It only runs with ATALAYA_LIVE=owner/repo.
func TestGitHubLive(t *testing.T) {
	repo := os.Getenv("ATALAYA_LIVE")
	if repo == "" {
		t.Skip("set ATALAYA_LIVE=owner/repo to run against GitHub")
	}
	g := &GitHub{Projects: []config.Project{{Name: "live", GitHub: repo}}}
	snap, err := g.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.MarshalIndent(snap, "", "  ")
	t.Logf("%s", b)
}
