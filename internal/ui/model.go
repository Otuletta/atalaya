// Package ui is the Bubble Tea front end. It only reads the store: every
// piece of data arrives as a store.State, so the UI never blocks on I/O.
package ui

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/store"
)

// Actions are the side effects behind o, p and x. Tests replace them.
type Actions interface {
	Editor(dir string) (cmd *exec.Cmd, interactive bool)
	Start(cmd *exec.Cmd) error
	OpenURL(url string) error
	Pull(ctx context.Context, dir string) (string, error)
	Kill(ctx context.Context, pid int32, started time.Time) error
}

// Deps is everything the UI needs from the outside.
type Deps struct {
	Config  *config.Config
	Store   *store.Store
	Refresh func(sources ...string)
	Actions Actions
	Now     func() time.Time
	Version string
}

// Messages.
type (
	stateMsg  struct{ state store.State }
	tickMsg   struct{}
	actionMsg struct {
		text    string
		err     error
		refresh []string
	}
	clearStatusMsg struct{ id int }
)

type confirmation struct {
	prompt string
	run    tea.Cmd
}

// Model is the root Bubble Tea model.
type Model struct {
	deps   Deps
	keys   keyMap
	help   help.Model
	filter textinput.Model

	state         store.State
	now           time.Time
	width, height int

	panel  Panel
	sel    [panelCount]string // key of the selected row per panel
	offset [panelCount]int    // first visible row per panel

	filtering  bool
	showHelp   bool
	detailOpen bool
	detailKey  string
	viewport   viewport.Model
	confirm    *confirmation
	refreshing bool

	status    string
	statusErr bool
	statusID  int
}

// New builds the model. The store may already hold data.
func New(d Deps) Model {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Refresh == nil {
		d.Refresh = func(...string) {}
	}
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filtrar proyectos…"
	h := help.New()
	h.ShortSeparator = sDim.Render(" · ")
	m := Model{
		deps: d, keys: newKeyMap(), help: h, filter: ti,
		now: d.Now(), width: 100, height: 30,
		viewport: viewport.New(),
	}
	if d.Store != nil {
		m.state = d.Store.Snapshot()
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.waitForState(), tick())
}

// waitForState blocks until the store changes, then delivers a copy.
func (m Model) waitForState() tea.Cmd {
	st := m.deps.Store
	if st == nil {
		return nil
	}
	return func() tea.Msg {
		<-st.Changed()
		return stateMsg{st.Snapshot()}
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.SetWidth(msg.Width)
		m.resizeViewport()
	case stateMsg:
		m.state = msg.state
		if m.refreshing && !m.anyRunning() {
			m.refreshing = false
		}
		m.refreshDetail()
		cmd = m.waitForState()
	case tickMsg:
		m.now = m.deps.Now()
		cmd = tick()
	case actionMsg:
		if len(msg.refresh) > 0 {
			m.deps.Refresh(msg.refresh...)
		}
		if msg.err != nil {
			cmd = m.setStatus(msg.err.Error(), true)
		} else {
			cmd = m.setStatus(msg.text, false)
		}
	case clearStatusMsg:
		if msg.id == m.statusID {
			m.status = ""
		}
	case tea.KeyPressMsg:
		m, cmd = m.handleKey(msg)
	case tea.PasteMsg:
		if m.filtering {
			m.filter, cmd = m.filter.Update(msg)
		}
	case tea.MouseWheelMsg:
		switch {
		case m.detailOpen:
			m.viewport, cmd = m.viewport.Update(msg)
		case msg.Button == tea.MouseWheelUp:
			m.move(-3)
		case msg.Button == tea.MouseWheelDown:
			m.move(3)
		}
	}
	m.clampScroll()
	return m, cmd
}

