package ui

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/store"
)

// Panel is one of the five tabs.
type Panel int

const (
	PanelOverview Panel = iota
	PanelRepos
	PanelGitHub
	PanelHealth
	PanelLocal
	PanelAgents
	PanelActivity
	PanelPlugins
	panelCount
)

var panelNames = [panelCount]string{"Resumen", "Repos", "GitHub", "Producción", "Local", "Agentes", "Actividad", "Plugins"}

// panelSources are the collectors each panel reads, for its status line.
var panelSources = [panelCount][]string{
	nil, // the overview reports every source with an error
	{"git", "fetch"}, {"github"}, {"health"}, {"ports", "docker", "ollama"}, {"agents", "usage"}, {"events"},
	nil, // plugins: one source per configured plugin, see Model.sources
}

// row is one line of a panel. Non-selectable rows are section titles and
// column headers inside the panel.
type row struct {
	key        string
	line       string
	selectable bool
	item       any
}

type panelView struct {
	header string
	rows   []row
	empty  string
}

// cursorWidth is the "› " gutter before every row.
const cursorWidth = 2

func (m Model) contentWidth() int { return max(m.width-cursorWidth, 20) }

// match applies the / filter.
func (m Model) match(fields ...string) bool {
	f := strings.ToLower(strings.TrimSpace(m.filter.Value()))
	if f == "" {
		return true
	}
	for _, s := range fields {
		if strings.Contains(strings.ToLower(s), f) {
			return true
		}
	}
	return false
}

// loading returns the message to show when a source has no data yet.
func (m Model) loading(source string) string {
	e, ok := m.state.Get(source)
	if ok && e.Err != "" {
		return sRed.Render(source + ": " + e.Err)
	}
	return sDim.Render("Cargando…")
}

func (m Model) build(p Panel) panelView {
	switch p {
	case PanelRepos:
		return m.reposPanel()
	case PanelGitHub:
		return m.githubPanel()
	case PanelHealth:
		return m.healthPanel()
	case PanelLocal:
		return m.localPanel()
	case PanelAgents:
		return m.agentsPanel()
	case PanelActivity:
		return m.activityPanel()
	case PanelPlugins:
		return m.pluginsPanel()
	}
	return panelView{}
}

// ---- 1. Repos ----

func (m Model) reposPanel() panelView {
	t := table{cols: []column{
		{title: "PROYECTO", max: 24, min: 8},
		{title: "RAMA", max: 24, min: 10},
		{title: "SYNC", max: 10},
		{title: "CAMBIOS", max: 16},
		{title: "STASH", hideEmpty: true},
		{title: "⎇", hideEmpty: true, drop: 3},
		{title: "HACE", right: true},
		{title: "AUTOR", max: 16, drop: 1}, // first to go on a narrow terminal
		{title: "ÚLTIMO COMMIT", flex: true, min: 24},
	}}
	var pv panelView
	snap, ok := store.Data[collector.GitSnapshot](m.state, "git")
	if !ok {
		pv.empty = m.loading("git")
		return pv
	}
	if len(snap.Repos) == 0 {
		pv.empty = sDim.Render("No hay proyectos con path. Ejecuta `atalaya init ~/tu/carpeta` o edita " + config.ContractHome(m.deps.Config.Path))
		return pv
	}
	var repos []collector.Repo
	for _, r := range snap.Repos {
		if m.match(r.Project, r.Branch) {
			t.add(m.repoCells(r)...)
			repos = append(repos, r)
		}
	}
	ws := t.layout(m.contentWidth())
	pv.header = t.header(ws)
	for i, l := range t.lines(ws) {
		pv.rows = append(pv.rows, row{key: "repo:" + repos[i].Project, selectable: true, item: repos[i], line: l})
	}
	return pv
}

func (m Model) repoCells(r collector.Repo) []string {
	name := r.Project
	switch {
	case r.Err != "":
		return []string{sRed.Render(name), "", "", "", "", "", "", "", sRed.Render(r.Err)}
	case r.OutOfSync():
		name = sRed.Render(name)
	case r.Dirty():
		name = sYellow.Render(name)
	}
	branch := r.Branch
	if r.Detached {
		branch = sDim.Render("@" + r.Branch)
	}
	when, author, subject := "", "", sDim.Render("sin commits")
	if c := r.LastCommit; c != nil {
		when, author, subject = sDim.Render(ago(m.now, c.When)), sDim.Render(c.Author), c.Subject
	}
	worktrees := ""
	if n := len(r.Worktrees); n > 0 {
		worktrees = sMagenta.Render(fmt.Sprint(n))
	}
	return []string{name, branch, syncCell(r), changesCell(r), stashCell(r.Stash), worktrees, when, author, subject}
}

