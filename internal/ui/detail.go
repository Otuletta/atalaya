package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/store"
)

// detailBuilder accumulates "label  value" lines.
type detailBuilder struct {
	b strings.Builder
}

func (d *detailBuilder) title(s string) {
	if d.b.Len() > 0 {
		d.b.WriteString("\n")
	}
	d.b.WriteString(sSection.Render(s) + "\n")
}

func (d *detailBuilder) kv(k string, v any) {
	s := fmt.Sprint(v)
	if s == "" {
		return
	}
	d.b.WriteString(sDim.Render(fmt.Sprintf("  %-14s", k)) + s + "\n")
}

func (d *detailBuilder) line(s string) { d.b.WriteString("  " + s + "\n") }

func (d *detailBuilder) String() string { return d.b.String() }

func stamp(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s (hace %s)", t.Local().Format("2006-01-02 15:04"), ago(now, t))
}

// detail renders the enter view of an item.
func (m Model) detail(item any) string {
	var d detailBuilder
	switch it := item.(type) {
	case collector.Repo:
		d.title(it.Project)
		d.kv("ruta", config.ContractHome(it.Path))
		d.kv("rama", it.Branch)
		d.kv("upstream", it.Upstream)
		if it.Upstream != "" {
			d.kv("sincronía", syncCell(it)+fmt.Sprintf("  (%d adelante, %d atrás)", it.Ahead, it.Behind))
		}
		if fs, ok := store.Data[collector.FetchSnapshot](m.state, "fetch"); ok {
			for _, f := range fs.Repos {
				if f.Project == it.Project {
					v := "hace " + ago(m.now, f.At)
					if f.Err != "" {
						v += "  " + sRed.Render(f.Err)
					}
					d.kv("último fetch", v)
				}
			}
		}
		d.kv("stash", it.Stash)
		if it.Err != "" {
			d.kv("error", sRed.Render(it.Err))
		}
		if c := it.LastCommit; c != nil {
			d.title("Último commit")
			d.kv("hash", c.Hash)
			d.kv("autor", c.Author)
			d.kv("fecha", stamp(m.now, c.When))
			d.kv("mensaje", c.Subject)
		}
		d.title(fmt.Sprintf("Cambios (%d)", len(it.Files)))
		if len(it.Files) == 0 {
			d.line(sDim.Render("árbol limpio"))
		}
		for _, f := range it.Files {
			d.line(xyStyle(f.XY) + "  " + f.Path)
		}
		d.title("Acciones")
		d.line("o  abrir en el editor    p  git pull --ff-only")

	case collector.GitHubRepo:
		d.title(it.Repo)
		d.kv("url", it.URL)
		d.kv("rama", it.DefaultBranch)
		if it.Err != "" {
			d.kv("error", sRed.Render(it.Err))
		}
		if ci := it.CI; ci != nil {
			d.title("CI en " + it.DefaultBranch)
			d.kv("estado", ciIcon(ci.State)+" "+ci.State)
			d.kv("workflow", ci.Workflow)
			d.kv("commit", ci.Commit)
			d.kv("inicio", stamp(m.now, ci.StartedAt))
			if ci.DurationSec > 0 {
				d.kv("duración", dur(time.Duration(ci.DurationSec)*time.Second))
			}
			d.kv("run", ci.URL)
		}
		if len(it.FailedChecks) > 0 {
			d.title("Checks fallidos")
			for _, c := range it.FailedChecks {
				d.line(sRed.Render("✗ "+c.Name) + "  " + sDim.Render(c.URL))
			}
		}
		d.title(fmt.Sprintf("PRs abiertos (%d)", it.OpenPRs))
		for _, pr := range it.PRs {
			d.line(fmt.Sprintf("%s #%d %s %s", ciIcon(pr.Checks), pr.Number, pr.Title, sDim.Render("· "+pr.Author)))
		}
		if len(it.ReviewRequests) > 0 {
			d.title("Te piden review")
			for _, pr := range it.ReviewRequests {
				d.line(fmt.Sprintf("%s #%d %s %s", sYellow.Render("◉"), pr.Number, pr.Title, sDim.Render("· "+pr.Author)))
			}
		}
		d.title("Acciones")
		d.line("o  abrir en el navegador    p  git pull --ff-only del repo local")

	case collector.PullRequest:
		d.title(fmt.Sprintf("#%d %s", it.Number, it.Title))
		d.kv("repo", it.Repo)
		d.kv("autor", it.Author)
		d.kv("rama", it.Branch)
		if it.Draft {
			d.kv("borrador", "sí")
		}
		d.kv("checks", ciIcon(it.Checks)+" "+it.Checks)
		d.kv("review", strings.ReplaceAll(it.ReviewDecision, "_", " "))
		d.kv("actualizado", stamp(m.now, it.UpdatedAt))
		d.kv("url", it.URL)
		d.title("Acciones")
		d.line("o  abrir el PR en el navegador")

	case collector.Check:
		d.title(it.URL)
		d.kv("proyecto", it.Project)
		if it.Status > 0 {
			d.kv("estado", it.Status)
		}
		if it.Err != "" {
			d.kv("error", sRed.Render(it.Err))
		}
		d.kv("latencia", fmt.Sprintf("%d ms", it.LatencyMS))
		d.kv("comprobado", stamp(m.now, it.CheckedAt))
		if t := it.TLS; t != nil {
			d.title("Certificado TLS")
			d.kv("expira", t.NotAfter.Local().Format("2006-01-02")+"  "+tlsCell(t))
			d.kv("sujeto", t.Subject)
			d.kv("emisor", t.Issuer)
		}
		d.title(fmt.Sprintf("Historial (%d muestras)", len(it.History)))
		d.line(sparkline(it.History, 60))
		for i := len(it.History) - 1; i >= 0 && i >= len(it.History)-10; i-- {
			s := it.History[i]
			mark := sGreen.Render("●")
			if !s.OK {
				mark = sRed.Render("●")
			}
			d.line(fmt.Sprintf("%s %s  %d ms", mark, s.At.Local().Format("15:04:05"), s.LatencyMS))
		}
		d.title("Acciones")
		d.line("o  abrir en el navegador")

	case collector.Port:
		d.title(fmt.Sprintf(":%d → %s", it.Port, it.Process))
		d.kv("escucha en", it.Addr)
		d.kv("pid", it.PID)
		d.kv("proyecto", it.Project)
		d.kv("directorio", config.ContractHome(it.Cwd))
		d.kv("desde", stamp(m.now, it.Started))
		d.kv("comando", it.Cmdline)
		d.title("Acciones")
		d.line(fmt.Sprintf("o  abrir http://localhost:%d    x  matar el proceso (SIGTERM)", it.Port))

	case collector.Container:
		d.title(it.Name)
		d.kv("id", it.ID)
		d.kv("imagen", it.Image)
		d.kv("estado", it.Status)
		d.kv("puertos", it.Ports)
		d.kv("proyecto", it.Project)

	case collector.OllamaModel:
		d.title(it.Name)
		d.kv("parámetros", it.Params)
		d.kv("cuantización", it.Quant)
		d.kv("tamaño", size(it.SizeBytes))
		d.kv("en VRAM", size(it.VRAMBytes))
		if !it.ExpiresAt.IsZero() {
			d.kv("se descarga", it.ExpiresAt.Local().Format("15:04:05"))
		}

	case collector.Agent:
		d.title(fmt.Sprintf("%s · pid %d", it.Name, it.PID))
		d.kv("proyecto", it.Project)
		d.kv("directorio", config.ContractHome(it.Cwd))
		d.kv("desde", stamp(m.now, it.Started))
		d.kv("comando", it.Cmdline)
		d.title("Acciones")
		d.line("o  abrir el directorio en el editor")
	}
	return d.String()
}

// xyStyle colors a porcelain XY code: staged in green, unstaged in yellow.
func xyStyle(xy string) string {
	if len(xy) != 2 {
		return xy
	}
	switch xy {
	case "??":
		return sDim.Render("??")
	case "UU", "AA", "DD", "AU", "UA", "DU", "UD":
		return sRed.Render(xy)
	}
	return sGreen.Render(string(xy[0])) + sYellow.Render(string(xy[1]))
}