func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	k := m.keys
	// A pending confirmation takes every key: only y confirms.
	if m.confirm != nil {
		c := m.confirm
		m.confirm = nil
		if msg.String() == "y" || msg.String() == "s" {
			return m, c.run
		}
		return m, m.setStatus("cancelado", false)
	}
	if m.filtering {
		switch msg.String() {
		case "esc":
			m.filtering = false
			m.filter.SetValue("")
			m.filter.Blur()
		case "enter":
			m.filtering = false
			m.filter.Blur()
		default:
			var cmd tea.Cmd
			m.filter, cmd = m.filter.Update(msg)
			return m, cmd
		}
		return m, nil
	}
	if m.showHelp {
		if key.Matches(msg, k.Help, k.Back, k.Quit) {
			m.showHelp = false
		}
		return m, nil
	}
	if key.Matches(msg, k.Quit) {
		if msg.String() == "q" && m.detailOpen {
			m.detailOpen = false
			return m, nil
		}
		return m, tea.Quit
	}
	if m.detailOpen {
		switch {
		case key.Matches(msg, k.Back, k.Enter):
			m.detailOpen = false
		case key.Matches(msg, k.Open):
			return m, m.open(m.selectedItem())
		case key.Matches(msg, k.Pull):
			return m.askPull(m.selectedItem())
		case key.Matches(msg, k.Kill):
			return m.askKill(m.selectedItem())
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, k.Next):
		m.panel = (m.panel + 1) % panelCount
	case key.Matches(msg, k.Prev):
		m.panel = (m.panel + panelCount - 1) % panelCount
	case key.Matches(msg, k.Panel):
		m.panel = Panel(msg.String()[0] - '1')
	case key.Matches(msg, k.Up):
		m.move(-1)
	case key.Matches(msg, k.Down):
		m.move(1)
	case key.Matches(msg, k.PgUp):
		m.move(-m.bodyHeight() / 2)
	case key.Matches(msg, k.PgDn):
		m.move(m.bodyHeight() / 2)
	case key.Matches(msg, k.Top):
		m.move(-1 << 20)
	case key.Matches(msg, k.Bottom):
		m.move(1 << 20)
	case key.Matches(msg, k.Filter):
		m.filtering = true
		return m, m.filter.Focus()
	case key.Matches(msg, k.Back):
		m.filter.SetValue("")
	case key.Matches(msg, k.Enter):
		if r, ok := m.selectedRow(); ok {
			m.detailOpen = true
			m.detailKey = r.key
			m.resizeViewport()
			m.viewport.SetContent(m.detail(r.item))
			m.viewport.GotoTop()
		}
	case key.Matches(msg, k.Open):
		return m, m.open(m.selectedItem())
	case key.Matches(msg, k.Pull):
		return m.askPull(m.selectedItem())
	case key.Matches(msg, k.Kill):
		return m.askKill(m.selectedItem())
	case key.Matches(msg, k.Refresh):
		m.refreshing = true
		m.deps.Refresh()
		return m, m.setStatus("refrescando todas las fuentes…", false)
	case key.Matches(msg, k.Help):
		m.showHelp = true
	}
	return m, nil
}

func (m *Model) setStatus(text string, isErr bool) tea.Cmd {
	m.statusID++
	m.status, m.statusErr = text, isErr
	id := m.statusID
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg { return clearStatusMsg{id} })
}

func (m Model) anyRunning() bool {
	for _, e := range m.state.Entries {
		if e.Running {
			return true
		}
	}
	return false
}

// ---- selection ----

func (m Model) selectable(rows []row) []int {
	var idx []int
	for i, r := range rows {
		if r.selectable {
			idx = append(idx, i)
		}
	}
	return idx
}

// selectedIndex is the row index of the selection, falling back to the
// first selectable row when the selected item disappeared.
func (m Model) selectedIndex(rows []row) int {
	first := -1
	for i, r := range rows {
		if !r.selectable {
			continue
		}
		if first < 0 {
			first = i
		}
		if r.key == m.sel[m.panel] {
			return i
		}
	}
	return first
}

func (m Model) selectedRow() (row, bool) {
	rows := m.build(m.panel).rows
	i := m.selectedIndex(rows)
	if i < 0 {
		return row{}, false
	}
	return rows[i], true
}

func (m Model) selectedItem() any {
	if m.detailOpen {
		for _, r := range m.build(m.panel).rows {
			if r.key == m.detailKey {
				return r.item
			}
		}
		return nil
	}
	r, ok := m.selectedRow()
	if !ok {
		return nil
	}
	return r.item
}

