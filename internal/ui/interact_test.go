package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/golden"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/store"
)

func (h *harness) click(x, y int) {
	h.tm.Send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
}

// at finds text on a line of the first screen, in cells.
func at(t *testing.T, w, ht int, text string) (x, y int) {
	t.Helper()
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	m.width, m.height = w, ht
	for y, l := range strings.Split(ansi.Strip(m.render()), "\n") {
		if i := strings.Index(l, text); i >= 0 {
			return ansi.StringWidth(l[:i]), y
		}
	}
	t.Fatalf("%q is not on screen", text)
	return 0, 0
}

func TestDialogConfirmsWithTheKeyboard(t *testing.T) {
	h := start(t)
	h.keys("1", "p")
	h.waitFor(t, "Confirmar")
	h.keys("right", "enter") // the focus moved to No: enter cancels
	h.waitFor(t, "cancelado")
	h.keys("p", "enter") // pull is safe: Sí has the focus
	h.waitCalls(t, 1)
	h.final(t)
}

func TestDangerousDialogDefaultsToNo(t *testing.T) {
	h := start(t)
	h.keys("4", "x")
	h.waitFor(t, "no se puede deshacer")
	h.keys("enter")
	h.waitFor(t, "cancelado")
	if calls := h.actions.Calls(); len(calls) != 0 {
		t.Errorf("enter killed a process: %v", calls)
	}
	h.final(t)
}

func TestDialogGolden(t *testing.T) {
	eachSize(t, func(t *testing.T, w, ht int) {
		h := startSized(t, w, ht)
		h.keys("4", "x")
		h.waitFor(t, "no se puede deshacer")
		golden.RequireEqual(t, []byte(screen(h.final(t))))
	})
}

func TestDialogButtonsAreClickable(t *testing.T) {
	h := start(t)
	h.keys("1", "p")
	h.waitFor(t, "Confirmar")
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	m.width, m.height = 120, 30
	m.confirm = &confirmation{prompt: "¿git pull --ff-only en odomto?", yes: true}
	d := m.dialogLayout()
	h.click(d.yes.x+1, d.yes.y)
	h.waitCalls(t, 1)
	h.final(t)
}

func TestSpinnerWhileAnActionRuns(t *testing.T) {
	release := make(chan struct{})
	fa := &fakeActions{pullFn: func() (string, error) { <-release; return "Fast-forward", nil }}
	h := startFull(t, testStore(), fa, 120, 30)
	h.keys("1", "p", "y")
	h.waitFor(t, "git pull en odomto…")
	close(release)
	h.waitFor(t, "pull odomto: Fast-forward")
	if m := h.final(t); m.busy != 0 {
		t.Errorf("busy = %d after the action ended", m.busy)
	}
}

func TestToastForANewEvent(t *testing.T) {
	st := testStore()
	h := startWith(t, st, 120, 30)
	h.waitFor(t, "Resumen")
	e := events.Event{At: now.Add(time.Minute), Kind: "health_down", Severity: events.Bad, Project: "odomto",
		Title: "Producción caída en odomto", Body: "https://api.odomto.com/health · 503"}
	old, _ := store.Data[events.Snapshot](st.Snapshot(), "events")
	st.Put("events", events.Snapshot{Events: append([]events.Event{e}, old.Events...)}, nil, now)
	h.waitFor(t, "Producción caída en odomto")
	h.click(110, 3) // on the toast: opens it in Activity
	m := h.final(t)
	if got, ok := m.selectedItem().(events.Event); m.panel != PanelActivity || !ok || got.Title != e.Title {
		t.Errorf("panel %v, selected %+v", m.panel, m.selectedItem())
	}
	if len(m.toasts) != 0 {
		t.Error("the toast stayed after it was clicked")
	}
}

func TestOldEventsDoNotToast(t *testing.T) {
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	if m.newToasts(); len(m.toasts) != 0 {
		t.Errorf("history from before the start toasted: %+v", m.toasts)
	}
}

func TestClickTabsRowsAndCards(t *testing.T) {
	x, y := at(t, 120, 30, "2 GitHub")
	h := start(t)
	h.click(x+1, y)
	h.waitFor(t, "REVIEW")
	// Line 2 is the header, 3 the repo, 4 its local branch.
	h.click(5, 4)
	h.click(5, 4) // and again: a double click opens it
	h.waitFor(t, "feat/sparkline")
	m := h.final(t)
	if m.panel != PanelGitHub || !m.detailOpen {
		t.Fatalf("panel %v, detail open %v", m.panel, m.detailOpen)
	}
	if _, ok := m.selectedItem().(branchItem); !ok {
		t.Errorf("selected %T, want the branch line", m.selectedItem())
	}

	cx, cy := at(t, 120, 30, "5 Agentes")
	h = start(t)
	h.click(cx, cy)
	h.click(cx, cy)
	if m := h.final(t); m.panel != PanelAgents {
		t.Errorf("a double click on the agents card went to %v", m.panel)
	}
}

func TestClickAPaletteResult(t *testing.T) {
	h := start(t)
	h.keys(":")
	h.tm.Type("3000")
	h.click(10, bodyTop+3) // the first result
	m := h.final(t)
	if p, ok := m.selectedItem().(collector.Port); !ok || p.Port != 3000 || m.palette != nil {
		t.Errorf("selected %+v, palette open %v", m.selectedItem(), m.palette != nil)
	}
}

func TestFooterFollowsTheSelection(t *testing.T) {
	m := New(Deps{Config: testConfig(), Store: testStore(), Now: func() time.Time { return now }})
	m.width, m.height = 120, 30
	m.panel = PanelGitHub
	if l := ansi.Strip(m.helpLine()); !strings.Contains(l, "c arreglar") {
		t.Errorf("on a broken CI the footer says %q", l)
	}
	m.panel = PanelLocal
	if l := ansi.Strip(m.helpLine()); !strings.Contains(l, "x matar") || strings.Contains(l, "arreglar") {
		t.Errorf("on a port the footer says %q", l)
	}
}
