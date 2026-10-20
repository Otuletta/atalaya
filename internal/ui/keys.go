package ui

import "charm.land/bubbles/v2/key"

type keyMap struct {
	Next, Prev, Panel                 key.Binding
	Up, Down, Top, Bottom, PgUp, PgDn key.Binding
	Filter, Enter, Back               key.Binding
	Open, Pull, Kill, Refresh         key.Binding
	Analyze, Fix                      key.Binding
	Help, Quit                        key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Next:    key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "siguiente panel")),
		Prev:    key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "panel anterior")),
		Panel:   key.NewBinding(key.WithKeys("0", "1", "2", "3", "4", "5", "6"), key.WithHelp("0-6", "ir al panel")),
		Up:      key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k/↑", "subir")),
		Down:    key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j/↓", "bajar")),
		Top:     key.NewBinding(key.WithKeys("g", "home"), key.WithHelp("g", "inicio")),
		Bottom:  key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G", "final")),
		PgUp:    key.NewBinding(key.WithKeys("pgup", "ctrl+u"), key.WithHelp("ctrl+u", "media página arriba")),
		PgDn:    key.NewBinding(key.WithKeys("pgdown", "ctrl+d"), key.WithHelp("ctrl+d", "media página abajo")),
		Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filtrar")),
		Enter:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "detalle")),
		Back:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "volver / limpiar filtro")),
		Open:    key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "abrir en editor / navegador")),
		Pull:    key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "git pull")),
		Kill:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "matar proceso del puerto")),
		Refresh: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refrescar todo")),
		Analyze: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "CI roto: diagnóstico de Claude")),
		Fix:     key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "CI roto: arreglar con Claude Code")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "ayuda")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "salir")),
	}
}

// short rebinds help text to fit the footer.
func short(b key.Binding, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(b.Keys()...), key.WithHelp(b.Help().Key, desc))
}

// ShortHelp is the footer line.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		short(k.Next, "panel"), short(k.Down, "navegar"), short(k.Filter, "filtrar"), short(k.Enter, "detalle"),
		short(k.Open, "abrir"), short(k.Pull, "pull"), short(k.Kill, "matar"), short(k.Refresh, "refrescar"),
		short(k.Help, "ayuda"), short(k.Quit, "salir"),
	}
}

// FullHelp is the ? overlay.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Next, k.Prev, k.Panel, k.Up, k.Down, k.Top, k.Bottom, k.PgUp, k.PgDn},
		{k.Filter, k.Enter, k.Back, k.Open, k.Pull, k.Kill, k.Analyze, k.Fix, k.Refresh, k.Help, k.Quit},
	}
}