func (m *Model) move(delta int) {
	rows := m.build(m.panel).rows
	idx := m.selectable(rows)
	if len(idx) == 0 {
		return
	}
	cur := slices.Index(idx, m.selectedIndex(rows))
	next := min(max(cur+delta, 0), len(idx)-1)
	m.sel[m.panel] = rows[idx[next]].key
}

// bodyHeight is the number of rows the panel can show.
func (m Model) bodyHeight() int {
	// tabs + rule + column header + status + help
	return max(m.height-5, 1)
}

// clampScroll keeps the selection on screen with minimal scrolling.
func (m *Model) clampScroll() {
	rows := m.build(m.panel).rows
	h := m.bodyHeight()
	off := m.offset[m.panel]
	if i := m.selectedIndex(rows); i >= 0 {
		if i < off {
			off = i
		}
		if i >= off+h {
			off = i - h + 1
		}
		// Show the section title above the first item of a section.
		if i == off && i > 0 && !rows[i-1].selectable && i-1 >= off-1 {
			off = max(i-2, 0)
		}
	}
	off = min(off, max(len(rows)-h, 0))
	m.offset[m.panel] = max(off, 0)
}

func (m *Model) resizeViewport() {
	m.viewport.SetWidth(max(m.width-2, 10))
	m.viewport.SetHeight(max(m.height-4, 3))
}

// refreshDetail updates an open detail view with fresh data.
func (m *Model) refreshDetail() {
	if !m.detailOpen {
		return
	}
	for _, r := range m.build(m.panel).rows {
		if r.key == m.detailKey {
			m.viewport.SetContent(m.detail(r.item))
			return
		}
	}
}

// ---- actions ----

func (m Model) projectPath(name string) string {
	for _, p := range m.deps.Config.Projects {
		if p.Name == name {
			return p.Path
		}
	}
	return ""
}

func (m Model) open(item any) tea.Cmd {
	a := m.deps.Actions
	if a == nil {
		return nil
	}
	openURL := func(url string) tea.Cmd {
		return func() tea.Msg {
			if err := a.OpenURL(url); err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{text: "abierto en el navegador: " + url}
		}
	}
	editor := func(dir string) tea.Cmd {
		if dir == "" {
			return func() tea.Msg { return actionMsg{err: fmt.Errorf("este proyecto no tiene path")} }
		}
		cmd, interactive := a.Editor(dir)
		if interactive {
			// Terminal editors take over the screen: suspend the TUI.
			return tea.ExecProcess(cmd, func(err error) tea.Msg {
				return actionMsg{text: "editor cerrado", err: err, refresh: []string{"git"}}
			})
		}
		return func() tea.Msg {
			if err := a.Start(cmd); err != nil {
				return actionMsg{err: err}
			}
			return actionMsg{text: fmt.Sprintf("abierto %s en %s", config.ContractHome(dir), cmd.Args[0])}
		}
	}
	switch it := item.(type) {
	case collector.Repo:
		return editor(it.Path)
	case collector.Agent:
		return editor(it.Cwd)
	case collector.GitHubRepo:
		if it.CI != nil && it.CI.State == collector.CIFailure && it.CI.URL != "" {
			return openURL(it.CI.URL) // the broken run is what you want to see
		}
		return openURL(it.URL + "/pulls")
	case collector.PullRequest:
		return openURL(it.URL)
	case collector.Check:
		return openURL(it.URL)
	case collector.Port:
		return openURL(fmt.Sprintf("http://localhost:%d", it.Port))
	}
	return m.statusCmd("o no aplica a este elemento")
}

func (m Model) statusCmd(text string) tea.Cmd {
	return func() tea.Msg { return actionMsg{text: text} }
}

func (m Model) askPull(item any) (Model, tea.Cmd) {
	var project, dir string
	switch it := item.(type) {
	case collector.Repo:
		project, dir = it.Project, it.Path
	case collector.GitHubRepo:
		project, dir = it.Project, m.projectPath(it.Project)
	default:
		return m, m.statusCmd("p solo aplica a repos")
	}
	if dir == "" {
		return m, m.statusCmd(project + " no tiene un checkout local")
	}
	a := m.deps.Actions
	m.confirm = &confirmation{
		prompt: fmt.Sprintf("¿git pull --ff-only en %s?", project),
		run: tea.Sequence(m.statusCmd("git pull en "+project+"…"), func() tea.Msg {
			out, err := a.Pull(context.Background(), dir)
			if err != nil {
				return actionMsg{err: fmt.Errorf("pull %s: %w", project, err), refresh: []string{"git"}}
			}
			return actionMsg{text: fmt.Sprintf("pull %s: %s", project, out), refresh: []string{"git", "github"}}
		}),
	}
	return m, nil
}

