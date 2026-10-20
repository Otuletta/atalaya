package ui

import (
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/store"
)

// What makes the dashboard answer the hand: the mouse, a dialog for
// confirmations, a spinner while something runs, toasts for what just
// happened and key hints for the selected item.

// The screen: the tabs on row 0, a rule, the body from bodyTop down to the
// status line and the key hints, the last two rows.
const bodyTop = 2

type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type (
	// busyDoneMsg wraps the result of an action that turned the spinner.
	busyDoneMsg     struct{ msg tea.Msg }
	toastExpiredMsg struct{ id int }
)

// ---- spinner ----

// startBusy runs cmd with the spinner turning and text in the status line
// until it returns.
func (m Model) startBusy(text string, cmd tea.Cmd) (Model, tea.Cmd) {
	if text == "" || cmd == nil {
		return m, cmd
	}
	m.busy++
	m.busyText = text
	done := func() tea.Msg { return busyDoneMsg{cmd()} }
	if m.busy == 1 {
		return m, tea.Batch(m.spin.Tick, done)
	}
	return m, done
}

// ---- confirmation dialog ----

func (m Model) confirmKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right", "tab", "shift+tab", "h", "l":
		m.confirm.yes = !m.confirm.yes
		return m, nil
	case "y", "s":
		return m.confirmed()
	case "enter", "space":
		if m.confirm.yes {
			return m.confirmed()
		}
	}
	m.confirm = nil
	return m, m.setStatus("cancelado", false)
}

func (m Model) confirmed() (Model, tea.Cmd) {
	c := m.confirm
	m.confirm = nil
	return m.startBusy(c.busy, c.run)
}

type dialog struct {
	box     string
	x, y    int
	yes, no rect
}

// dialogLayout draws the confirmation and says where its buttons are.
func (m Model) dialogLayout() dialog {
	c := m.confirm
	width := min(max(ansi.StringWidth(c.prompt)+8, 46), m.width-4)
	inner := width - 6 // border and padding
	color, title := cYellow, "Confirmar"
	if c.danger {
		color, title = cRed, "Confirmar · no se puede deshacer"
	}
	yes, no := button("Sí", c.yes, color), button("No", !c.yes, color)
	buttons := yes + "   " + no
	pad := max((inner-ansi.StringWidth(buttons))/2, 0)
	prompt := strings.Split(ansi.Wordwrap(c.prompt, inner, ""), "\n")
	lines := []string{lipgloss.NewStyle().Foreground(color).Bold(true).Render(title), ""}
	for _, l := range prompt {
		lines = append(lines, sBold.Render(l))
	}
	lines = append(lines, "", strings.Repeat(" ", pad)+buttons, "", sDim.Render("y sí · n o esc no · ←→ elegir"))
	for i, l := range lines {
		lines[i] = fit(l, inner)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 2).
		Render(strings.Join(lines, "\n"))
	d := dialog{box: box, x: max((m.width-width)/2, 0)}
	d.y = bodyTop + max((m.height-4-lipgloss.Height(box))/2, 0)
	bx, by := d.x+3+pad, d.y+1+2+len(prompt)+1
	d.yes = rect{bx, by, ansi.StringWidth(yes), 1}
	d.no = rect{bx + ansi.StringWidth(yes) + 3, by, ansi.StringWidth(no), 1}
	return d
}

func button(label string, focused bool, color color.Color) string {
	text := "  " + label + "  "
	if focused {
		return lipgloss.NewStyle().Reverse(true).Bold(true).Foreground(color).Render(text)
	}
	return sDim.Render(text)
}

// ---- toasts ----

type toast struct {
	id int
	ev events.Event
}

const (
	toastFor  = 6 * time.Second
	maxToasts = 3
)

// newToasts shows the events that happened since the last look, unless
// the Activity panel, which lists them, is on screen.
func (m *Model) newToasts() tea.Cmd {
	snap, ok := store.Data[events.Snapshot](m.state, "events")
	if !ok {
		return nil
	}
	var cmds []tea.Cmd
	newest := m.toastedUntil
	for i := len(snap.Events) - 1; i >= 0; i-- { // oldest first
		e := snap.Events[i]
		if !e.At.After(m.toastedUntil) {
			continue
		}
		if e.At.After(newest) {
			newest = e.At
		}
		if m.panel == PanelActivity {
			continue
		}
		m.toastID++
		id := m.toastID
		m.toasts = append(m.toasts, toast{id: id, ev: e})
		cmds = append(cmds, tea.Tick(toastFor, func(time.Time) tea.Msg { return toastExpiredMsg{id} }))
	}
	m.toastedUntil = newest
	if len(m.toasts) > maxToasts {
		m.toasts = m.toasts[len(m.toasts)-maxToasts:]
	}
	return tea.Batch(cmds...)
}