func syncCell(r collector.Repo) string {
	switch {
	case r.Detached:
		return sDim.Render("detached")
	case r.UpstreamGone:
		return sRed.Render("sin remoto")
	case r.Upstream == "":
		return sDim.Render("local")
	case r.Ahead > 0 || r.Behind > 0:
		var parts []string
		if r.Ahead > 0 {
			parts = append(parts, fmt.Sprintf("↑%d", r.Ahead))
		}
		if r.Behind > 0 {
			parts = append(parts, fmt.Sprintf("↓%d", r.Behind))
		}
		return sRed.Render(strings.Join(parts, " "))
	}
	return sGreen.Render("✓")
}

func changesCell(r collector.Repo) string {
	c := r.Changes
	var parts []string
	if c.Conflicts > 0 {
		parts = append(parts, sRed.Render(fmt.Sprintf("!%d", c.Conflicts)))
	}
	if c.Staged > 0 {
		parts = append(parts, sGreen.Render(fmt.Sprintf("+%d", c.Staged)))
	}
	if c.Unstaged > 0 {
		parts = append(parts, sYellow.Render(fmt.Sprintf("~%d", c.Unstaged)))
	}
	if c.Untracked > 0 {
		parts = append(parts, sDim.Render(fmt.Sprintf("?%d", c.Untracked)))
	}
	if len(parts) == 0 {
		return sDim.Render("limpio")
	}
	return strings.Join(parts, " ")
}

func stashCell(n int) string {
	if n == 0 {
		return ""
	}
	return sYellow.Render(fmt.Sprintf("≡%d", n))
}

// ---- 2. GitHub ----

func (m Model) githubPanel() panelView {
	t := table{cols: []column{
		{title: "REPO", max: 24, min: 8},
		{title: "CI", max: 30, min: 12},
		{title: "PRS", right: true},
		{title: "REVIEW", right: true},
		{title: "FALLOS", flex: true, min: 12},
	}}
	var pv panelView
	snap, ok := store.Data[collector.GitHubSnapshot](m.state, "github")
	if !ok {
		pv.empty = m.loading("github")
		return pv
	}
	if len(snap.Repos) == 0 && len(snap.OtherReviewRequests) == 0 {
		pv.empty = sDim.Render(`Ningún proyecto tiene github = "owner/repo" en la config.`)
		return pv
	}
	// PR rows span the whole width; only repo rows go through the table.
	type group struct {
		repo collector.GitHubRepo
		prs  []row
	}
	var groups []group
	for _, r := range snap.Repos {
		if m.match(r.Project, r.Repo) {
			t.add(m.githubCells(r)...)
			groups = append(groups, group{r, m.prRows(r)})
		}
	}
	ws := t.layout(m.contentWidth())
	pv.header = t.header(ws)
	for i, l := range t.lines(ws) {
		g := groups[i]
		pv.rows = append(pv.rows, row{key: "gh:" + g.repo.Repo, selectable: true, item: g.repo, line: l})
		if g.repo.Branch != nil {
			pv.rows = append(pv.rows, m.branchRow(g.repo))
		}
		pv.rows = append(pv.rows, g.prs...)
	}
	others := snap.OtherReviewRequests
	if len(others) > 0 && m.match("otros", "review") {
		pv.rows = append(pv.rows, row{line: ""}, row{line: sSection.Render(fmt.Sprintf("REVIEWS EN OTROS REPOS (%d)", len(others)))})
		for _, pr := range others {
			pv.rows = append(pv.rows, m.prRow(pr, true, true))
		}
	}
	return pv
}

// maxPRRows caps the PRs listed under each repo.
const maxPRRows = 3

// prRows lists a repo's PRs under it: review requests first (they are
// waiting on you) and always shown, then other PRs up to maxPRRows, then one
// row per bot with all its PRs. The detail view has every PR.
func (m Model) prRows(r collector.GitHubRepo) []row {
	requested := map[int]bool{}
	for _, pr := range r.ReviewRequests {
		requested[pr.Number] = true
	}
	prs := slices.Clone(r.PRs)
	for _, pr := range r.ReviewRequests {
		if !slices.ContainsFunc(prs, func(p collector.PullRequest) bool { return p.Number == pr.Number }) {
			prs = append(prs, pr)
		}
	}
	slices.SortStableFunc(prs, func(a, b collector.PullRequest) int {
		switch {
		case requested[a.Number] && !requested[b.Number]:
			return -1
		case requested[b.Number] && !requested[a.Number]:
			return 1
		}
		return 0
	})
	var rows []row
	var botOrder []string
	bots := map[string][]collector.PullRequest{}
	shown, more := 0, 0
	for _, pr := range prs {
		switch {
		case requested[pr.Number]:
			rows = append(rows, m.prRow(pr, true, false))
		case m.deps.Config.IsBot(pr.Author):
			if _, seen := bots[pr.Author]; !seen {
				botOrder = append(botOrder, pr.Author)
			}
			bots[pr.Author] = append(bots[pr.Author], pr)
		case shown < maxPRRows:
			rows = append(rows, m.prRow(pr, false, false))
			shown++
		default:
			more++
		}
	}
	if more > 0 {
		rows = append(rows, row{line: sDim.Render(fmt.Sprintf("     … %d PRs más (enter en el repo)", more))})
	}
	for _, author := range botOrder {
		rows = append(rows, m.botRow(r, author, bots[author]))
	}
	return rows
}