func (m Model) askKill(item any) (Model, tea.Cmd) {
	p, ok := item.(collector.Port)
	if !ok {
		return m, m.statusCmd("x solo aplica a puertos (panel Local)")
	}
	a := m.deps.Actions
	m.confirm = &confirmation{
		prompt: fmt.Sprintf("¿Matar %s (pid %d) en :%d?", p.Process, p.PID, p.Port),
		run: func() tea.Msg {
			if err := a.Kill(context.Background(), p.PID, p.Started); err != nil {
				return actionMsg{err: fmt.Errorf("kill %d: %w", p.PID, err), refresh: []string{"ports"}}
			}
			return actionMsg{text: fmt.Sprintf("SIGTERM enviado a %s (pid %d)", p.Process, p.PID), refresh: []string{"ports", "agents"}}
		},
	}
	return m, nil
}

// ---- view ----

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.WindowTitle = "atalaya"
	return v
}

func (m Model) render() string {
	if m.width < 40 || m.height < 8 {
		return "Ventana demasiado pequeña para atalaya."
	}
	var b strings.Builder
	b.WriteString(m.tabs() + "\n")
	b.WriteString(sDim.Render(strings.Repeat("─", m.width)) + "\n")

	body := m.height - 4
	var lines []string
	switch {
	case m.showHelp:
		lines = strings.Split(m.helpView(), "\n")
	case m.detailOpen:
		lines = strings.Split(m.viewport.View(), "\n")
	default:
		lines = m.panelLines(body)
	}
	for i := range body {
		if i < len(lines) {
			b.WriteString(fit(lines[i], m.width))
		}
		b.WriteString("\n")
	}
	b.WriteString(fit(m.statusLine(), m.width) + "\n")
	b.WriteString(fit(m.helpLine(), m.width))
	return b.String()
}

func (m Model) tabs() string {
	clock := sDim.Render(m.now.Format("15:04:05"))
	if m.refreshing {
		clock = sYellow.Render("⟳ ") + clock
	}
	// On a narrow terminal give up, in order: the brand's name, the clock,
	// and the names of the inactive tabs. Numbers and badges always stay.
	for _, v := range []struct {
		brand, clock string
		names        bool
	}{
		{sBrand.Render("◆ ATALAYA") + " ", clock, true},
		{sBrand.Render("◆"), clock, true},
		{sBrand.Render("◆"), "", true},
		{sBrand.Render("◆"), "", false},
	} {
		left := v.brand + m.tabLabels(v.names)
		if pad := m.width - ansi.StringWidth(left) - ansi.StringWidth(v.clock); pad >= 1 || (pad == 0 && v.clock == "") {
			return left + strings.Repeat(" ", max(pad, 0)) + v.clock
		}
	}
	return fit(sBrand.Render("◆")+m.tabLabels(false), m.width)
}

// tabLabels renders the tabs; without names only the active one is named.
func (m Model) tabLabels(names bool) string {
	var b strings.Builder
	for p := range panelCount {
		style, label := sTab, fmt.Sprint(p+1)
		if p == m.panel {
			style = sTabOn
		}
		if names || p == m.panel {
			label += " " + panelNames[p]
		}
		b.WriteString(style.Render(label) + m.badge(p))
	}
	return b.String()
}

func (m Model) panelLines(body int) []string {
	pv := m.build(m.panel)
	lines := []string{"  " + pv.header}
	if pv.header == "" {
		lines = nil
	}
	if len(pv.rows) == 0 {
		msg := pv.empty
		if msg == "" {
			msg = sDim.Render("Nada coincide con «" + m.filter.Value() + "». Esc limpia el filtro.")
		}
		return append(lines, "", "  "+msg)
	}
	sel := m.selectedIndex(pv.rows)
	rows := body - len(lines)
	off := min(m.offset[m.panel], max(len(pv.rows)-rows, 0))
	for i := off; i < len(pv.rows) && i < off+rows; i++ {
		gutter := "  "
		if i == sel {
			gutter = sCursor.Render("› ")
		}
		lines = append(lines, gutter+pv.rows[i].line)
	}
	return lines
}

