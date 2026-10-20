package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/otuletta/atalaya/internal/collector"
)

// Colors are the 16 ANSI colors, so the UI follows the user's terminal
// theme (light or dark) without detecting it.
var (
	cRed     = lipgloss.Color("1")
	cGreen   = lipgloss.Color("2")
	cYellow  = lipgloss.Color("3")
	cBlue    = lipgloss.Color("4")
	cMagenta = lipgloss.Color("5")
	cCyan    = lipgloss.Color("6")
	cDim     = lipgloss.Color("8")

	sRed     = lipgloss.NewStyle().Foreground(cRed)
	sGreen   = lipgloss.NewStyle().Foreground(cGreen)
	sYellow  = lipgloss.NewStyle().Foreground(cYellow)
	sBlue    = lipgloss.NewStyle().Foreground(cBlue)
	sCyan    = lipgloss.NewStyle().Foreground(cCyan)
	sMagenta = lipgloss.NewStyle().Foreground(cMagenta)
	sDim     = lipgloss.NewStyle().Foreground(cDim)
	sBold    = lipgloss.NewStyle().Bold(true)
	sBrand   = lipgloss.NewStyle().Bold(true).Foreground(cCyan)
	sTab     = lipgloss.NewStyle().Foreground(cDim).Padding(0, 1)
	sTabOn   = lipgloss.NewStyle().Bold(true).Reverse(true).Padding(0, 1)
	sColHead = lipgloss.NewStyle().Foreground(cDim).Bold(true)
	sSection = lipgloss.NewStyle().Bold(true).Foreground(cMagenta)
	sCursor  = lipgloss.NewStyle().Bold(true).Foreground(cCyan)
	sKey     = lipgloss.NewStyle().Bold(true).Foreground(cBlue)
	sBox     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cCyan).Padding(0, 2)
)

// fit truncates or pads s (which may contain ANSI styles) to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if ansi.StringWidth(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return s + strings.Repeat(" ", w-ansi.StringWidth(s))
}

// gap separates table columns.
const gap = 2

// ago is a compact relative time: "ahora", "45s", "12m", "3h", "2d"…
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < 5*time.Second:
		return "ahora"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d < 14*24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dsem", int(d.Hours()/24/7))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmes", int(d.Hours()/24/30))
	}
	return fmt.Sprintf("%da", int(d.Hours()/24/365))
}

// hace is ago as a phrase: "ahora" or "hace 3m".
func hace(now, t time.Time) string {
	a := ago(now, t)
	if a == "ahora" || a == "—" {
		return a
	}
	return "hace " + a
}

// dur formats a duration as "45s", "3m20s" or "1h05m".
func dur(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours()/24), int(d.Hours())%24)
}

// size formats bytes as "5.6 GB".
func size(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

var sparkBlocks = []rune("▁▂▃▄▅▆▇█")

// sparkline draws the latest samples, oldest on the left. Bars are scaled
// from zero to the highest latency shown, so a 10% wobble stays small and a
// spike stands out. Failed checks are a red ×.
func sparkline(samples []collector.Sample, width int) string {
	if len(samples) > width {
		samples = samples[len(samples)-width:]
	}
	var hi int64
	for _, s := range samples {
		if s.OK {
			hi = max(hi, s.LatencyMS)
		}
	}
	var b strings.Builder
	for _, s := range samples {
		if !s.OK {
			b.WriteString(sRed.Render("×"))
			continue
		}
		i := 0
		if hi > 0 {
			i = int(s.LatencyMS * int64(len(sparkBlocks)-1) / hi)
		}
		b.WriteString(sGreen.Render(string(sparkBlocks[i])))
	}
	return b.String()
}

// ciIcon is ✓ ✗ ⟳ in color.
func ciIcon(state string) string {
	switch state {
	case collector.CISuccess:
		return sGreen.Render("✓")
	case collector.CIFailure:
		return sRed.Render("✗")
	case collector.CIPending:
		return sYellow.Render("⟳")
	case collector.CICancelled:
		return sDim.Render("⊘")
	}
	return sDim.Render("·")
}
