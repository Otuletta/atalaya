package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shurcooL/githubv4"

	"github.com/otuletta/atalaya/internal/config"
)

// CI states, normalized from GitHub's check and status enums.
const (
	CISuccess   = "success"
	CIFailure   = "failure"
	CIPending   = "pending"
	CICancelled = "cancelled"
)

// CIRun is the last GitHub Actions run on the head of the default branch.
// Without Actions, State comes from the commit's combined status and the
// other fields stay empty.
type CIRun struct {
	State     string    `json:"state"`
	Workflow  string    `json:"workflow,omitempty"`
	URL       string    `json:"url,omitempty"`
	Commit    string    `json:"commit"`
	StartedAt time.Time `json:"started_at,omitzero"`
	// DurationSec grows while the run is in progress.
	DurationSec int64 `json:"duration_sec,omitempty"`
}

// FailedCheck is a failing check run or commit status.
type FailedCheck struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// PullRequest is an open PR.
type PullRequest struct {
	Repo      string    `json:"repo"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Author    string    `json:"author"`
	Branch    string    `json:"branch,omitempty"`
	Draft     bool      `json:"draft,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Checks is the rollup of the PR's last commit: success, failure,
	// pending or empty when it has no checks.
	Checks         string `json:"checks,omitempty"`
	ReviewDecision string `json:"review_decision,omitempty"`
}

// GitHubRepo is the remote state of one project.
type GitHubRepo struct {
	Project        string        `json:"project"`
	Repo           string        `json:"repo"`
	URL            string        `json:"url,omitempty"`
	DefaultBranch  string        `json:"default_branch,omitempty"`
	CI             *CIRun        `json:"ci,omitempty"`
	FailedChecks   []FailedCheck `json:"failed_checks,omitempty"`
	OpenPRs        int           `json:"open_prs"`
	PRs            []PullRequest `json:"prs,omitempty"`
	ReviewRequests []PullRequest `json:"review_requests,omitempty"`
	Err            string        `json:"error,omitempty"`
}

// FailingPRs counts open PRs whose checks fail.
func (r GitHubRepo) FailingPRs() int {
	n := 0
	for _, pr := range r.PRs {
		if pr.Checks == CIFailure {
			n++
		}
	}
	return n
}

// RateLimit is GitHub's GraphQL budget, reported by the query itself.
type RateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	Cost      int       `json:"cost"`
	ResetAt   time.Time `json:"reset_at"`
}

// GitHubSnapshot is the result of the GitHub collector.
type GitHubSnapshot struct {
	Viewer string       `json:"viewer"`
	Repos  []GitHubRepo `json:"repos"`
	// OtherReviewRequests are review requests in repos that are not
	// registered as projects.
	OtherReviewRequests []PullRequest `json:"other_review_requests,omitempty"`
	RateLimit           RateLimit     `json:"rate_limit"`
}

// GitHub fetches CI, PRs and review requests of every project with a
// github = "owner/repo" in a single GraphQL query.
//
// Note on caching: GitHub's GraphQL endpoint does not support conditional
// requests (ETag/If-None-Match only exist in the REST API). The budget is
// protected instead by asking for one query per interval, reading the
// rateLimit field of the response and pausing until resetAt when it runs low.
type GitHub struct {
	Projects []config.Project
	Every    time.Duration
	// Endpoint overrides the API URL (tests, GitHub Enterprise).
	Endpoint string
	// Token returns the API token. Defaults to $GH_TOKEN, $GITHUB_TOKEN or
	// `gh auth token`, so Atalaya never asks for or stores credentials.
	Token func(ctx context.Context) (string, error)
	Now   func() time.Time

	once      sync.Once
	transport *ghTransport
	client    *githubv4.Client
}

func (g *GitHub) Name() string            { return "github" }
func (g *GitHub) Interval() time.Duration { return g.Every }

func (g *GitHub) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g *GitHub) init() {
	token := g.Token
	if token == nil {
		token = ghToken
	}
	g.transport = &ghTransport{token: token, base: http.DefaultTransport}
	hc := &http.Client{Transport: g.transport, Timeout: 30 * time.Second}
	if g.Endpoint != "" {
		g.client = githubv4.NewEnterpriseClient(g.Endpoint, hc)
	} else {
		g.client = githubv4.NewClient(hc)
	}
}