func (m Model) statusLine() string {
	switch {
	case m.confirm != nil:
		return sYellow.Bold(true).Render(m.confirm.prompt) + sDim.Render("  y confirma · cualquier otra tecla cancela")
	case m.filtering:
		return m.filter.View()
	case m.status != "":
		if m.statusErr {
			return sRed.Render("✗ " + m.status)
		}
		return m.status
	}
	var parts []string
	if f := m.filter.Value(); f != "" {
		parts = append(parts, sYellow.Render("filtro: "+f))
	}
	for _, src := range panelSources[m.panel] {
		e, ok := m.state.Get(src)
		switch {
		case !ok:
			parts = append(parts, sDim.Render(src+" …"))
		case e.Err != "":
			s := src + ": " + e.Err
			if !e.NextRun.IsZero() {
				s += " · reintento " + e.NextRun.Local().Format("15:04:05")
			}
			parts = append(parts, sRed.Render(s))
		default:
			parts = append(parts, sDim.Render(src+" "+hace(m.now, e.SuccessAt)))
		}
	}
	if m.panel == PanelGitHub {
		if s, ok := store.Data[collector.GitHubSnapshot](m.state, "github"); ok && s.RateLimit.Limit > 0 {
			parts = append(parts, sDim.Render(fmt.Sprintf("@%s · API %d/%d", s.Viewer, s.RateLimit.Remaining, s.RateLimit.Limit)))
		}
	}
	return strings.Join(parts, sDim.Render(" · "))
}

func (m Model) helpLine() string {
	k := m.keys
	var full, compact []key.Binding
	if m.detailOpen {
		full = []key.Binding{short(k.Back, "volver"), short(k.Down, "desplazar"),
			short(k.Open, "abrir"), short(k.Pull, "pull"), short(k.Kill, "matar"), short(k.Quit, "salir")}
		compact = []key.Binding{short(k.Back, "volver"), short(k.Down, "desplazar"), actionsBinding, short(k.Quit, "salir")}
	} else {
		full = k.ShortHelp()
		compact = []key.Binding{short(k.Next, "panel"), short(k.Filter, "filtrar"), short(k.Enter, "detalle"),
			actionsBinding, short(k.Help, "ayuda"), short(k.Quit, "salir")}
	}
	// Use the longest list that fits; the shorter ones point to ? for the
	// rest. They are measured untruncated.
	h := m.help
	h.SetWidth(0)
	for _, bs := range [][]key.Binding{full, compact, {short(k.Help, "ayuda"), short(k.Quit, "salir")}} {
		if s := h.ShortHelpView(bs); ansi.StringWidth(s) <= m.width {
			return s
		}
	}
	return m.help.ShortHelpView([]key.Binding{short(k.Help, "ayuda")})
}

// actionsBinding groups o, p and x in the compact footer.
var actionsBinding = key.NewBinding(key.WithKeys("o", "p", "x"), key.WithHelp("o/p/x", "acciones"))

func (m Model) helpView() string {
	cols := make([]string, 0, 2)
	for _, group := range m.keys.FullHelp() {
		var b strings.Builder
		for i, k := range group {
			if i > 0 {
				b.WriteString("\n")
			}
			h := k.Help()
			b.WriteString(sKey.Render(fmt.Sprintf("%-10s", h.Key)) + " " + h.Desc)
		}
		cols = append(cols, b.String())
	}
	body := lipgloss.JoinVertical(lipgloss.Left,
		sBrand.Render("Atalaya")+sDim.Render(" "+m.deps.Version+" — torre de control de tus proyectos"),
		"",
		lipgloss.JoinHorizontal(lipgloss.Top, cols[0], "     ", cols[1]),
		"",
		sYellow.Render("amarillo")+sDim.Render(" trabajo sin commitear · ")+sRed.Render("rojo")+sDim.Render(" desincronizado, roto o caído"),
		sDim.Render("config: "+config.ContractHome(m.deps.Config.Path)),
	)
	return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, sBox.Render(body))
}
