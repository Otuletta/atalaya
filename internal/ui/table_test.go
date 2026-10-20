package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTableSizesToContent(t *testing.T) {
	tb := table{cols: []column{{title: "A"}, {title: "BRANCH", max: 10}, {title: "MSG", flex: true}}}
	tb.add("x", "main", "hola")
	tb.add("yyy", "feature/very-long-name", "adiós")
	ws := tb.layout(40)
	// A: "yyy" (3); BRANCH: capped at 10; MSG: the rest (40 - 3 - 10 - 2 gaps×2).
	if ws[0] != 3 || ws[1] != 10 || ws[2] != 23 {
		t.Errorf("widths = %v", ws)
	}
	if l := tb.line(ws, tb.rows[1]); ansi.StringWidth(l) > 40 || !strings.Contains(l, "feature/v…") {
		t.Errorf("line = %q", l)
	}
}

func TestTableDropsThenShrinks(t *testing.T) {
	tb := table{cols: []column{
		{title: "NAME", min: 4},
		{title: "AUTHOR", drop: 1},
		{title: "MSG", flex: true, min: 10},
	}}
	tb.add("project-name", "Ada Lovelace", "fix: something")
	// Everything fits at 60.
	if ws := tb.layout(60); ws[1] == hidden {
		t.Errorf("author hidden with room to spare: %v", ws)
	}
	// At 30 the author goes first (12+2+10 = 24 fits), the name keeps its width.
	if ws := tb.layout(30); ws[1] != hidden || ws[0] != 12 || ws[2] != 16 {
		t.Errorf("at 30: %v", ws)
	}
	// At 18 the name shrinks toward its minimum too.
	if ws := tb.layout(18); ws[0] != 6 || ws[2] != 10 {
		t.Errorf("at 18: %v", ws)
	}
}

func TestTableHideEmptyAndRightAlign(t *testing.T) {
	tb := table{cols: []column{{title: "N"}, {title: "STASH", hideEmpty: true}, {title: "PID", right: true}}}
	tb.add("a", "", "7")
	tb.add("b", "", "1234")
	ws := tb.layout(80)
	if ws[1] != hidden {
		t.Errorf("empty column shown: %v", ws)
	}
	if got := tb.line(ws, tb.rows[0]); got != "a     7" {
		t.Errorf("line = %q", got)
	}
	if got := ansi.Strip(tb.header(ws)); got != "N   PID" {
		t.Errorf("header = %q", got)
	}
}
