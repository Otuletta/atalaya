package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// column describes one table column. Columns size themselves to their
// content, so a short branch name does not reserve 20 cells.
type column struct {
	title string
	// max caps the content-based width (0: no cap).
	max int
	// min is how far the column may shrink when space is short (0: it
	// does not shrink).
	min int
	// flex takes whatever width is left, and at least min.
	flex  bool
	right bool
	// drop hides the column when even shrunk columns do not fit; higher
	// values go first. 0 never hides.
	drop int
	// hideEmpty hides the column when no row has a value in it.
	hideEmpty bool
}

// table lays out rows of styled cells to a total width.
type table struct {
	cols []column
	rows [][]string
}

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

// hidden marks a column that is not shown.
const hidden = -1

// layout resolves the width of every column (hidden for those not shown).
// Order of sacrifices when the terminal is narrow: hide droppable columns,
// then shrink the shrinkable ones, widest slack first.
func (t *table) layout(total int) []int {
	ws := make([]int, len(t.cols))
	for i, c := range t.cols {
		w, empty := ansi.StringWidth(c.title), true
		for _, r := range t.rows {
			if i < len(r) && r[i] != "" {
				empty = false
				w = max(w, ansi.StringWidth(r[i]))
			}
		}
		switch {
		case c.hideEmpty && empty:
			ws[i] = hidden
		case c.flex:
			ws[i] = max(c.min, 1) // grows at the end
		case c.max > 0:
			ws[i] = min(w, c.max)
		default:
			ws[i] = w
		}
	}
	for used(ws) > total {
		j := -1
		for i, c := range t.cols {
			if ws[i] != hidden && c.drop > 0 && (j < 0 || c.drop > t.cols[j].drop) {
				j = i
			}
		}
		if j < 0 {
			break
		}
		ws[j] = hidden
	}
	for used(ws) > total {
		j, slack := -1, 0
		for i, c := range t.cols {
			if ws[i] != hidden && !c.flex && c.min > 0 && ws[i]-c.min > slack {
				j, slack = i, ws[i]-c.min
			}
		}
		if j < 0 {
			break
		}
		ws[j]--
	}
	for i, c := range t.cols {
		if c.flex && ws[i] != hidden {
			ws[i] += max(total-used(ws), 0)
			break
		}
	}
	return ws
}

// used is the width taken by the visible columns and the gaps between them.
func used(ws []int) int {
	sum, n := 0, 0
	for _, w := range ws {
		if w != hidden {
			sum += w
			n++
		}
	}
	return sum + gap*max(n-1, 0)
}

// line renders cells with the widths from layout.
func (t *table) line(ws []int, cells []string) string {
	var b strings.Builder
	first := true
	for i, c := range t.cols {
		if ws[i] == hidden {
			continue
		}
		if !first {
			b.WriteString(strings.Repeat(" ", gap))
		}
		first = false
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		if c.right && ansi.StringWidth(cell) < ws[i] {
			cell = strings.Repeat(" ", ws[i]-ansi.StringWidth(cell)) + cell
		}
		b.WriteString(fit(cell, ws[i]))
	}
	return strings.TrimRight(b.String(), " ")
}

// header renders the column titles.
func (t *table) header(ws []int) string {
	titles := make([]string, len(t.cols))
	for i, c := range t.cols {
		titles[i] = sColHead.Render(c.title)
	}
	return t.line(ws, titles)
}

// lines renders every row.
func (t *table) lines(ws []int) []string {
	out := make([]string, len(t.rows))
	for i, r := range t.rows {
		out[i] = t.line(ws, r)
	}
	return out
}
