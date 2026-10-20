package ui

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/otuletta/atalaya/internal/collector"
)

// The command palette (:) finds anything Atalaya shows — a repo, a PR, a
// port, an agent, an event — and jumps to it. It also lists the panels and
// a few commands, so nothing needs a key to be remembered.

type paletteEntry struct {
	label   string // what is shown and matched
	context string // shown dimmed on the right, matched too
	panel   Panel
	key     string // the row to select; empty to just open the panel
	press   string // a command: the key it stands for
}

type paletteResult struct {
	entry int
	match match
}

type palette struct {
	input   textinput.Model
	entries []paletteEntry
	results []paletteResult
	sel     int
}

// paletteRows is how many results the palette shows at most.
const paletteRows = 10

var spaces = regexp.MustCompile(`\s{2,}`)

// rowContext is what a row does not say about itself: the repo of a PR
// listed under it.
func rowContext(item any) string {
	switch it := item.(type) {
	case collector.PullRequest:
		return it.Repo
	case botGroup:
		return it.Repo
	case branchItem:
		return it.Repo.Project
	}
	return ""
}

// paletteEntries lists every selectable row of every panel, ignoring the
// filter and the width of the terminal, then the panels and the commands.
func (m Model) paletteEntries() []paletteEntry {
	wide := m
	wide.width = 240
	wide.filter.SetValue("")
	var out []paletteEntry
	for p := PanelRepos; p < panelCount; p++ {
		for _, r := range wide.build(p).rows {
			if !r.selectable {
				continue
			}
			ctx := panelNames[p]
			if c := rowContext(r.item); c != "" {
				ctx = c + " · " + ctx
			}
			label := spaces.ReplaceAllString(strings.TrimSpace(ansi.Strip(r.line)), "  ")
			out = append(out, paletteEntry{label: label, context: ctx, panel: p, key: r.key})
		}
	}
	for p := range panelCount {
		out = append(out, paletteEntry{label: "Ir al panel " + panelNames[p], context: strconv.Itoa(int(p)), panel: p})
	}
	for _, c := range []struct{ label, press string }{
		{"Pull de todos los repos atrasados y limpios", "P"},
		{"Refrescar todas las fuentes", "r"},
		{"Filtrar proyectos", "/"},
		{"Atajos de teclado", "?"},
	} {
		out = append(out, paletteEntry{label: c.label, context: c.press, press: c.press})
	}
	return out
}

func (m Model) openPalette() (Model, tea.Cmd) {
	ti := textinput.New()
	ti.Prompt = ": "
	ti.Placeholder = "repo, PR, puerto, agente, panel o comando…"
	p := &palette{input: ti, entries: m.paletteEntries()}
	p.input.SetWidth(max(m.paletteWidth()-8, 10))
	p.filter()
	m.palette = p
	return m, p.input.Focus()
}

// filter ranks the entries against the query; with none, they keep their
// order.
func (p *palette) filter() {
	q := p.input.Value()
	p.results = p.results[:0]
	for i, e := range p.entries {
		if mt, ok := fuzzy(q, e.label+"  "+e.context); ok {
			p.results = append(p.results, paletteResult{entry: i, match: mt})
		}
	}
	slices.SortStableFunc(p.results, func(a, b paletteResult) int { return b.match.score - a.match.score })
	p.sel = 0
}

func (m Model) paletteKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.palette
	switch msg.String() {
	case "esc":
		m.palette = nil
	case "enter":
		return m.paletteRun()
	case "up", "ctrl+p", "shift+tab":
		p.sel = max(p.sel-1, 0)
	case "down", "ctrl+n", "tab":
		p.sel = max(min(p.sel+1, len(p.results)-1), 0)
	default:
		return m, p.update(msg)
	}
	return m, nil
}

// update types into the query and ranks again when it changed.
func (p *palette) update(msg tea.Msg) tea.Cmd {
	before := p.input.Value()
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)
	if p.input.Value() != before {
		p.filter()
	}
	return cmd
}

