package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/store"
)

// Panel is one of the five tabs.
type Panel int

const (
	PanelRepos Panel = iota
	PanelGitHub
	PanelHealth
	PanelLocal
	PanelAgents
	panelCount
)

var panelNames = [panelCount]string{"Repos", "GitHub", "Producción", "Local", "Agentes"}

// panelSources are the collectors each panel reads, for its status line.
var panelSources = [panelCount][]string{
	{"git", "fetch"}, {"github"}, {"health"}, {"ports", "docker", "ollama"}, {"agents"},
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

// nameWidth sizes the project column to the longest project name.
func (m Model) nameWidth() int {
	w := 8
	for _, p := range m.deps.Config.Projects {
		w = max(w, ansi.StringWidth(p.Name))
	}
	return min(w, 24)
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
	}
	return panelView{}
}

// ---- 1. Repos ----

func (m Model) reposPanel() panelView {
	cols := []column{{title: "PROYECTO", width: m.nameWidth()}, {title: "RAMA", width: 20}, {title: "SYNC", width: 9},
		{title: "CAMBIOS", width: 13}, {title: "STASH", width: 5}, {title: "ÚLTIMO COMMIT"}}
	ws := widths(cols, m.contentWidth())
	pv := panelView{header: header(cols, ws)}
	snap, ok := store.Data[collector.GitSnapshot](m.state, "git")
	if !ok {
		pv.empty = m.loading("git")
		return pv
	}
	if len(snap.Repos) == 0 {
		pv.empty = sDim.Render("No hay proyectos con path. Ejecuta `atalaya init ~/tu/carpeta` o edita " + config.ContractHome(m.deps.Config.Path))
		return pv
	}
	for _, r := range snap.Repos {
		if !m.match(r.Project, r.Branch) {
			continue
		}
		pv.rows = append(pv.rows, row{key: "repo:" + r.Project, selectable: true, item: r, line: line(cols, ws, m.repoCells(r)...)})
	}
	return pv
}