type placedToast struct {
	box  string
	x, y int
	area rect
	ev   events.Event
}

// toastLayout stacks the toasts in the top right corner, newest first.
func (m Model) toastLayout() []placedToast {
	width := min(48, m.width/2)
	inner := width - 4
	y := bodyTop
	var out []placedToast
	for i := len(m.toasts) - 1; i >= 0; i-- {
		e := m.toasts[i].ev
		color := cDim
		switch e.Severity {
		case events.Bad:
			color = cRed
		case events.Good:
			color = cGreen
		case events.Ask:
			color = cYellow
		}
		lines := []string{fit(eventIcon(e)+" "+sBold.Render(e.Title), inner)}
		if e.Body != "" {
			lines = append(lines, fit(sDim.Render(e.Body), inner))
		}
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(color).Padding(0, 1).
			Render(strings.Join(lines, "\n"))
		h := lipgloss.Height(box)
		if y+h > m.height-2 {
			break
		}
		x := m.width - width - 1
		out = append(out, placedToast{box: box, x: x, y: y, area: rect{x, y, width, h}, ev: e})
		y += h
	}
	return out
}

func eventKey(e events.Event) string {
	return fmt.Sprintf("event:%d:%s", e.At.UnixNano(), e.Kind)
}

// ---- mouse ----

type click struct {
	x, y int
	at   time.Time
}

// doubleClick is the most time between the clicks of a double click.
const doubleClick = 400 * time.Millisecond

