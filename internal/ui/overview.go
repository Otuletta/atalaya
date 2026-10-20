package ui

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/store"
)

// card is one tile of the overview: a panel reduced to what needs
// attention. enter on a card opens its panel.
type card struct {
	panel Panel
	lines []string
}

func (m Model) cards() []card {
	cards := []card{m.reposCard(), m.githubCard(), m.healthCard(), m.localCard(), m.agentsCard(), m.activityCard()}
	if len(m.deps.Config.Plugins) > 0 {
		cards = append(cards, m.pluginsCard())
	}
	return cards
}

// pluginsCard has one line per plugin: its worst status, title and summary.
func (m Model) pluginsCard() card {
	c := card{panel: PanelPlugins}
	for _, p := range m.deps.Config.Plugins {
		s, ok := store.Data[collector.PluginSnapshot](m.state, collector.PluginSource(p.Name))
		if !ok {
			if e, _ := m.state.Get(collector.PluginSource(p.Name)); e.Err != "" {
				c.lines = append(c.lines, sRed.Render("✗ "+p.Name)+sDim.Render(" · "+e.Err))
			} else {
				c.lines = append(c.lines, sDim.Render(p.Name+" …"))
			}
			continue
		}
		worst := collector.PluginOK
		for _, r := range s.Rows {
			if pluginRank(r.Status) > pluginRank(worst) {
				worst = r.Status
			}
		}
		c.lines = append(c.lines, pluginIcon(worst)+" "+s.Title+sDim.Render(" · "+s.Summary))
	}
	return c
}

func pluginRank(status string) int {
	switch status {
	case collector.PluginError:
		return 3
	case collector.PluginWarn:
		return 2
	case collector.PluginOK:
		return 1
	}
	return 0
}

func (m Model) activityCard() card {
	c := card{panel: PanelActivity}
	snap, ok := store.Data[events.Snapshot](m.state, "events")
	if !ok || len(snap.Events) == 0 {
		c.lines = []string{sDim.Render("sin novedades")}
		return c
	}
	for _, e := range snap.Events {
		c.lines = append(c.lines, eventIcon(e)+" "+e.Title+sDim.Render(" · "+when(m.now, e.At)))
	}
	return c
}

// overviewCols is how many cards fit side by side.
func (m Model) overviewCols() int {
	switch {
	case m.width >= 150:
		return 4
	case m.width >= 100:
		return 3
	case m.width >= 60:
		return 2
	}
	return 1
}

// overviewLines lays the cards out in a grid. Every line of a card is
// padded to the card's width and every card of a row to the row's height,
// so the grid needs no further alignment.
func (m Model) overviewLines(body int) []string {
	cards := m.cards()
	cols := m.overviewCols()
	rows := (len(cards) + cols - 1) / cols
	inner := m.cardInner()
	maxLines := max(body/rows-3, 1) // border and title
	var out []string
	for r := range rows {
		row := cards[r*cols : min((r+1)*cols, len(cards))]
		height := 0
		for _, c := range row {
			height = max(height, min(len(c.lines), maxLines))
		}
		var boxes []string
		for i, c := range row {
			content := c.lines
			switch {
			case len(content) <= maxLines:
			case maxLines >= 3:
				content = append(slices.Clone(content[:maxLines-1]), sDim.Render(fmt.Sprintf("… y %d más", len(c.lines)-maxLines+1)))
			default:
				// Too short for a "… y N más" line: show content.
				content = content[:maxLines]
			}
			title := sBold.Render(fmt.Sprintf("%d %s", int(c.panel), panelNames[c.panel]))
			if b := m.badge(c.panel); b != "" {
				title += " " + b
			}
			lines := []string{fit(title, inner)}
			for j := range height {
				l := ""
				if j < len(content) {
					l = content[j]
				}
				lines = append(lines, fit(l, inner))
			}
			border := cDim
			if r*cols+i == m.overviewSel {
				border = cCyan
			}
			box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 1)
			boxes = append(boxes, box.Render(strings.Join(lines, "\n")))
		}
		joined := boxes[0]
		for _, b := range boxes[1:] {
			joined = lipgloss.JoinHorizontal(lipgloss.Top, joined, " ", b)
		}
		out = append(out, strings.Split(joined, "\n")...)
	}
	return out
}