func (g *GitHub) Collect(ctx context.Context) (Snapshot, error) {
	g.once.Do(g.init)
	var projects []config.Project
	for _, p := range g.Projects {
		if p.GitHub != "" {
			projects = append(projects, p)
		}
	}
	if len(projects) == 0 {
		return GitHubSnapshot{}, nil
	}
	names := make([]string, len(projects))
	for i, p := range projects {
		names[i] = p.GitHub
	}
	q := buildQuery(names)
	err := g.client.Query(ctx, q.Interface(), nil)
	if err != nil {
		if rerr := g.transport.classify(err, g.now()); rerr != nil {
			return nil, rerr
		}
	}
	snap, ok := decodeSnapshot(q.Elem(), projects, g.now())
	if !ok {
		// Nothing decoded: the error is about the whole query.
		if err == nil {
			err = errors.New("respuesta vacía de GitHub")
		}
		return nil, err
	}
	rl := snap.RateLimit
	if rl.Limit > 0 && rl.Remaining < max(rl.Cost, 1)*10 {
		return snap, &RetryAfterError{Err: fmt.Errorf("límite de GitHub casi agotado (%d/%d)", rl.Remaining, rl.Limit), At: rl.ResetAt}
	}
	return snap, nil
}

// ---- query shape ----

type rateLimitNode struct {
	Limit     int
	Remaining int
	Cost      int
	ResetAt   time.Time
}

type rollupNode struct {
	State    string
	Contexts struct {
		Nodes []struct {
			Typename string `graphql:"__typename"`
			CheckRun struct {
				Name       string
				Status     string
				Conclusion string
				DetailsURL string `graphql:"detailsUrl"`
			} `graphql:"... on CheckRun"`
			StatusContext struct {
				Context   string
				State     string
				TargetURL string `graphql:"targetUrl"`
			} `graphql:"... on StatusContext"`
		}
	} `graphql:"contexts(first: 50)"`
}

type suiteNode struct {
	Status      string
	Conclusion  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	WorkflowRun *struct {
		URL      string `graphql:"url"`
		Workflow struct{ Name string }
	}
}

type prNode struct {
	Number         int
	Title          string
	URL            string `graphql:"url"`
	IsDraft        bool
	UpdatedAt      time.Time
	HeadRefName    string
	ReviewDecision string
	Author         *struct{ Login string }
	Repository     struct{ NameWithOwner string }
	Commits        struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct{ State string }
			}
		}
	} `graphql:"commits(last: 1)"`
}

type repoNode struct {
	NameWithOwner    string
	URL              string `graphql:"url"`
	DefaultBranchRef *struct {
		Name   string
		Target struct {
			Commit struct {
				Oid               string
				StatusCheckRollup *rollupNode
				CheckSuites       struct {
					Nodes []suiteNode
				} `graphql:"checkSuites(last: 20)"`
			} `graphql:"... on Commit"`
		}
	}
	PullRequests struct {
		TotalCount int
		Nodes      []prNode
	} `graphql:"pullRequests(states: OPEN, first: 20, orderBy: {field: UPDATED_AT, direction: DESC})"`
}

type searchNode struct {
	Nodes []struct {
		PullRequest prNode `graphql:"... on PullRequest"`
	}
}

const reviewSearch = `search(query: "is:open is:pr review-requested:@me archived:false", type: ISSUE, first: 50)`

func gqlTag(v string) reflect.StructTag { return reflect.StructTag("graphql:" + strconv.Quote(v)) }

func gqlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// buildQuery returns a pointer to a struct shaped like
//
//	{ viewer{login} rateLimit{…} search(…){…}
//	  r0: repository(owner:"a", name:"b"){…}  r1: …  }
//
// githubv4 derives queries from struct types, so a query with one alias per
// repo needs a type built at runtime. One request covers every repo.
func buildQuery(repos []string) reflect.Value {
	fields := []reflect.StructField{
		{Name: "Viewer", Type: reflect.TypeFor[struct{ Login string }]()},
		{Name: "RateLimit", Type: reflect.TypeFor[rateLimitNode]()},
		{Name: "Search", Type: reflect.TypeFor[searchNode](), Tag: gqlTag(reviewSearch)},
	}
	for i, r := range repos {
		owner, name, _ := strings.Cut(r, "/")
		fields = append(fields, reflect.StructField{
			Name: fmt.Sprintf("R%d", i),
			Type: reflect.TypeFor[*repoNode](),
			Tag:  gqlTag(fmt.Sprintf("r%d: repository(owner: %s, name: %s)", i, gqlString(owner), gqlString(name))),
		})
	}
	return reflect.New(reflect.StructOf(fields))
}