// branchItem is the line of the local branch under its repo.
type branchItem struct {
	Repo   collector.GitHubRepo
	Branch collector.BranchCI
}

// branchRow shows the branch checked out locally, when it is not the
// default one: `↳ feat/x · ⟳ CI 1m20s · PR #12`.
func (m Model) branchRow(r collector.GitHubRepo) row {
	b := *r.Branch
	parts := []string{sDim.Render("↳ ") + b.Name}
	switch {
	case !b.Pushed:
		parts = append(parts, sDim.Render("sin publicar en GitHub"))
	case b.CI == nil:
		parts = append(parts, sDim.Render("sin CI"))
	default:
		parts = append(parts, ciText(b.CI))
	}
	switch {
	case b.PRNumber > 0:
		parts = append(parts, link(b.PRURL, sBlue.Render(fmt.Sprintf("PR #%d", b.PRNumber))))
	case b.Pushed:
		parts = append(parts, sDim.Render("sin PR"))
	}
	l := "   " + strings.Join(parts, sDim.Render(" · "))
	return row{key: "branch:" + r.Repo, selectable: true, item: branchItem{r, b}, line: fit(l, m.contentWidth())}
}

// botGroup is one row for every open PR of a bot in a repo.
type botGroup struct {
	Repo    string
	RepoURL string
	Author  string
	PRs     []collector.PullRequest
}

// botRow summarizes a bot's PRs: `✗ dependabot · 8 PRs (8 con checks rotos)`.
func (m Model) botRow(r collector.GitHubRepo, author string, prs []collector.PullRequest) row {
	var failing, pending int
	for _, pr := range prs {
		switch pr.Checks {
		case collector.CIFailure:
			failing++
		case collector.CIPending:
			pending++
		}
	}
	state, note := collector.CISuccess, ""
	switch {
	case failing > 0:
		state, note = collector.CIFailure, fmt.Sprintf(" (%d con checks rotos)", failing)
	case pending > 0:
		state, note = collector.CIPending, fmt.Sprintf(" (%d en curso)", pending)
	}
	l := fmt.Sprintf("     %s %s%s", ciIcon(state), author, sDim.Render(fmt.Sprintf(" · %d PRs%s", len(prs), note)))
	return row{key: "bots:" + r.Repo + ":" + author, selectable: true,
		item: botGroup{Repo: r.Repo, RepoURL: r.URL, Author: author, PRs: prs}, line: fit(l, m.contentWidth())}
}

// ciText is "✓ CI 3m07s": icon, workflow and duration. Without GitHub
// Actions (only external statuses such as Vercel) it is "✓ checks".
func ciText(run *collector.CIRun) string {
	s := ciIcon(run.State)
	if run.Workflow != "" {
		s += " " + run.Workflow
	} else {
		s += " checks"
	}
	if run.DurationSec > 0 {
		s += " " + sDim.Render(dur(time.Duration(run.DurationSec)*time.Second))
	}
	return link(run.URL, s)
}

// failing reports a broken CI on the default branch or the local one.
func failing(r collector.GitHubRepo) bool {
	return (r.CI != nil && r.CI.State == collector.CIFailure) || len(r.FailedChecks) > 0 ||
		(r.Branch != nil && r.Branch.CI != nil && r.Branch.CI.State == collector.CIFailure)
}

func (m Model) githubCells(r collector.GitHubRepo) []string {
	if r.Err != "" {
		return []string{sRed.Render(r.Project), sDim.Render("—"), "", "", sRed.Render(r.Err)}
	}
	ci := sDim.Render("sin CI")
	if r.CI != nil {
		ci = ciText(r.CI)
	}
	prs := sDim.Render("0")
	if r.OpenPRs > 0 {
		prs = fmt.Sprint(r.OpenPRs)
	}
	review := sDim.Render("0")
	if n := len(r.ReviewRequests); n > 0 {
		review = sYellow.Render(fmt.Sprintf("◉ %d", n))
	}
	var fails []string
	if n := len(r.FailedChecks); n > 0 {
		names := make([]string, 0, n)
		for _, c := range r.FailedChecks {
			names = append(names, c.Name)
		}
		fails = append(fails, sRed.Render(fmt.Sprintf("✗ %s", strings.Join(names, ", "))))
	}
	if n := r.FailingPRs(); n > 0 {
		fails = append(fails, sRed.Render(fmt.Sprintf("%d PR con checks rotos", n)))
	}
	name := r.Project
	if failing(r) {
		name = sRed.Render(name)
	}
	return []string{link(r.URL, name), ci, prs, review, strings.Join(fails, sDim.Render(" · "))}
}