// cardInner is the text width inside a card (border and padding excluded).
func (m Model) cardInner() int {
	cols := m.overviewCols()
	return max((m.width-(cols-1))/cols-4, 8)
}

// plural is "1 puerto" or "3 puertos".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// moveCard moves the overview selection; vertical moves jump a whole row.
func (m *Model) moveCard(delta int) {
	n := len(m.cards())
	m.overviewSel = min(max(m.overviewSel+delta, 0), n-1)
}

// ---- cards ----

func (m Model) reposCard() card {
	c := card{panel: PanelRepos}
	snap, ok := store.Data[collector.GitSnapshot](m.state, "git")
	if !ok {
		return card{panel: PanelRepos, lines: []string{m.loading("git")}}
	}
	var dirty, sync, broken int
	var problems []collector.Repo
	for _, r := range snap.Repos {
		switch {
		case r.Err != "":
			broken++
		case r.OutOfSync():
			sync++
		case r.Dirty():
			dirty++
		default:
			continue
		}
		problems = append(problems, r)
	}
	var head []string
	if broken > 0 {
		head = append(head, sRed.Render(plural(broken, "con error", "con error")))
	}
	if sync > 0 {
		head = append(head, sRed.Render(plural(sync, "desincronizado", "desincronizados")))
	}
	if dirty > 0 {
		head = append(head, sYellow.Render(plural(dirty, "con cambios", "con cambios")))
	}
	if len(head) == 0 {
		c.lines = append(c.lines, fmt.Sprintf("%d repos", len(snap.Repos)), sGreen.Render("✓ todo limpio y sincronizado"))
	} else {
		c.lines = append(c.lines, strings.Join(head, sDim.Render(" · ")))
	}
	// Red (out of sync, errors) before yellow (only local changes).
	slices.SortStableFunc(problems, func(a, b collector.Repo) int {
		return cmp.Compare(repoRank(b), repoRank(a))
	})
	for _, r := range problems {
		cells := m.repoCells(r)
		c.lines = append(c.lines, cells[0]+" "+cells[1]+" "+syncCell(r)+" "+changesCell(r))
	}
	return c
}

func repoRank(r collector.Repo) int {
	switch {
	case r.Err != "":
		return 3
	case r.OutOfSync():
		return 2
	}
	return 1
}

func (m Model) githubCard() card {
	c := card{panel: PanelGitHub}
	snap, ok := store.Data[collector.GitHubSnapshot](m.state, "github")
	if !ok {
		c.lines = []string{m.loading("github")}
		return c
	}
	var green, red, running, prs int
	var broken []string
	for _, r := range snap.Repos {
		prs += r.OpenPRs
		if r.CI == nil {
			continue
		}
		switch r.CI.State {
		case collector.CISuccess:
			green++
		case collector.CIFailure:
			red++
		case collector.CIPending:
			running++
		}
		if failing(r) {
			broken = append(broken, sRed.Render(r.Project)+" "+ciText(r.CI))
		}
	}
	reviews := slices.Clone(snap.OtherReviewRequests)
	for _, r := range snap.Repos {
		reviews = append(reviews, r.ReviewRequests...)
	}
	c.lines = append(c.lines, fmt.Sprintf("CI %s %s %s · %d PRs", sGreen.Render(fmt.Sprintf("✓%d", green)),
		sRed.Render(fmt.Sprintf("✗%d", red)), sYellow.Render(fmt.Sprintf("⟳%d", running)), prs))
	c.lines = append(c.lines, broken...)
	for _, pr := range reviews {
		c.lines = append(c.lines, sYellow.Render("◉ ")+link(pr.URL, sBlue.Render(fmt.Sprintf("#%d", pr.Number)))+" "+pr.Title+sDim.Render(" · "+pr.Author))
	}
	if len(broken) == 0 && len(reviews) == 0 {
		c.lines = append(c.lines, sGreen.Render("✓ nada esperándote"))
	}
	return c
}