// ---- decoding ----

func decodeSnapshot(v reflect.Value, projects []config.Project, now time.Time) (GitHubSnapshot, bool) {
	var snap GitHubSnapshot
	snap.Viewer = v.FieldByName("Viewer").Field(0).String()
	rl := v.FieldByName("RateLimit").Interface().(rateLimitNode)
	snap.RateLimit = RateLimit(rl)
	search := v.FieldByName("Search").Interface().(searchNode)

	decoded := snap.Viewer != ""
	byRepo := map[string]*GitHubRepo{}
	for i, p := range projects {
		gr := GitHubRepo{Project: p.Name, Repo: p.GitHub}
		node, _ := v.Field(3 + i).Interface().(*repoNode)
		if node == nil {
			gr.Err = "repo no encontrado o sin acceso"
		} else {
			decoded = true
			fillRepo(&gr, node, now)
		}
		snap.Repos = append(snap.Repos, gr)
	}
	for i := range snap.Repos {
		byRepo[strings.ToLower(snap.Repos[i].Repo)] = &snap.Repos[i]
	}
	for _, n := range search.Nodes {
		pr := toPR(n.PullRequest)
		if pr.Number == 0 {
			continue
		}
		if r, ok := byRepo[strings.ToLower(pr.Repo)]; ok {
			r.ReviewRequests = append(r.ReviewRequests, pr)
		} else {
			snap.OtherReviewRequests = append(snap.OtherReviewRequests, pr)
		}
	}
	return snap, decoded
}

func toPR(n prNode) PullRequest {
	pr := PullRequest{
		Repo: n.Repository.NameWithOwner, Number: n.Number, Title: n.Title, URL: n.URL,
		Branch: n.HeadRefName, Draft: n.IsDraft, UpdatedAt: n.UpdatedAt,
		ReviewDecision: strings.ToLower(n.ReviewDecision),
	}
	if n.Author != nil {
		pr.Author = n.Author.Login
	}
	if len(n.Commits.Nodes) > 0 {
		if r := n.Commits.Nodes[0].Commit.StatusCheckRollup; r != nil {
			pr.Checks = normalizeState(r.State)
		}
	}
	return pr
}

func fillRepo(gr *GitHubRepo, n *repoNode, now time.Time) {
	gr.URL = n.URL
	gr.OpenPRs = n.PullRequests.TotalCount
	for _, p := range n.PullRequests.Nodes {
		pr := toPR(p)
		if pr.Repo == "" {
			pr.Repo = n.NameWithOwner
		}
		gr.PRs = append(gr.PRs, pr)
	}
	if n.DefaultBranchRef == nil {
		return
	}
	gr.DefaultBranch = n.DefaultBranchRef.Name
	c := n.DefaultBranchRef.Target.Commit
	if c.StatusCheckRollup == nil {
		return
	}
	run := &CIRun{State: normalizeState(c.StatusCheckRollup.State), Commit: shortSHA(c.Oid)}
	gr.FailedChecks = failedChecks(c.StatusCheckRollup)
	lastRun(run, c.CheckSuites.Nodes, now)
	gr.CI = run
}

// lastRun replaces the rollup summary with GitHub Actions data. It takes the
// latest run of each workflow (scheduled workflows add a new suite to the
// same commit every day, so older ones are history) and shows the most
// severe of them: a passing "Dependabot Updates" must not hide a broken CI.
// Suites of other apps are ignored; they often sit in QUEUED forever.
func lastRun(run *CIRun, suites []suiteNode, now time.Time) {
	latest := map[string]*suiteNode{}
	for i := range suites {
		s := &suites[i]
		if s.WorkflowRun == nil {
			continue
		}
		name := s.WorkflowRun.Workflow.Name
		if cur, ok := latest[name]; !ok || s.CreatedAt.After(cur.CreatedAt) {
			latest[name] = s
		}
	}
	var pick *suiteNode
	for _, s := range latest {
		if pick == nil || severity(*s) > severity(*pick) ||
			(severity(*s) == severity(*pick) && s.CreatedAt.After(pick.CreatedAt)) {
			pick = s
		}
	}
	if pick == nil {
		return
	}
	run.State = suiteState(*pick)
	end := pick.UpdatedAt
	if run.State == CIPending {
		end = now
	}
	run.Workflow = pick.WorkflowRun.Workflow.Name
	run.URL = pick.WorkflowRun.URL
	run.StartedAt = pick.CreatedAt
	run.DurationSec = int64(end.Sub(pick.CreatedAt).Seconds())
}

