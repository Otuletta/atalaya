// Package ui is the Bubble Tea front end. It only reads the store: every
// piece of data arrives as a store.State, so the UI never blocks on I/O.
package ui

import (
	"context"
	"fmt"
	"maps"
	"net/url"
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

	"github.com/otuletta/atalaya/internal/ai"
	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/store"
)

// Actions are the side effects behind o, p and x. Tests replace them.
type Actions interface {
	Editor(dir string) (cmd *exec.Cmd, interactive bool)
	Start(cmd *exec.Cmd) error
	OpenURL(url string) error
	Pull(ctx context.Context, dir string) (string, error)
	Kill(ctx context.Context, pid int32, started time.Time) error
	// Diagnose asks Claude why a CI failed; Fix prepares a Claude Code
	// session to repair it.
	Diagnose(ctx context.Context, f ai.Failure) (string, error)
	Fix(ctx context.Context, f ai.Failure) (*exec.Cmd, error)
	// RerunFailed re-runs the failed jobs of a GitHub Actions run; MergePR
	// merges a pull request.
	RerunFailed(ctx context.Context, runURL string) error
	MergePR(ctx context.Context, repo string, number int) error
}

// Deps is everything the UI needs from the outside.
type Deps struct {
	Config  *config.Config
	Store   *store.Store
	Refresh func(sources ...string)
	Actions Actions
	// Focus is told when the terminal gains or loses focus, to poll less
	// while nobody is looking. Terminals without focus reporting never call it.
	Focus func(focused bool)
	// Changes signals store updates for this UI. Default: Store.Changed().
	// Each SSH session has its own subscription.
	Changes <-chan struct{}
	// ReadOnly disables the action keys (an SSH viewer must not open,
	// pull or kill things on the machine that serves it).
	ReadOnly bool
	Now      func() time.Time
	Version  string
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
	// diagnosisMsg carries Claude's explanation of a broken CI.
	diagnosisMsg struct {
		failure ai.Failure
		text    string
		err     error
	}
	// fixReadyMsg carries the Claude Code command, ready to take over the
	// terminal.
	fixReadyMsg struct {
		cmd *exec.Cmd
		err error
	}
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

	panel       Panel
	overviewSel int                // selected card on the overview
	sel         [panelCount]string // key of the selected row per panel
	offset      [panelCount]int    // first visible row per panel

	filtering  bool
	showHelp   bool
	detailOpen bool
	detailKey  string
	viewport   viewport.Model
	confirm    *confirmation
	refreshing bool
	// noteFailure is the CI a diagnosis note is about, so c works there.
	noteFailure *ai.Failure
	// seenUntil is when the Activity panel was last open: newer events
	// count as unseen in its tab.
	seenUntil time.Time

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
	m.seenUntil = m.now // history from before this run is not news
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
	changes := m.deps.Changes
	if changes == nil {
		changes = st.Changed()
	}
	return func() tea.Msg {
		<-changes
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
	case diagnosisMsg:
		if msg.err != nil {
			cmd = m.setStatus(msg.err.Error(), true)
			break
		}
		m.status = ""
		m.noteFailure = &msg.failure
		m.openNote(m.diagnosisNote(msg.failure, msg.text))
	case fixReadyMsg:
		if msg.err != nil {
			cmd = m.setStatus(msg.err.Error(), true)
			break
		}
		cmd = tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			return actionMsg{text: "Claude Code cerrado", err: err, refresh: []string{"git", "github", "agents"}}
		})
	case tea.FocusMsg:
		if m.deps.Focus != nil {
			m.deps.Focus(true)
		}
	case tea.BlurMsg:
		if m.deps.Focus != nil {
			m.deps.Focus(false)
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
	if m.panel == PanelActivity {
		m.seenUntil = m.deps.Now()
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
	if m.deps.ReadOnly && key.Matches(msg, k.Open, k.Pull, k.Kill, k.Analyze, k.Fix, k.PullAll, k.Rerun, k.Merge) {
		return m, m.statusCmd("solo lectura por SSH: las acciones se hacen en la máquina que ejecuta atalaya")
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
		case key.Matches(msg, k.Analyze):
			return m.analyze(m.selectedItem())
		case key.Matches(msg, k.Fix):
			return m.askFix(m.selectedItem())
		case key.Matches(msg, k.Rerun):
			return m.askRerun(m.selectedItem())
		case key.Matches(msg, k.Merge):
			return m.askMerge(m.selectedItem())
		case key.Matches(msg, k.PullAll):
			return m.askPullAll()
		default:
			var cmd tea.Cmd
			m.viewport, cmd = m.viewport.Update(msg)
			return m, cmd
		}
		return m, nil
	}

	if m.panel == PanelOverview {
		if handled, cmd := m.overviewKey(msg); handled {
			return m, cmd
		}
	}

	switch {
	case key.Matches(msg, k.Next):
		m.panel = (m.panel + 1) % panelCount
	case key.Matches(msg, k.Prev):
		m.panel = (m.panel + panelCount - 1) % panelCount
	case key.Matches(msg, k.Panel):
		m.panel = Panel(msg.String()[0] - '0')
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
	case key.Matches(msg, k.Analyze):
		return m.analyze(m.selectedItem())
	case key.Matches(msg, k.Fix):
		return m.askFix(m.selectedItem())
	case key.Matches(msg, k.Rerun):
		return m.askRerun(m.selectedItem())
	case key.Matches(msg, k.Merge):
		return m.askMerge(m.selectedItem())
	case key.Matches(msg, k.PullAll):
		return m.askPullAll()
	case key.Matches(msg, k.Refresh):
		m.refreshing = true
		m.deps.Refresh()
		return m, m.setStatus("refrescando todas las fuentes…", false)
	case key.Matches(msg, k.Help):
		m.showHelp = true
	}
	return m, nil
}