// prRow is a PR under its repo. showRepo names the repo, for PRs listed
// outside of it.
func (m Model) prRow(pr collector.PullRequest, requested, showRepo bool) row {
	mark := "  "
	if requested {
		mark = sYellow.Render("◉ ")
	}
	title := pr.Title
	if pr.Draft {
		title = sDim.Render("[borrador] ") + title
	}
	meta := fmt.Sprintf(" · %s · %s", pr.Author, ago(m.now, pr.UpdatedAt))
	if showRepo {
		meta = " · " + pr.Repo + meta
	}
	l := fmt.Sprintf("   %s%s %s %s%s", mark, ciIcon(pr.Checks), link(pr.URL, sBlue.Render(fmt.Sprintf("#%d", pr.Number))), title, sDim.Render(meta))
	return row{key: "pr:" + pr.URL, selectable: true, item: pr, line: fit(l, m.contentWidth())}
}

// ---- 3. Producción ----

const sparkWidth = 20

func (m Model) healthPanel() panelView {
	t := table{cols: []column{
		{title: "PROYECTO", max: 20, min: 8},
		{title: "ENDPOINT", flex: true, min: 24},
		{title: "ESTADO"},
		{title: "LATENCIA", right: true},
		{title: "24H", right: true},
		{title: "P95", right: true, drop: 1},
		{title: "TENDENCIA", max: sparkWidth, min: 8},
		{title: "TLS", right: true},
	}}
	const spark = 6 // index of TENDENCIA
	var pv panelView
	snap, ok := store.Data[collector.HealthSnapshot](m.state, "health")
	if !ok {
		pv.empty = m.loading("health")
		return pv
	}
	if len(snap.Checks) == 0 {
		pv.empty = sDim.Render(`Ningún proyecto tiene health = ["https://…"] en la config.`)
		return pv
	}
	var checks []collector.Check
	for _, c := range snap.Checks {
		if m.match(c.Project, c.URL) {
			t.add(healthCells(c)...)
			checks = append(checks, c)
		}
	}
	ws := t.layout(m.contentWidth())
	// The sparkline is drawn for the width it got, newest samples kept.
	for i, c := range checks {
		if ws[spark] != hidden {
			t.rows[i][spark] = sparkline(c.History, ws[spark])
		}
	}
	pv.header = t.header(ws)
	for i, l := range t.lines(ws) {
		pv.rows = append(pv.rows, row{key: "health:" + checks[i].URL, selectable: true, item: checks[i], line: l})
	}
	return pv
}

func healthCells(c collector.Check) []string {
	endpoint := link(c.URL, c.URL)
	status := sGreen.Render(fmt.Sprintf("● %d", c.Status))
	if !c.OK {
		if c.Status > 0 {
			status = sRed.Render(fmt.Sprintf("● %d", c.Status))
		} else {
			status = sRed.Render("● caído")
			endpoint += sRed.Render("  " + c.Err)
		}
	}
	latency := fmt.Sprintf("%d ms", c.LatencyMS)
	switch {
	case c.Status == 0:
		latency = sDim.Render("—") // no response: there is no latency to show
	case c.LatencyMS > 1000:
		latency = sYellow.Render(latency)
	}
	// Placeholder as wide as the sparkline will want; drawn after layout.
	spark := strings.Repeat(" ", min(len(c.History), sparkWidth))
	uptime, p95 := sDim.Render("—"), sDim.Render("—")
	if s := c.Stats; s != nil {
		uptime = uptimeCell(s.Uptime24h)
		if s.P95MS > 0 {
			p95 = sDim.Render(fmt.Sprintf("%d ms", s.P95MS))
		}
	}
	return []string{c.Project, endpoint, status, latency, uptime, p95, spark, tlsCell(c.TLS)}
}

// uptimeCell is "99.9%": green from three nines down to 99%, then yellow,
// then red under 95%.
func uptimeCell(pct float64) string {
	if pct < 0 {
		return sDim.Render("—")
	}
	// Truncated, never rounded up: 99.95% is not "100.0%".
	s := fmt.Sprintf("%.1f%%", math.Floor(pct*10)/10)
	if pct == 100 {
		s = "100%"
	}
	switch {
	case pct >= 99:
		return sGreen.Render(s)
	case pct >= 95:
		return sYellow.Render(s)
	}
	return sRed.Render(s)
}