func (m Model) healthCard() card {
	c := card{panel: PanelHealth}
	inner := m.cardInner()
	snap, ok := store.Data[collector.HealthSnapshot](m.state, "health")
	if !ok {
		c.lines = []string{m.loading("health")}
		return c
	}
	if len(snap.Checks) == 0 {
		c.lines = []string{sDim.Render("sin endpoints configurados")}
		return c
	}
	up := 0
	for _, ch := range snap.Checks {
		if ch.OK {
			up++
		}
	}
	head := sGreen.Render(fmt.Sprintf("✓%d", up))
	if down := len(snap.Checks) - up; down > 0 {
		head += " " + sRed.Render(fmt.Sprintf("✗%d", down))
	}
	worst := -1.0
	for _, ch := range snap.Checks {
		if ch.Stats != nil && ch.Stats.Uptime24h >= 0 && (worst < 0 || ch.Stats.Uptime24h < worst) {
			worst = ch.Stats.Uptime24h
		}
	}
	if worst >= 0 {
		head += sDim.Render(" · 24h ") + uptimeCell(worst)
	}
	c.lines = append(c.lines, fmt.Sprintf("%d endpoints  %s", len(snap.Checks), head))
	// Down endpoints first.
	checks := slices.Clone(snap.Checks)
	slices.SortStableFunc(checks, func(a, b collector.Check) int {
		switch {
		case !a.OK && b.OK:
			return -1
		case a.OK && !b.OK:
			return 1
		}
		return 0
	})
	// ● host latency sparkline: 12 cells of dot, spaces and latency; the
	// host takes up to 22 and the sparkline what is left.
	hostW := min(22, max(inner-18, 8))
	for _, ch := range checks {
		dot, lat := sGreen.Render("●"), sDim.Render(fmt.Sprintf("%d ms", ch.LatencyMS))
		if !ch.OK {
			dot, lat = sRed.Render("●"), sRed.Render(cmp.Or(ch.Err, "caído"))
		}
		spark := sparkline(ch.History, max(inner-hostW-12, 4))
		c.lines = append(c.lines, dot+" "+link(ch.URL, fit(hostOnly(ch.URL), hostW))+" "+fit(lat, 8)+" "+spark)
	}
	return c
}

func (m Model) localCard() card {
	c := card{panel: PanelLocal}
	ports, ok := store.Data[collector.PortsSnapshot](m.state, "ports")
	if !ok {
		c.lines = []string{m.loading("ports")}
		return c
	}
	// Ports of your projects first: they are the dev servers.
	ps := slices.Clone(ports.Ports)
	slices.SortStableFunc(ps, func(a, b collector.Port) int {
		return cmp.Compare(projectRank(b), projectRank(a))
	})
	head := plural(len(ps), "puerto", "puertos")
	if d, ok := store.Data[collector.DockerSnapshot](m.state, "docker"); ok && d.Available {
		head += " · " + plural(len(d.Containers), "contenedor", "contenedores")
	}
	c.lines = append(c.lines, head)
	for _, p := range ps {
		l := link(fmt.Sprintf("http://localhost:%d", p.Port), sBold.Render(fmt.Sprintf(":%d", p.Port))) + " → " + p.Process
		if p.Project != "" {
			l += sDim.Render(" · ") + sCyan.Render(p.Project)
		}
		c.lines = append(c.lines, l)
	}
	if o, ok := store.Data[collector.OllamaSnapshot](m.state, "ollama"); ok && o.Available {
		for _, md := range o.Models {
			c.lines = append(c.lines, sDim.Render("ollama ")+md.Name)
		}
	}
	return c
}

