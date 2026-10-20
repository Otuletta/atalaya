package ui

import (
	"strings"
	"time"

	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/store"
)

// Summary renders every panel as text, without the interactive chrome. It
// backs `atalaya status`: the same rows as the TUI, from the same store.
func Summary(cfg *config.Config, st store.State, now time.Time, width int) string {
	m := New(Deps{Config: cfg, Now: func() time.Time { return now }})
	m.state, m.width = st, width
	var b strings.Builder
	for p := PanelRepos; p < panelCount; p++ {
		pv := m.build(p)
		b.WriteString(sSection.Render(strings.ToUpper(panelNames[p])) + m.badge(p) + "\n")
		if len(pv.rows) == 0 {
			b.WriteString("  " + pv.empty + "\n\n")
			continue
		}
		if pv.header != "" {
			b.WriteString("  " + pv.header + "\n")
		}
		for _, r := range pv.rows {
			b.WriteString(strings.TrimRight("  "+r.line, " ") + "\n")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