func tlsCell(t *collector.TLSInfo) string {
	if t == nil {
		return sDim.Render("—")
	}
	s := fmt.Sprintf("%d d", t.DaysLeft)
	switch {
	case t.DaysLeft < 7:
		return sRed.Render(s)
	case t.DaysLeft < 21:
		return sYellow.Render(s)
	}
	return sGreen.Render(s)
}

// ---- 4. Local ----

func (m Model) localPanel() panelView {
	var pv panelView
	w := m.contentWidth()

	section := func(title string, n int, note string) {
		t := sSection.Render(title)
		if note != "" {
			t += sDim.Render(" · " + note)
		} else {
			t += sDim.Render(fmt.Sprintf(" (%d)", n))
		}
		if len(pv.rows) > 0 {
			pv.rows = append(pv.rows, row{line: ""})
		}
		pv.rows = append(pv.rows, row{line: t})
	}
	// emit adds a section's table: column titles, then one row per item.
	emit := func(t *table, keys []string, items []any) {
		if len(items) == 0 {
			return
		}
		ws := t.layout(w)
		pv.rows = append(pv.rows, row{line: t.header(ws)})
		for i, l := range t.lines(ws) {
			pv.rows = append(pv.rows, row{key: keys[i], selectable: true, item: items[i], line: l})
		}
	}

	// Ports: `:3000 → node · odomto`.
	if ports, ok := store.Data[collector.PortsSnapshot](m.state, "ports"); ok {
		t := table{cols: []column{
			{title: "PUERTO"},
			{title: "PROCESO", max: 18, min: 8},
			{title: "PID", right: true, drop: 2},
			{title: "PROYECTO", max: 16, min: 8},
			{title: "DIRECTORIO", flex: true, min: 12, drop: 1},
		}}
		var keys []string
		var items []any
		for _, p := range ports.Ports {
			if !m.match(p.Project, p.Process, fmt.Sprint(p.Port)) {
				continue
			}
			project := sDim.Render("—")
			if p.Project != "" {
				project = sCyan.Render(p.Project)
			}
			t.add(link(fmt.Sprintf("http://localhost:%d", p.Port), sBold.Render(fmt.Sprintf(":%d", p.Port))), "→ "+p.Process, sDim.Render(fmt.Sprint(p.PID)),
				project, sDim.Render(config.ContractHome(p.Cwd)))
			keys = append(keys, fmt.Sprintf("port:%d:%d", p.Port, p.PID))
			items = append(items, p)
		}
		section("PUERTOS", len(items), "")
		emit(&t, keys, items)
	} else {
		section("PUERTOS", 0, ansi.Strip(m.loading("ports")))
	}

	// Docker.
	if d, ok := store.Data[collector.DockerSnapshot](m.state, "docker"); ok && d.Available {
		t := table{cols: []column{
			{title: "CONTENEDOR", max: 24, min: 10},
			{title: "IMAGEN", max: 24, min: 10},
			{title: "ESTADO", max: 18, min: 8},
			{title: "PROYECTO", max: 16, min: 8},
			{title: "PUERTOS", flex: true, min: 10, drop: 1},
		}}
		var keys []string
		var items []any
		for _, c := range d.Containers {
			if !m.match(c.Project, c.Name, c.Image) {
				continue
			}
			state := sGreen.Render(c.Status)
			if c.State != "running" {
				state = sYellow.Render(c.Status)
			}
			t.add(c.Name, sDim.Render(c.Image), state, sCyan.Render(c.Project), sDim.Render(c.Ports))
			keys = append(keys, "docker:"+c.ID)
			items = append(items, c)
		}
		section("DOCKER", len(items), "")
		emit(&t, keys, items)
	} else if ok {
		section("DOCKER", 0, d.Note)
	} else {
		section("DOCKER", 0, ansi.Strip(m.loading("docker")))
	}

	// Ollama.
	if o, ok := store.Data[collector.OllamaSnapshot](m.state, "ollama"); ok && o.Available {
		t := table{cols: []column{
			{title: "MODELO", max: 30, min: 12},
			{title: "PARÁMETROS", max: 16, drop: 1},
			{title: "VRAM", right: true},
			{title: "SE DESCARGA", flex: true, min: 8},
		}}
		var keys []string
		var items []any
		for _, md := range o.Models {
			if !m.match(md.Name) {
				continue
			}
			expires := sDim.Render("—")
			if !md.ExpiresAt.IsZero() {
				expires = "en " + dur(md.ExpiresAt.Sub(m.now))
			}
			t.add(md.Name, sDim.Render(strings.TrimSpace(md.Params+" "+md.Quant)), size(md.VRAMBytes), expires)
			keys = append(keys, "ollama:"+md.Name)
			items = append(items, md)
		}
		note := ""
		if len(o.Models) == 0 {
			note = "corriendo, sin modelos cargados"
		}
		section("OLLAMA", len(items), note)
		emit(&t, keys, items)
	} else if ok {
		section("OLLAMA", 0, o.Note)
	} else {
		section("OLLAMA", 0, ansi.Strip(m.loading("ollama")))
	}
	return pv
}