// overviewKey handles the keys that mean something else on the overview:
// moving between cards, opening one, and the actions that need an item.
func (m *Model) overviewKey(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	k, cols := m.keys, m.overviewCols()
	switch {
	case key.Matches(msg, k.Up):
		m.moveCard(-cols)
	case key.Matches(msg, k.Down):
		m.moveCard(cols)
	case msg.String() == "left" || msg.String() == "h":
		m.moveCard(-1)
	case msg.String() == "right" || msg.String() == "l":
		m.moveCard(1)
	case key.Matches(msg, k.Top):
		m.overviewSel = 0
	case key.Matches(msg, k.Bottom):
		m.overviewSel = len(m.cards()) - 1
	case key.Matches(msg, k.Enter):
		m.panel = m.cards()[m.overviewSel].panel
	case key.Matches(msg, k.Open, k.Pull, k.Kill, k.Analyze, k.Fix, k.Rerun, k.Merge):
		return true, m.statusCmd("abre un panel con enter para actuar sobre un elemento")
	default:
		return false, nil
	}
	return true, nil
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
	if m.detailOpen && m.detailKey == "note" && m.noteFailure != nil {
		return *m.noteFailure
	}
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
	case branchItem:
		b := it.Branch
		switch {
		case b.CI != nil && b.CI.State == collector.CIFailure && b.CI.URL != "":
			return openURL(b.CI.URL)
		case b.PRURL != "":
			return openURL(b.PRURL)
		case b.Pushed:
			return openURL(b.URL)
		}
		return m.statusCmd("la rama " + b.Name + " no está en GitHub todavía")
	case botGroup:
		q := "is:pr is:open author:app/" + strings.TrimSuffix(it.Author, "[bot]")
		return openURL(it.RepoURL + "/pulls?q=" + url.QueryEscape(q))
	case collector.PullRequest:
		return openURL(it.URL)
	case collector.Check:
		return openURL(it.URL)
	case collector.Port:
		return openURL(fmt.Sprintf("http://localhost:%d", it.Port))
	case events.Event:
		if it.URL != "" {
			return openURL(it.URL)
		}
	case pluginItem:
		if it.Row.URL != "" {
			return openURL(it.Row.URL)
		}
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
	case branchItem:
		project, dir = it.Repo.Project, m.projectPath(it.Repo.Project)
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

// pullable is every repo a pull can only fast-forward: behind its
// upstream, nothing of its own to push, no uncommitted work.
func (m Model) pullable() []collector.Repo {
	gs, _ := store.Data[collector.GitSnapshot](m.state, "git")
	var out []collector.Repo
	for _, r := range gs.Repos {
		if r.Behind > 0 && r.Ahead == 0 && !r.Dirty() && r.Upstream != "" && !r.UpstreamGone && !r.Detached && r.Err == "" {
			out = append(out, r)
		}
	}
	return out
}

// names lists a few names and how many more there are.
func names(all []string, n int) string {
	if len(all) <= n {
		return strings.Join(all, ", ")
	}
	return fmt.Sprintf("%s y %d más", strings.Join(all[:n], ", "), len(all)-n)
}

// batchResult summarizes a batch: "3 ok" or "2 ok · falló kdx: …".
func batchResult(verb string, ok int, failed []string) actionMsg {
	text := fmt.Sprintf("%s: %d ok", verb, ok)
	if len(failed) == 0 {
		return actionMsg{text: text}
	}
	return actionMsg{err: fmt.Errorf("%s · falló %s", text, strings.Join(failed, " · "))}
}

// askPullAll fast-forwards every repo that is simply behind.
func (m Model) askPullAll() (Model, tea.Cmd) {
	repos := m.pullable()
	if len(repos) == 0 {
		return m, m.statusCmd("ningún repo está atrasado y limpio: nada que traer")
	}
	var list []string
	for _, r := range repos {
		list = append(list, fmt.Sprintf("%s ↓%d", r.Project, r.Behind))
	}
	a := m.deps.Actions
	m.confirm = &confirmation{
		prompt: fmt.Sprintf("¿git pull --ff-only en %s (%s)?", plural(len(repos), "repo", "repos"), names(list, 4)),
		run: tea.Sequence(m.statusCmd("git pull en "+plural(len(repos), "repo", "repos")+"…"), func() tea.Msg {
			var ok int
			var failed []string
			for _, r := range repos {
				if _, err := a.Pull(context.Background(), r.Path); err != nil {
					failed = append(failed, r.Project+": "+err.Error())
					continue
				}
				ok++
			}
			msg := batchResult("pull", ok, failed)
			msg.refresh = []string{"git", "github"}
			return msg
		}),
	}
	return m, nil
}

// askRerun re-runs the failed jobs of a broken CI.
func (m Model) askRerun(item any) (Model, tea.Cmd) {
	f, ok := m.failure(item)
	if !ok || m.deps.Actions == nil {
		return m, m.statusCmd("R relanza un CI roto de GitHub Actions (panel 2)")
	}
	a := m.deps.Actions
	m.confirm = &confirmation{
		prompt: fmt.Sprintf("¿Relanzar los jobs fallidos del CI de %s en %s?", f.Project, f.Branch),
		run: func() tea.Msg {
			if err := a.RerunFailed(context.Background(), f.RunURL); err != nil {
				return actionMsg{err: fmt.Errorf("rerun %s: %w", f.Project, err)}
			}
			return actionMsg{text: "CI de " + f.Project + " relanzado: los jobs fallidos vuelven a correr",
				refresh: []string{"github"}}
		},
	}
	return m, nil
}

// mergeable is a PR whose checks pass and nobody asked to change.
func mergeable(pr collector.PullRequest) bool {
	return pr.Checks == collector.CISuccess && !pr.Draft && pr.ReviewDecision != "CHANGES_REQUESTED"
}

// askMerge merges the green PRs of a bot group, or one green PR.
func (m Model) askMerge(item any) (Model, tea.Cmd) {
	var prs []collector.PullRequest
	var what string
	switch it := item.(type) {
	case botGroup:
		for _, pr := range it.PRs {
			if mergeable(pr) {
				prs = append(prs, pr)
			}
		}
		if len(prs) == 0 {
			return m, m.statusCmd(fmt.Sprintf("ninguno de los %d PRs de %s tiene los checks en verde", len(it.PRs), it.Author))
		}
		what = fmt.Sprintf("%s de %s en %s", plural(len(prs), "PR", "PRs"), it.Author, it.Repo)
	case collector.PullRequest:
		if !mergeable(it) {
			return m, m.statusCmd(fmt.Sprintf("#%d no tiene los checks en verde: no se fusiona", it.Number))
		}
		prs, what = []collector.PullRequest{it}, fmt.Sprintf("#%d de %s", it.Number, it.Repo)
	default:
		return m, m.statusCmd("M fusiona PRs en verde: un grupo de bots o un PR (panel 2)")
	}
	if m.deps.Actions == nil {
		return m, nil
	}
	var nums []string
	for _, pr := range prs {
		nums = append(nums, fmt.Sprintf("#%d", pr.Number))
	}
	a := m.deps.Actions
	m.confirm = &confirmation{
		prompt: fmt.Sprintf("¿Merge (squash) de %s: %s?", what, names(nums, 6)),
		run: tea.Sequence(m.statusCmd("fusionando "+what+"…"), func() tea.Msg {
			var ok int
			var failed []string
			for _, pr := range prs {
				if err := a.MergePR(context.Background(), pr.Repo, pr.Number); err != nil {
					failed = append(failed, fmt.Sprintf("#%d: %v", pr.Number, err))
					continue
				}
				ok++
			}
			msg := batchResult("merge", ok, failed)
			msg.refresh = []string{"github"}
			return msg
		}),
	}
	return m, nil
}

// failure is the broken CI an item stands for: a repo whose default
// branch fails, or the line of a local branch that fails.
func (m Model) failure(item any) (ai.Failure, bool) {
	switch it := item.(type) {
	case ai.Failure:
		return it, true
	case collector.GitHubRepo:
		if ci := it.CI; ci != nil && ci.State == collector.CIFailure && ci.URL != "" {
			return ai.Failure{Project: it.Project, Dir: m.projectPath(it.Project), Branch: it.DefaultBranch,
				Workflow: ci.Workflow, RunURL: ci.URL}, true
		}
	case branchItem:
		if ci := it.Branch.CI; ci != nil && ci.State == collector.CIFailure && ci.URL != "" {
			return ai.Failure{Project: it.Repo.Project, Dir: m.projectPath(it.Repo.Project), Branch: it.Branch.Name,
				Workflow: ci.Workflow, RunURL: ci.URL}, true
		}
	}
	return ai.Failure{}, false
}

const notCI = "a y c funcionan sobre un CI roto de GitHub Actions (panel 2)"

// analyze asks Claude, in the background, why the CI failed.
func (m Model) analyze(item any) (Model, tea.Cmd) {
	f, ok := m.failure(item)
	if !ok || m.deps.Actions == nil {
		return m, m.statusCmd(notCI)
	}
	a := m.deps.Actions
	m.status, m.statusErr = "pidiendo a Claude un diagnóstico del CI de "+f.Project+"…", false
	return m, func() tea.Msg {
		text, err := a.Diagnose(context.Background(), f)
		return diagnosisMsg{failure: f, text: text, err: err}
	}
}

// askFix confirms before handing the terminal to Claude Code.
func (m Model) askFix(item any) (Model, tea.Cmd) {
	f, ok := m.failure(item)
	if !ok || m.deps.Actions == nil {
		return m, m.statusCmd(notCI)
	}
	if f.Dir == "" {
		return m, m.statusCmd(f.Project + " no tiene un checkout local donde arreglarlo")
	}
	a := m.deps.Actions
	m.confirm = &confirmation{
		prompt: fmt.Sprintf("¿Abrir Claude Code en %s para arreglar el CI de %s?", f.Project, f.Branch),
		run: tea.Sequence(m.statusCmd("descargando el log del CI…"), func() tea.Msg {
			cmd, err := a.Fix(context.Background(), f)
			return fixReadyMsg{cmd: cmd, err: err}
		}),
	}
	return m, nil
}

// openNote shows a text in the detail viewport (esc closes it).
func (m *Model) openNote(text string) {
	m.detailOpen, m.detailKey = true, "note"
	m.resizeViewport()
	m.viewport.SetContent(text)
	m.viewport.GotoTop()
}

func (m Model) diagnosisNote(f ai.Failure, text string) string {
	var d detailBuilder
	d.title(fmt.Sprintf("Diagnóstico de Claude · CI de %s en %s", f.Project, f.Branch))
	d.kv("workflow", f.Workflow)
	d.kv("run", f.RunURL)
	d.b.WriteString("\n")
	for _, l := range strings.Split(ansi.Wordwrap(text, max(m.width-6, 20), ""), "\n") {
		d.line(l)
	}
	d.title("Acciones")
	d.line("c  abrir Claude Code para arreglarlo    esc  volver")
	return d.String()
}

// ---- view ----

func (m Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	v.ReportFocus = true
	v.WindowTitle = m.windowTitle()
	if pct, done := m.loadProgress(); !done {
		v.ProgressBar = tea.NewProgressBar(tea.ProgressBarDefault, pct)
	}
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
	case m.panel == PanelOverview:
		lines = m.overviewLines(body)
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
	if m.deps.ReadOnly {
		clock = sMagenta.Render("ssh · solo lectura ") + clock
	}
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
		style, label := sTab, fmt.Sprint(int(p))
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
		lines = append(lines, "")
		// Explanations are sentences: wrap them instead of cutting them.
		for _, l := range strings.Split(ansi.Wordwrap(msg, max(m.width-4, 20), ""), "\n") {
			lines = append(lines, "  "+l)
		}
		return lines
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
	if m.panel == PanelOverview {
		return strings.Join(append(parts, m.overviewStatus()), sDim.Render(" · "))
	}
	for _, src := range m.sources() {
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

// sources are the collectors behind the current panel.
func (m Model) sources() []string {
	if m.panel == PanelPlugins {
		var srcs []string
		for _, p := range m.deps.Config.Plugins {
			srcs = append(srcs, collector.PluginSource(p.Name))
		}
		return srcs
	}
	return panelSources[m.panel]
}

// overviewStatus names only the failing sources: on the overview, silence
// means everything is fine.
func (m Model) overviewStatus() string {
	names := slices.Sorted(maps.Keys(m.state.Entries))
	var errs []string
	for _, n := range names {
		if e := m.state.Entries[n]; e.Err != "" {
			errs = append(errs, sRed.Render(n+": "+e.Err))
		}
	}
	if len(errs) == 0 {
		return sDim.Render(fmt.Sprintf("%d fuentes al día · enter abre una tarjeta", len(names)))
	}
	return strings.Join(errs, sDim.Render(" · "))
}

func (m Model) helpLine() string {
	k := m.keys
	var full, compact []key.Binding
	if m.detailOpen {
		// In a detail q closes it, like esc; ctrl+c still quits.
		full = []key.Binding{short(k.Back, "volver"), short(k.Down, "desplazar"),
			short(k.Open, "abrir"), short(k.Pull, "pull"), short(k.Kill, "matar"), aiBinding}
		compact = []key.Binding{short(k.Back, "volver"), short(k.Down, "desplazar"), actionsBinding}
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

// aiBinding groups a and c, the Claude actions on a broken CI.
var aiBinding = key.NewBinding(key.WithKeys("a", "c"), key.WithHelp("a/c", "Claude"))

// actionsBinding groups o, p and x in the compact footer.
var actionsBinding = key.NewBinding(key.WithKeys("o", "p", "x"), key.WithHelp("o/p/x", "acciones"))

func (m Model) helpView() string {
	cols := make([]string, 0, 2)
	for _, group := range m.keys.FullHelp() {
		keyWidth := 0
		for _, k := range group {
			keyWidth = max(keyWidth, lipgloss.Width(k.Help().Key))
		}
		var b strings.Builder
		for i, k := range group {
			if i > 0 {
				b.WriteString("\n")
			}
			h := k.Help()
			b.WriteString(sKey.Render(h.Key+strings.Repeat(" ", keyWidth-lipgloss.Width(h.Key))) + "  " + h.Desc)
		}
		cols = append(cols, b.String())
	}
	body := lipgloss.JoinVertical(lipgloss.Left,
		sBrand.Render("Atalaya")+sDim.Render(" "+m.deps.Version+" — torre de control de tus proyectos"),
		"",
		lipgloss.JoinHorizontal(lipgloss.Top, cols[0], "    ", cols[1]),
		"",
		sYellow.Render("amarillo")+sDim.Render(" trabajo sin commitear · ")+sRed.Render("rojo")+sDim.Render(" desincronizado, roto o caído"),
		sDim.Render("config: "+config.ContractHome(m.deps.Config.Path)),
	)
	return lipgloss.Place(m.width, m.height-4, lipgloss.Center, lipgloss.Center, sBox.Render(body))
}