func projectRank(p collector.Port) int {
	if p.Project != "" {
		return 1
	}
	return 0
}

func (m Model) agentsCard() card {
	c := card{panel: PanelAgents}
	snap, ok := store.Data[collector.AgentsSnapshot](m.state, "agents")
	if !ok {
		c.lines = []string{m.loading("agents")}
		return c
	}
	if len(snap.Agents) == 0 {
		c.lines = []string{sDim.Render("ningún agente corriendo")}
		return c
	}
	head := plural(len(snap.Agents), "sesión", "sesiones")
	if waiting, blocked := agentAttention(snap); waiting+blocked > 0 {
		head += sYellow.Render(" · " + plural(waiting+blocked, "te espera", "te esperan"))
	}
	c.lines = append(c.lines, head)
	// Agents that need you first, then the ones working.
	agents := slices.Clone(snap.Agents)
	slices.SortStableFunc(agents, func(a, b collector.Agent) int { return cmp.Compare(agentRank(b), agentRank(a)) })
	for _, a := range agents {
		where := sDim.Render("—")
		if a.Project != "" {
			where = sCyan.Render(a.Project)
		}
		c.lines = append(c.lines, fmt.Sprintf("%s %s %s", sessionState(a.Session), where, sDim.Render(m.activityText(a))))
	}
	return c
}

func agentRank(a collector.Agent) int {
	if a.Session == nil {
		return 0
	}
	switch a.Session.State {
	case collector.SessionBlocked:
		return 3
	case collector.SessionWaiting:
		return 2
	}
	return 1
}

// activityText is activityCell without styles, for the card: without a
// transcript (Cline) it is how long the process has run.
func (m Model) activityText(a collector.Agent) string {
	if a.Session == nil {
		return dur(m.now.Sub(a.Started))
	}
	if a.Session.State == collector.SessionWaiting {
		return a.Session.Title
	}
	return a.Session.Activity
}

// ---- helpers shared with the panels ----

// link makes text a clickable hyperlink (OSC 8) in terminals that support
// it; others simply show the text.
func link(url, text string) string {
	if url == "" {
		return text
	}
	return ansi.SetHyperlink(url) + text + ansi.ResetHyperlink()
}

// hostOnly is the host of a URL, without path.
func hostOnly(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// windowTitle puts the state in the terminal tab: visible even when the
// window is in the background. It sticks to characters whose UTF-8 bytes
// are all outside the C1 range: ✗ (E2 9C 97) carries 0x9C, the 8-bit
// string terminator, and an OSC parser that is not UTF-8-aware ends the
// title there and prints the rest on screen.
func (m Model) windowTitle() string {
	var parts []string
	if s, ok := store.Data[collector.GitHubSnapshot](m.state, "github"); ok {
		n := 0
		for _, r := range s.Repos {
			if failing(r) {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, plural(n, "CI roto", "CI rotos"))
		}
	}
	if s, ok := store.Data[collector.HealthSnapshot](m.state, "health"); ok {
		n := 0
		for _, c := range s.Checks {
			if !c.OK {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, plural(n, "caído", "caídos"))
		}
	}
	if s, ok := store.Data[collector.AgentsSnapshot](m.state, "agents"); ok {
		if waiting, blocked := agentAttention(s); waiting+blocked > 0 {
			parts = append(parts, plural(waiting+blocked, "te espera", "te esperan"))
		}
	}
	if len(parts) == 0 {
		return "atalaya · todo bien"
	}
	return "atalaya · " + strings.Join(parts, " · ")
}

// loadProgress is the share of sources that have reported at least once,
// for the terminal's native progress bar while starting or refreshing.
func (m Model) loadProgress() (pct int, done bool) {
	total, ready := 0, 0
	for _, e := range m.state.Entries {
		total++
		if !e.UpdatedAt.IsZero() && (!m.refreshing || !e.Running) {
			ready++
		}
	}
	if total == 0 {
		return 0, false
	}
	return ready * 100 / total, ready == total
}