func suiteState(s suiteNode) string {
	switch {
	case s.Status != "COMPLETED":
		return CIPending
	case isFailure(s.Conclusion):
		return CIFailure
	case s.Conclusion == "CANCELLED" || s.Conclusion == "STALE":
		return CICancelled
	}
	return CISuccess
}

func severity(s suiteNode) int {
	switch suiteState(s) {
	case CIFailure:
		return 3
	case CIPending:
		return 2
	case CICancelled:
		return 1
	}
	return 0
}

func failedChecks(r *rollupNode) []FailedCheck {
	var out []FailedCheck
	for _, n := range r.Contexts.Nodes {
		switch n.Typename {
		case "CheckRun":
			if isFailure(n.CheckRun.Conclusion) {
				out = append(out, FailedCheck{Name: n.CheckRun.Name, URL: n.CheckRun.DetailsURL})
			}
		case "StatusContext":
			if s := n.StatusContext.State; s == "FAILURE" || s == "ERROR" {
				out = append(out, FailedCheck{Name: n.StatusContext.Context, URL: n.StatusContext.TargetURL})
			}
		}
	}
	return out
}

func isFailure(conclusion string) bool {
	switch conclusion {
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE", "ACTION_REQUIRED":
		return true
	}
	return false
}

// normalizeState maps StatusState (SUCCESS, FAILURE, ERROR, PENDING,
// EXPECTED) to the three states the UI draws.
func normalizeState(s string) string {
	switch s {
	case "SUCCESS":
		return CISuccess
	case "FAILURE", "ERROR":
		return CIFailure
	case "PENDING", "EXPECTED":
		return CIPending
	}
	return strings.ToLower(s)
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// ---- auth and rate limits ----

// ghToken reuses the GitHub CLI's credentials.
func ghToken(ctx context.Context) (string, error) {
	for _, env := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if t := os.Getenv(env); t != "" {
			return t, nil
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := run(ctx, nil, "gh", "auth", "token")
	if err != nil {
		return "", fmt.Errorf("sin token de GitHub, ejecuta `gh auth login`: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ghTransport adds the token and remembers the rate-limit headers of the
// last response, so a 403/429 can be turned into "retry at …".
type ghTransport struct {
	token func(context.Context) (string, error)
	base  http.RoundTripper

	mu         sync.Mutex
	cached     string
	status     int
	remaining  int
	reset      time.Time
	retryAfter time.Duration
}

func (t *ghTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	tok := t.cached
	t.mu.Unlock()
	if tok == "" {
		var err error
		if tok, err = t.token(req.Context()); err != nil {
			return nil, err
		}
		t.mu.Lock()
		t.cached = tok
		t.mu.Unlock()
	}
	t.mu.Lock()
	t.status = 0 // a network error must not be classified with a stale status
	t.mu.Unlock()
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "bearer "+tok)
	req.Header.Set("User-Agent", "atalaya")
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.status = resp.StatusCode
	t.remaining = -1
	if v, err := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining")); err == nil {
		t.remaining = v
	}
	if v, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		t.reset = time.Unix(v, 0)
	}
	t.retryAfter = 0
	if v, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
		t.retryAfter = time.Duration(v) * time.Second
	}
	if resp.StatusCode == http.StatusUnauthorized {
		t.cached = "" // the token may have been rotated: ask gh again next time
	}
	return resp, nil
}

// classify turns HTTP-level failures into actionable errors. It returns nil
// for GraphQL-level errors, which come with partial data.
func (t *ghTransport) classify(err error, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.status {
	case http.StatusUnauthorized:
		return errors.New("token de GitHub inválido: ejecuta `gh auth login`")
	case http.StatusForbidden, http.StatusTooManyRequests:
		at := now.Add(time.Minute)
		if t.retryAfter > 0 {
			at = now.Add(t.retryAfter)
		} else if t.remaining == 0 && t.reset.After(now) {
			at = t.reset
		}
		return &RetryAfterError{Err: errors.New("límite de peticiones de GitHub"), At: at}
	case http.StatusOK:
		return nil
	}
	return err
}