// ---- 5. Agentes ----

func (m Model) agentsPanel() panelView {
	t := table{cols: []column{
		{title: "AGENTE", max: 12},
		{title: "PROYECTO", max: 16, min: 8},
		{title: "RAMA", max: 24, min: 8, hideEmpty: true, drop: 2},
		{title: "ESTADO"},
		{title: "ACTIVIDAD", flex: true, min: 16},
		{title: "TOKENS", right: true, hideEmpty: true, drop: 3},
		{title: "≈ $ SESIÓN", right: true, hideEmpty: true}, // lifetime, not the 7-day block
		{title: "CPU", right: true, drop: 1},
		{title: "DESDE", right: true},
	}}
	var pv panelView
	usage, hasUsage := store.Data[collector.UsageSnapshot](m.state, "usage")
	if hasUsage {
		pv.rows = append(pv.rows, m.usageRows(usage)...)
	}
	snap, ok := store.Data[collector.AgentsSnapshot](m.state, "agents")
	switch {
	case !ok:
		pv.rows = append(pv.rows, row{line: m.loading("agents")})
		return pv
	case len(snap.Agents) == 0:
		pv.rows = append(pv.rows, row{line: sDim.Render("No hay agentes corriendo (patrones: " + strings.Join(m.deps.Config.Agents.Patterns, ", ") + ").")})
		return pv
	}
	bySession := map[string]collector.SessionUsage{}
	for _, su := range usage.Sessions {
		bySession[su.ID] = su
	}
	var agents []collector.Agent
	for _, a := range snap.Agents {
		title := ""
		if a.Session != nil {
			title = a.Session.Title
		}
		if !m.match(a.Project, a.Name, a.Cwd, a.Branch, a.Worktree, title) {
			continue
		}
		project := sDim.Render("—")
		if a.Project != "" {
			project = sCyan.Render(a.Project)
		}
		tokens, cost := "", ""
		if a.Session != nil {
			if su, ok := bySession[a.Session.ID]; ok {
				tokens, cost = sDim.Render(tokenCount(su.Tokens.Total())), money(su.CostUSD)
			}
		}
		t.add(sBold.Render(a.Name), project, placeCell(a), sessionState(a.Session), activityCell(a),
			tokens, cost, sDim.Render(fmt.Sprintf("%.0f%%", a.CPU)), dur(m.now.Sub(a.Started)))
		agents = append(agents, a)
	}
	ws := t.layout(m.contentWidth())
	pv.rows = append(pv.rows, row{line: t.header(ws)})
	for i, l := range t.lines(ws) {
		pv.rows = append(pv.rows, row{key: fmt.Sprintf("agent:%d", agents[i].PID), selectable: true, item: agents[i], line: l})
	}
	return pv
}

// usageRows is the spend block on top of the Agents panel: today, the
// last days as bars, and the projects that cost the most.
func (m Model) usageRows(u collector.UsageSnapshot) []row {
	var costs []float64
	var week float64
	for _, d := range u.Days {
		costs = append(costs, d.CostUSD)
		week += d.CostUSD
	}
	head := sSection.Render("COSTE ≈ API") + "  hoy " + sBold.Render(money(u.Today.CostUSD)) +
		sDim.Render(" · "+tokenCount(u.Today.Tokens.Total())+" tokens") +
		"   " + fmt.Sprint(len(u.Days)) + " días " + sBold.Render(money(week)) + "  " + sMagenta.Render(bars(costs))
	rows := []row{{line: fit(head, m.contentWidth())}}
	top := u.Projects
	if len(top) > 4 {
		top = top[:4]
	}
	maxCost := 0.0
	for _, p := range top {
		maxCost = max(maxCost, p.CostUSD)
	}
	for _, p := range top {
		width := 0
		if maxCost > 0 {
			width = int(p.CostUSD / maxCost * 20)
		}
		bar := sMagenta.Render(strings.Repeat("█", max(width, 1))) + sDim.Render(strings.Repeat("░", 20-max(width, 1)))
		rows = append(rows, row{line: fit(fmt.Sprintf("  %s %s %s", fit(sCyan.Render(p.Project), 14), bar, money(p.CostUSD)), m.contentWidth())})
	}
	return append(rows, row{line: ""})
}