// paletteRun jumps to the chosen entry, or runs its command as if its key
// had been pressed (so read-only and confirmations still apply).
func (m Model) paletteRun() (Model, tea.Cmd) {
	p := m.palette
	m.palette = nil
	if len(p.results) == 0 {
		return m, nil
	}
	e := p.entries[p.results[p.sel].entry]
	if e.press != "" {
		return m.handleKey(tea.KeyPressMsg{Code: []rune(e.press)[0], Text: e.press})
	}
	m.showHelp, m.detailOpen = false, false
	m.panel = e.panel
	if e.key == "" {
		return m, nil
	}
	if !slices.ContainsFunc(m.build(e.panel).rows, func(r row) bool { return r.key == e.key }) {
		m.filter.SetValue("") // the filter hides it
	}
	m.sel[e.panel] = e.key
	return m, nil
}

func (m Model) paletteWidth() int { return min(m.width-4, 100) }

// paletteWindow is the first result shown and how many fit: between the
// tabs and the status line, 6 rows go to the border, the query and the
// footer.
func (m Model) paletteWindow() (start, n int) {
	n = max(min(paletteRows, m.height-10), 1)
	return max(0, m.palette.sel-n+1), n
}

// paletteView is the palette's box.
func (m Model) paletteView() string {
	p := m.palette
	inner := m.paletteWidth() - 4
	lines := []string{fit(p.input.View(), inner), sDim.Render(strings.Repeat("─", inner))}
	start, n := m.paletteWindow()
	for i := start; i < start+n; i++ {
		if i >= len(p.results) {
			if i == 0 {
				lines = append(lines, sDim.Render("  nada coincide"))
				continue
			}
			lines = append(lines, "")
			continue
		}
		r := p.results[i]
		e := p.entries[r.entry]
		gutter := "  "
		if i == p.sel {
			gutter = sCursor.Render("› ")
		}
		ctx := ansi.Truncate(e.context, inner/3, "…")
		labelWidth := inner - 2 - ansi.StringWidth(ctx) - 2
		lines = append(lines, gutter+fit(highlight(e.label, r.match.pos, i == p.sel), labelWidth)+"  "+sDim.Render(ctx))
	}
	lines = append(lines, "", sDim.Render(fmt.Sprintf("%d de %d · ↑↓ elegir · enter ir · esc cerrar", len(p.results), len(p.entries))))
	return sPalette.Width(inner + 4).Render(strings.Join(lines, "\n"))
}

var sPalette = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cCyan).Padding(0, 1)

// highlight marks the matched runes of a label; the selected one is bold.
func highlight(label string, pos []int, selected bool) string {
	base := lipgloss.NewStyle()
	if selected {
		base = sBold
	}
	hit := sYellow.Bold(true)
	var b strings.Builder
	var run []rune
	inHit := false
	flush := func() {
		if len(run) == 0 {
			return
		}
		if inHit {
			b.WriteString(hit.Render(string(run)))
		} else {
			b.WriteString(base.Render(string(run)))
		}
		run = run[:0]
	}
	j := 0
	for i, r := range []rune(label) {
		isHit := j < len(pos) && pos[j] == i
		if isHit {
			j++
		}
		if isHit != inHit {
			flush()
			inHit = isHit
		}
		run = append(run, r)
	}
	flush()
	return b.String()
}

// overlay draws fg over bg, with fg's top-left corner at x, y, cropped to
// bg. A modal dims what it covers.
func overlay(bg, fg string, x, y int, dim bool) string {
	lines := strings.Split(bg, "\n")
	for i, l := range lines {
		if dim {
			lines[i] = sDim.Render(ansi.Strip(l))
		}
	}
	out := strings.Split(lipgloss.NewCompositor(
		lipgloss.NewLayer(strings.Join(lines, "\n")),
		lipgloss.NewLayer(fg).X(x).Y(y).Z(1),
	).Render(), "\n")
	return strings.Join(out[:min(len(out), len(lines))], "\n")
}