func (m Model) repoCells(r collector.Repo) []string {
	name := r.Project
	switch {
	case r.Err != "":
		return []string{sRed.Render(name), "", "", "", "", sRed.Render(r.Err)}
	case r.OutOfSync():
		name = sRed.Render(name)
	case r.Dirty():
		name = sYellow.Render(name)
	}
	branch := r.Branch
	if r.Detached {
		branch = sDim.Render("@" + r.Branch)
	}
	return []string{name, branch, syncCell(r), changesCell(r), stashCell(r.Stash), m.commitCell(r.LastCommit)}
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

func (m Model) commitCell(c *collector.Commit) string {
	if c == nil {
		return sDim.Render("sin commits")
	}
	return sDim.Render(fmt.Sprintf("%-5s %s ·", ago(m.now, c.When), c.Author)) + " " + c.Subject
}

// ---- 2. GitHub ----

func (m Model) githubPanel() panelView {
	cols := []column{{title: "REPO", width: m.nameWidth()}, {title: "CI", width: 26}, {title: "PRS", width: 4, right: true},
		{title: "REVIEW", width: 6, right: true}, {title: "FALLOS"}}
	ws := widths(cols, m.contentWidth())
	pv := panelView{header: header(cols, ws)}
	snap, ok := store.Data[collector.GitHubSnapshot](m.state, "github")
	if !ok {
		pv.empty = m.loading("github")
		return pv
	}
	if len(snap.Repos) == 0 && len(snap.OtherReviewRequests) == 0 {
		pv.empty = sDim.Render(`Ningún proyecto tiene github = "owner/repo" en la config.`)
		return pv
	}
	for _, r := range snap.Repos {
		if !m.match(r.Project, r.Repo) {
			continue
		}
		pv.rows = append(pv.rows, row{key: "gh:" + r.Repo, selectable: true, item: r, line: line(cols, ws, m.githubCells(r)...)})
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
		// Review requests first: they are waiting on you.
		slices.SortStableFunc(prs, func(a, b collector.PullRequest) int {
			switch {
			case requested[a.Number] && !requested[b.Number]:
				return -1
			case requested[b.Number] && !requested[a.Number]:
				return 1
			}
			return 0
		})
		// Requested reviews are always listed; other PRs up to a few, the
		// detail view (enter) has all of them.
		shown := 0
		for _, pr := range prs {
			if !requested[pr.Number] {
				if shown == maxPRRows {
					pv.rows = append(pv.rows, row{line: sDim.Render(fmt.Sprintf("     … %d PRs más (enter en el repo)", len(prs)-len(r.ReviewRequests)-shown))})
					break
				}
				shown++
			}
			pv.rows = append(pv.rows, m.prRow(pr, requested[pr.Number], false))
		}
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

func (m Model) githubCells(r collector.GitHubRepo) []string {
	if r.Err != "" {
		return []string{sRed.Render(r.Project), sRed.Render(r.Err)}
	}
	ci := sDim.Render("sin CI")
	if r.CI != nil {
		ci = ciIcon(r.CI.State)
		if r.CI.Workflow != "" {
			ci += " " + r.CI.Workflow
		}
		if r.CI.DurationSec > 0 {
			ci += " " + sDim.Render(dur(time.Duration(r.CI.DurationSec)*time.Second))
		}
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
	if (r.CI != nil && r.CI.State == collector.CIFailure) || len(r.FailedChecks) > 0 {
		name = sRed.Render(name)
	}
	return []string{name, ci, prs, review, strings.Join(fails, sDim.Render(" · "))}
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
	l := fmt.Sprintf("   %s%s %s %s%s", mark, ciIcon(pr.Checks), sBlue.Render(fmt.Sprintf("#%d", pr.Number)), title, sDim.Render(meta))
	return row{key: "pr:" + pr.URL, selectable: true, item: pr, line: fit(l, m.contentWidth())}
}

// ---- 3. Producción ----

const sparkWidth = 20

func (m Model) healthPanel() panelView {
	cols := []column{{title: "PROYECTO", width: m.nameWidth()}, {title: "ENDPOINT"}, {title: "ESTADO", width: 7},
		{title: "LATENCIA", width: 8, right: true}, {title: "ÚLTIMOS MINUTOS", width: sparkWidth}, {title: "TLS", width: 6, right: true}}
	ws := widths(cols, m.contentWidth())
	pv := panelView{header: header(cols, ws)}
	snap, ok := store.Data[collector.HealthSnapshot](m.state, "health")
	if !ok {
		pv.empty = m.loading("health")
		return pv
	}
	if len(snap.Checks) == 0 {
		pv.empty = sDim.Render(`Ningún proyecto tiene health = ["https://…"] en la config.`)
		return pv
	}
	for _, c := range snap.Checks {
		if !m.match(c.Project, c.URL) {
			continue
		}
		pv.rows = append(pv.rows, row{key: "health:" + c.URL, selectable: true, item: c, line: line(cols, ws, healthCells(c)...)})
	}
	return pv
}

func healthCells(c collector.Check) []string {
	endpoint := c.URL
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
	return []string{c.Project, endpoint, status, latency, sparkline(c.History, sparkWidth), tlsCell(c.TLS)}
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
	nw := m.nameWidth()

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

	// Ports.
	pcols := []column{{title: "PUERTO", width: 7}, {title: "PROCESO", width: 18}, {title: "PID", width: 6, right: true},
		{title: "PROYECTO", width: nw}, {title: "DIRECTORIO"}}
	pws := widths(pcols, w)
	if ports, ok := store.Data[collector.PortsSnapshot](m.state, "ports"); ok {
		var rows []row
		for _, p := range ports.Ports {
			if !m.match(p.Project, p.Process, fmt.Sprint(p.Port)) {
				continue
			}
			project := sDim.Render("—")
			if p.Project != "" {
				project = sCyan.Render(p.Project)
			}
			rows = append(rows, row{key: fmt.Sprintf("port:%d:%d", p.Port, p.PID), selectable: true, item: p,
				line: line(pcols, pws, sBold.Render(fmt.Sprintf(":%d", p.Port)), "→ "+p.Process, sDim.Render(fmt.Sprint(p.PID)),
					project, sDim.Render(config.ContractHome(p.Cwd)))})
		}
		section("PUERTOS", len(rows), "")
		if len(rows) > 0 {
			pv.rows = append(pv.rows, row{line: header(pcols, pws)})
		}
		pv.rows = append(pv.rows, rows...)
	} else {
		section("PUERTOS", 0, ansi.Strip(m.loading("ports")))
	}

	// Docker.
	dcols := []column{{title: "CONTENEDOR", width: 20}, {title: "IMAGEN", width: 22}, {title: "ESTADO", width: 18},
		{title: "PROYECTO", width: nw}, {title: "PUERTOS"}}
	dws := widths(dcols, w)
	if d, ok := store.Data[collector.DockerSnapshot](m.state, "docker"); ok && d.Available {
		var rows []row
		for _, c := range d.Containers {
			if !m.match(c.Project, c.Name, c.Image) {
				continue
			}
			state := sGreen.Render(c.Status)
			if c.State != "running" {
				state = sYellow.Render(c.Status)
			}
			rows = append(rows, row{key: "docker:" + c.ID, selectable: true, item: c,
				line: line(dcols, dws, c.Name, sDim.Render(c.Image), state, sCyan.Render(c.Project), sDim.Render(c.Ports))})
		}
		section("DOCKER", len(rows), "")
		if len(rows) > 0 {
			pv.rows = append(pv.rows, row{line: header(dcols, dws)})
		}
		pv.rows = append(pv.rows, rows...)
	} else if ok {
		section("DOCKER", 0, d.Note)
	} else {
		section("DOCKER", 0, ansi.Strip(m.loading("docker")))
	}

	// Ollama.
	ocols := []column{{title: "MODELO", width: 28}, {title: "PARÁMETROS", width: 16}, {title: "VRAM", width: 9, right: true}, {title: "SE DESCARGA"}}
	ows := widths(ocols, w)
	if o, ok := store.Data[collector.OllamaSnapshot](m.state, "ollama"); ok && o.Available {
		var rows []row
		for _, md := range o.Models {
			if !m.match(md.Name) {
				continue
			}
			expires := sDim.Render("—")
			if !md.ExpiresAt.IsZero() {
				expires = "en " + dur(md.ExpiresAt.Sub(m.now))
			}
			rows = append(rows, row{key: "ollama:" + md.Name, selectable: true, item: md,
				line: line(ocols, ows, md.Name, sDim.Render(strings.TrimSpace(md.Params+" "+md.Quant)), size(md.VRAMBytes), expires)})
		}
		note := ""
		if len(o.Models) == 0 {
			note = "corriendo, sin modelos cargados"
		}
		section("OLLAMA", len(rows), note)
		if len(rows) > 0 {
			pv.rows = append(pv.rows, row{line: header(ocols, ows)})
		}
		pv.rows = append(pv.rows, rows...)
	} else if ok {
		section("OLLAMA", 0, o.Note)
	} else {
		section("OLLAMA", 0, ansi.Strip(m.loading("ollama")))
	}
	return pv
}

// ---- 5. Agentes ----

func (m Model) agentsPanel() panelView {
	cols := []column{{title: "AGENTE", width: 10}, {title: "PROYECTO", width: m.nameWidth()}, {title: "DIRECTORIO"},
		{title: "PID", width: 6, right: true}, {title: "DESDE", width: 7, right: true}}
	ws := widths(cols, m.contentWidth())
	pv := panelView{header: header(cols, ws)}
	snap, ok := store.Data[collector.AgentsSnapshot](m.state, "agents")
	if !ok {
		pv.empty = m.loading("agents")
		return pv
	}
	if len(snap.Agents) == 0 {
		pv.empty = sDim.Render("No hay agentes corriendo (patrones: " + strings.Join(m.deps.Config.Agents.Patterns, ", ") + ").")
		return pv
	}
	for _, a := range snap.Agents {
		if !m.match(a.Project, a.Name, a.Cwd) {
			continue
		}
		project := sDim.Render("—")
		if a.Project != "" {
			project = sCyan.Render(a.Project)
		}
		pv.rows = append(pv.rows, row{key: fmt.Sprintf("agent:%d", a.PID), selectable: true, item: a,
			line: line(cols, ws, sBold.Render(a.Name), project, sDim.Render(config.ContractHome(a.Cwd)),
				sDim.Render(fmt.Sprint(a.PID)), dur(m.now.Sub(a.Started)))})
	}
	return pv
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
				if (r.CI != nil && r.CI.State == collector.CIFailure) || len(r.FailedChecks) > 0 {
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
			return count("●", len(s.Agents), sCyan)
		}
	}
	return ""
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