// money is "$41.20" ("$0.04" for small amounts).
func money(usd float64) string {
	if usd >= 1000 {
		return fmt.Sprintf("$%.0f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

// tokenCount is "1.2M", "450k" or "980".
func tokenCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// bars draws values as block heights relative to the largest.
func bars(values []float64) string {
	hi := 0.0
	for _, v := range values {
		hi = max(hi, v)
	}
	var b strings.Builder
	for _, v := range values {
		i := 0
		if hi > 0 {
			i = int(v / hi * float64(len(sparkBlocks)-1))
		}
		b.WriteRune(sparkBlocks[i])
	}
	return b.String()
}

// placeCell is the branch, or "⎇ name" when the agent works in a linked
// worktree.
func placeCell(a collector.Agent) string {
	if a.Worktree != "" {
		return sMagenta.Render("⎇ " + a.Worktree)
	}
	return a.Branch
}

// sessionState is the icon and word for what an agent is doing.
func sessionState(s *collector.Session) string {
	if s == nil {
		return sDim.Render("—")
	}
	switch s.State {
	case collector.SessionWaiting:
		return sYellow.Render("◉ te espera")
	case collector.SessionBlocked:
		return sRed.Render("⏸ ¿permiso?")
	}
	return sGreen.Render("⟳ trabajando")
}

// activityCell says what a working agent is doing, or the session's title
// when it is waiting.
func activityCell(a collector.Agent) string {
	s := a.Session
	switch {
	case s == nil:
		return sDim.Render(config.ContractHome(a.Cwd))
	case s.State == collector.SessionWaiting:
		return sDim.Render(cmpOr(s.Title, "turno terminado"))
	case s.Title != "":
		return s.Activity + sDim.Render(" · "+s.Title)
	}
	return s.Activity
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- 6. Actividad ----

func (m Model) activityPanel() panelView {
	t := table{cols: []column{
		{title: "CUÁNDO", right: true},
		{title: ""},
		{title: "PROYECTO", max: 16, min: 6, drop: 1},
		{title: "EVENTO", max: 44, min: 16},
		{title: "DETALLE", flex: true, min: 10, drop: 2},
	}}
	var pv panelView
	snap, ok := store.Data[events.Snapshot](m.state, "events")
	if !ok || len(snap.Events) == 0 {
		pv.empty = sDim.Render("Todavía no ha pasado nada: aquí aparecerán CI rotos y arreglados, caídas, reviews, PRs y agentes.")
		return pv
	}
	var evs []events.Event
	for _, e := range snap.Events {
		if !m.match(e.Project, e.Title, e.Body, e.Kind) {
			continue
		}
		t.add(sDim.Render(when(m.now, e.At)), eventIcon(e), sCyan.Render(e.Project), link(e.URL, e.Title), sDim.Render(e.Body))
		evs = append(evs, e)
	}
	ws := t.layout(m.contentWidth())
	pv.header = t.header(ws)
	for i, l := range t.lines(ws) {
		pv.rows = append(pv.rows, row{key: fmt.Sprintf("event:%d:%s", evs[i].At.UnixNano(), evs[i].Kind),
			selectable: true, item: evs[i], line: l})
	}
	return pv
}

// when is "hace 3m" today and a date before.
func when(now, t time.Time) string {
	if now.Sub(t) < 24*time.Hour {
		return hace(now, t)
	}
	return t.Local().Format("02/01 15:04")
}

func eventIcon(e events.Event) string {
	switch {
	case e.Kind == "agent_blocked":
		return sRed.Render("⏸")
	case e.Severity == events.Bad:
		return sRed.Render("✗")
	case e.Severity == events.Good:
		return sGreen.Render("✓")
	case e.Severity == events.Ask:
		return sYellow.Render("◉")
	}
	return sDim.Render("·")
}

// unseen counts the events that matter (not plain info) since the
// Activity panel was last open.
func (m Model) unseen() int {
	snap, ok := store.Data[events.Snapshot](m.state, "events")
	if !ok {
		return 0
	}
	n := 0
	for _, e := range snap.Events {
		if e.At.After(m.seenUntil) && e.Severity != events.Info {
			n++
		}
	}
	return n
}

// ---- 7. Plugins ----

// pluginItem is a row of a plugin's table.
type pluginItem struct {
	Plugin  string
	Title   string
	Columns []string
	Row     collector.PluginRow
}

func (m Model) pluginsPanel() panelView {
	var pv panelView
	if len(m.deps.Config.Plugins) == 0 {
		pv.empty = sDim.Render("Sin plugins. Un plugin es cualquier programa que imprima una tabla en JSON: " +
			"añade [[plugin]] a la config (ejemplos en examples/plugins).")
		return pv
	}
	w := m.contentWidth()
	for _, p := range m.deps.Config.Plugins {
		src := collector.PluginSource(p.Name)
		if len(pv.rows) > 0 {
			pv.rows = append(pv.rows, row{line: ""})
		}
		s, ok := store.Data[collector.PluginSnapshot](m.state, src)
		if !ok {
			note := sDim.Render(" · Cargando…")
			if e, _ := m.state.Get(src); e.Err != "" {
				note = sRed.Render(" · " + e.Err)
			}
			pv.rows = append(pv.rows, row{line: sSection.Render(strings.ToUpper(p.Name)) + note})
			continue
		}
		title := sSection.Render(strings.ToUpper(s.Title))
		if s.Summary != "" {
			title += sDim.Render(" · " + s.Summary)
		}
		if e, _ := m.state.Get(src); e.Err != "" {
			title += sRed.Render(" · " + e.Err)
		}
		pv.rows = append(pv.rows, row{line: title})
		cols := []column{{title: ""}}
		for i, c := range s.Columns {
			col := column{title: c, max: 40, min: 6}
			if i == len(s.Columns)-1 {
				col = column{title: c, flex: true, min: 10}
			}
			cols = append(cols, col)
		}
		t := table{cols: cols}
		var items []pluginItem
		for _, r := range s.Rows {
			if !m.match(append([]string{r.Project}, r.Cells...)...) {
				continue
			}
			cells := []string{pluginIcon(r.Status)}
			for i, c := range r.Cells {
				if i == 0 {
					c = link(r.URL, c)
				}
				cells = append(cells, c)
			}
			t.add(cells...)
			items = append(items, pluginItem{Plugin: p.Name, Title: s.Title, Columns: s.Columns, Row: r})
		}
		if len(items) == 0 {
			continue
		}
		ws := t.layout(w)
		pv.rows = append(pv.rows, row{line: t.header(ws)})
		for i, l := range t.lines(ws) {
			pv.rows = append(pv.rows, row{key: fmt.Sprintf("plugin:%s:%d", p.Name, i), selectable: true, item: items[i], line: l})
		}
	}
	return pv
}

func pluginIcon(status string) string {
	switch status {
	case collector.PluginOK:
		return sGreen.Render("✓")
	case collector.PluginWarn:
		return sYellow.Render("●")
	case collector.PluginError:
		return sRed.Render("✗")
	}
	return sDim.Render("·")
}

// ---- tab badges ----

// badge summarizes what needs attention in a panel, for its tab label.
func (m Model) badge(p Panel) string {
	switch p {
	case PanelRepos:
		if s, ok := store.Data[collector.GitSnapshot](m.state, "git"); ok {
			red, yellow := 0, 0
			for _, r := range s.Repos {
				switch {
				case r.Err != "" || r.OutOfSync():
					red++
				case r.Dirty():
					yellow++
				}
			}
			return badges(count("●", red, sRed), count("●", yellow, sYellow))
		}
	case PanelGitHub:
		if s, ok := store.Data[collector.GitHubSnapshot](m.state, "github"); ok {
			fail, review := 0, len(s.OtherReviewRequests)
			for _, r := range s.Repos {
				if failing(r) {
					fail++
				}
				review += len(r.ReviewRequests)
			}
			return badges(count("✗", fail, sRed), count("◉", review, sYellow))
		}
	case PanelHealth:
		if s, ok := store.Data[collector.HealthSnapshot](m.state, "health"); ok {
			down := 0
			for _, c := range s.Checks {
				if !c.OK {
					down++
				}
			}
			return count("✗", down, sRed)
		}
	case PanelAgents:
		if s, ok := store.Data[collector.AgentsSnapshot](m.state, "agents"); ok {
			waiting, blocked := agentAttention(s)
			return badges(count("●", len(s.Agents), sCyan), count("◉", waiting, sYellow), count("⏸", blocked, sRed))
		}
	case PanelActivity:
		return count("●", m.unseen(), sYellow)
	case PanelPlugins:
		errs, warns := 0, 0
		for _, p := range m.deps.Config.Plugins {
			if s, ok := store.Data[collector.PluginSnapshot](m.state, collector.PluginSource(p.Name)); ok {
				for _, r := range s.Rows {
					switch r.Status {
					case collector.PluginError:
						errs++
					case collector.PluginWarn:
						warns++
					}
				}
			}
		}
		return badges(count("✗", errs, sRed), count("●", warns, sYellow))
	}
	return ""
}

// agentAttention counts agents waiting for you and agents that look stuck.
func agentAttention(s collector.AgentsSnapshot) (waiting, blocked int) {
	for _, a := range s.Agents {
		if a.Session == nil {
			continue
		}
		switch a.Session.State {
		case collector.SessionWaiting:
			waiting++
		case collector.SessionBlocked:
			blocked++
		}
	}
	return waiting, blocked
}

func badges(parts ...string) string {
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " ")
}

// count is a tab badge such as ✗2. The symbol keeps it from reading as the
// next tab's number.
func count(symbol string, n int, s lipgloss.Style) string {
	if n == 0 {
		return ""
	}
	return s.Render(fmt.Sprintf("%s%d", symbol, n))
}