// click selects what is under the pointer: a tab, a card, a row, a palette
// result, a button of the dialog or a toast. A double click opens it.
func (m Model) click(ms tea.Mouse) (Model, tea.Cmd) {
	if ms.Button != tea.MouseLeft {
		return m, nil
	}
	now := time.Now()
	double := m.lastClick.y == ms.Y && abs(m.lastClick.x-ms.X) <= 2 && now.Sub(m.lastClick.at) < doubleClick
	m.lastClick = click{ms.X, ms.Y, now}
	if double {
		m.lastClick = click{} // a third click starts over
	}

	switch {
	case m.confirm != nil:
		d := m.dialogLayout()
		switch {
		case d.yes.contains(ms.X, ms.Y):
			return m.confirmed()
		case d.no.contains(ms.X, ms.Y):
			m.confirm = nil
			return m, m.setStatus("cancelado", false)
		}
		return m, nil
	case m.palette != nil:
		return m.clickPalette(ms)
	case m.showHelp:
		m.showHelp = false
		return m, nil
	case m.filtering:
		return m, nil
	}
	for _, t := range m.toastLayout() {
		if t.area.contains(ms.X, ms.Y) {
			m.toasts = nil
			m.detailOpen = false
			m.panel = PanelActivity
			m.sel[PanelActivity] = eventKey(t.ev)
			return m, nil
		}
	}
	switch {
	case ms.Y == 0:
		if p, ok := m.tabAt(ms.X); ok {
			m.panel, m.detailOpen = p, false
		}
	case m.detailOpen || ms.Y < bodyTop || ms.Y >= m.height-2:
	case m.panel == PanelOverview:
		if i := m.cardAt(ms.X, ms.Y); i >= 0 {
			m.overviewSel = i
			if double {
				m.panel = m.cards()[i].panel
			}
		}
	default:
		if r, ok := m.rowAt(ms.Y); ok {
			m.sel[m.panel] = r.key
			if double {
				m.openDetail(r)
			}
		}
	}
	return m, nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func (m *Model) openDetail(r row) {
	m.detailOpen = true
	m.detailKey = r.key
	m.resizeViewport()
	m.viewport.SetContent(m.detail(r.item))
	m.viewport.GotoTop()
}

// tabAt is the tab under column x of the tab bar.
func (m Model) tabAt(x int) (Panel, bool) {
	brand, _, names := m.tabVariant(m.clockText())
	pos := ansi.StringWidth(brand)
	for p := range panelCount {
		w := ansi.StringWidth(m.tabLabel(p, names))
		if x >= pos && x < pos+w {
			return p, true
		}
		pos += w
	}
	return 0, false
}

// rowAt is the selectable row drawn on screen line y, as panelLines lays
// them out.
func (m Model) rowAt(y int) (row, bool) {
	pv := m.build(m.panel)
	header := 0
	if pv.header != "" {
		header = 1
	}
	i := y - bodyTop - header
	visible := m.height - 4 - header
	if i < 0 || i >= visible || len(pv.rows) == 0 {
		return row{}, false
	}
	off := min(m.offset[m.panel], max(len(pv.rows)-visible, 0))
	if idx := off + i; idx < len(pv.rows) && pv.rows[idx].selectable {
		return pv.rows[idx], true
	}
	return row{}, false
}

// cardAt is the overview card at x, y, or -1, as overviewLines lays them
// out: rows of cards as tall as their tallest, one column apart.
func (m Model) cardAt(x, y int) int {
	cards := m.cards()
	cols := m.overviewCols()
	rows := (len(cards) + cols - 1) / cols
	maxLines := max((m.height-4)/rows-3, 1)
	width := m.cardInner() + 4
	top := bodyTop
	for r := range rows {
		height := 0
		for _, c := range cards[r*cols : min((r+1)*cols, len(cards))] {
			height = max(height, min(len(c.lines), maxLines))
		}
		h := height + 3 // title and border
		for i := range min(cols, len(cards)-r*cols) {
			if (rect{i * (width + 1), top, width, h}).contains(x, y) {
				return r*cols + i
			}
		}
		top += h
	}
	return -1
}

// clickPalette runs the result clicked; a click outside closes it.
func (m Model) clickPalette(ms tea.Mouse) (Model, tea.Cmd) {
	p := m.palette
	start, n := m.paletteWindow()
	width := m.paletteWidth()
	box := rect{(m.width - width) / 2, bodyTop, width, n + 6}
	if !box.contains(ms.X, ms.Y) {
		m.palette = nil
		return m, nil
	}
	if i := ms.Y - bodyTop - 3; i >= 0 && i < n && start+i < len(p.results) {
		p.sel = start + i
		return m.paletteRun()
	}
	return m, nil
}

// ---- key hints ----

// itemHints are the keys that do something on an item, for the footer.
func (m Model) itemHints(item any) []key.Binding {
	k := m.keys
	if _, ok := m.failure(item); ok {
		return []key.Binding{short(k.Analyze, "diagnóstico"), short(k.Fix, "arreglar"),
			short(k.Rerun, "relanzar"), short(k.Open, "ver el run")}
	}
	switch it := item.(type) {
	case collector.Repo:
		hs := []key.Binding{short(k.Open, "editor")}
		if it.Behind > 0 {
			hs = append(hs, short(k.Pull, "pull"))
		}
		return hs
	case collector.GitHubRepo:
		return []key.Binding{short(k.Open, "PRs"), short(k.Pull, "pull")}
	case branchItem:
		return []key.Binding{short(k.Open, "abrir"), short(k.Pull, "pull")}
	case botGroup:
		hs := []key.Binding{short(k.Open, "abrir")}
		if n := len(slices.DeleteFunc(slices.Clone(it.PRs), func(pr collector.PullRequest) bool { return !mergeable(pr) })); n > 0 {
			hs = append(hs, short(k.Merge, fmt.Sprintf("merge de %d en verde", n)))
		}
		return hs
	case collector.PullRequest:
		hs := []key.Binding{short(k.Open, "abrir el PR")}
		if mergeable(it) {
			hs = append(hs, short(k.Merge, "merge"))
		}
		return hs
	case collector.Port:
		return []key.Binding{short(k.Open, "abrir"), short(k.Kill, "matar")}
	case collector.Check:
		return []key.Binding{short(k.Open, "abrir")}
	case collector.Agent:
		return []key.Binding{short(k.Open, "editor")}
	case events.Event:
		if it.URL != "" {
			return []key.Binding{short(k.Open, "abrir")}
		}
	case pluginItem:
		if it.Row.URL != "" {
			return []key.Binding{short(k.Open, "abrir")}
		}
	}
	return nil
}

func concat(lists ...[]key.Binding) []key.Binding {
	var out []key.Binding
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}
