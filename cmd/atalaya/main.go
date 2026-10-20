// Command atalaya is a control tower for your projects in the terminal.
//
//	atalaya                     open the dashboard
//	atalaya status [--json]     print everything once and exit
//	atalaya init [dir]          scan dir for git repos and write the config
//	atalaya version
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/otuletta/atalaya/internal/actions"
	"github.com/otuletta/atalaya/internal/ai"
	"github.com/otuletta/atalaya/internal/collector"
	"github.com/otuletta/atalaya/internal/config"
	"github.com/otuletta/atalaya/internal/demo"
	"github.com/otuletta/atalaya/internal/events"
	"github.com/otuletta/atalaya/internal/notify"
	"github.com/otuletta/atalaya/internal/scheduler"
	"github.com/otuletta/atalaya/internal/store"
	"github.com/otuletta/atalaya/internal/ui"
)

// version is set by GoReleaser.
var version = "dev"

const usage = `atalaya — torre de control para tus proyectos

Uso:
  atalaya [--config ruta]              abre el panel
  atalaya --demo                       abre el panel con datos simulados
  atalaya status [--json]              imprime el estado una vez y sale
  atalaya init [carpeta] [--depth N]   detecta repos y genera la config
  atalaya log [-n 30] [--json]         historial de lo que ha pasado
  atalaya version

La config vive en %s
`

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "atalaya:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	defPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	global := flag.NewFlagSet("atalaya", flag.ContinueOnError)
	global.SetOutput(stderr)
	cfgPath := global.String("config", defPath, "ruta del archivo de configuración")
	demoMode := global.Bool("demo", false, "datos simulados, para probar sin config o grabar el GIF")
	global.Usage = func() { fmt.Fprintf(stderr, usage, config.ContractHome(defPath)) }
	if err := global.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	rest := global.Args()
	cmd := ""
	if len(rest) > 0 {
		cmd, rest = rest[0], rest[1:]
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	switch cmd {
	case "":
		return dashboard(ctx, *cfgPath, *demoMode)
	case "status":
		fs := flag.NewFlagSet("status", flag.ContinueOnError)
		fs.SetOutput(stderr)
		asJSON := fs.Bool("json", false, "salida JSON para scripts")
		fetch := fs.Bool("fetch", false, "hace git fetch antes de calcular ahead/behind")
		timeout := fs.Duration("timeout", 20*time.Second, "tiempo máximo de recolección")
		fs.StringVar(cfgPath, "config", *cfgPath, "ruta del archivo de configuración")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return status(ctx, *cfgPath, *demoMode, *asJSON, *fetch, *timeout, stdout)
	case "init":
		fs := flag.NewFlagSet("init", flag.ContinueOnError)
		fs.SetOutput(stderr)
		depth := fs.Int("depth", 3, "profundidad máxima de búsqueda")
		fs.StringVar(cfgPath, "config", *cfgPath, "ruta del archivo de configuración")
		// Accept the folder before or after the flags.
		dir := "."
		if len(rest) > 0 && rest[0] != "" && rest[0][0] != '-' {
			dir, rest = rest[0], rest[1:]
		}
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			dir = fs.Arg(0)
		}
		return initConfig(ctx, *cfgPath, dir, *depth, stdout)
	case "log":
		fs := flag.NewFlagSet("log", flag.ContinueOnError)
		fs.SetOutput(stderr)
		n := fs.Int("n", 30, "cuántos eventos mostrar")
		asJSON := fs.Bool("json", false, "salida JSON")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		return showLog(*n, *asJSON, stdout)
	case "version", "--version":
		fmt.Fprintln(stdout, "atalaya", version)
		return nil
	case "help":
		global.Usage()
		return nil
	}
	return fmt.Errorf("subcomando desconocido %q (prueba atalaya help)", cmd)
}

func loadConfig(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if errors.Is(err, config.ErrNotFound) {
		return nil, fmt.Errorf("no hay config en %s\n\nCréala detectando tus repos:\n  atalaya init ~/ruta/a/tus/proyectos", config.ContractHome(path))
	}
	return cfg, err
}

// collectors builds every source from the config. The order is the order
// of the JSON output and of the scheduler's goroutines. stateDir, when set,
// is where the health history persists.
func collectors(cfg *config.Config, withFetch bool, stateDir string) []collector.Collector {
	iv := cfg.Intervals
	health := &collector.Health{Projects: cfg.Projects, Every: iv.Health.Duration}
	if stateDir != "" {
		health.HistoryPath = filepath.Join(stateDir, "health.jsonl")
	}
	cs := []collector.Collector{
		&collector.Git{Projects: cfg.Projects, Every: iv.Git.Duration},
		&collector.GitHub{Projects: cfg.Projects, Every: iv.GitHub.Duration},
		health,
		&collector.Ports{Projects: cfg.Projects, Ignore: cfg.Ports.Ignore, ShowEphemeral: cfg.Ports.ShowEphemeral, Every: iv.Ports.Duration},
		&collector.Docker{Projects: cfg.Projects, Every: iv.Docker.Duration},
		&collector.Ollama{Host: cfg.OllamaHost, Every: iv.Ollama.Duration},
		&collector.Agents{Projects: cfg.Projects, Patterns: cfg.Agents.Patterns, Every: iv.Agents.Duration},
	}
	for _, p := range cfg.Plugins {
		cs = append(cs, &collector.Plugin{Plugin: p, Projects: cfg.Projects})
	}
	if withFetch {
		cs = append(cs, &collector.Fetch{Projects: cfg.Projects, Every: iv.Fetch.Duration})
	}
	return cs
}

// While the terminal is in the background, the local sources (which cost
// CPU on every poll) run backgroundFactor times less often. GitHub and
// health keep their pace: they feed the notifications.
var backgroundSources = []string{"git", "ports", "agents", "docker", "ollama"}

const backgroundFactor = 5

// setup is what a command works with: the config, the sources, what the
// action keys do and the activity log.
type setup struct {
	cfg     *config.Config
	sources []collector.Collector
	actions ui.Actions
	log     *events.Log
}

// openLog opens the activity log in the state dir; without one, the log
// lives in memory.
func openLog() (*events.Log, string) {
	dir, err := config.StateDir()
	if err == nil {
		if log, err := events.Open(filepath.Join(dir, "events.jsonl")); err == nil {
			return log, dir
		}
	}
	log, _ := events.Open("")
	return log, ""
}

// load resolves what to watch: the real config, or the simulated sources of
// --demo (with a seeded activity log that is never written to disk).
func load(path string, demoMode, withFetch bool) (setup, error) {
	if demoMode {
		log, _ := events.Open("")
		demo.SeedEvents(log, time.Now())
		return setup{demo.Config(), demo.Collectors(time.Now()), demo.Actions{}, log}, nil
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return setup{}, err
	}
	log, stateDir := openLog()
	acts := actions.System{EditorSetting: cfg.Editor, Assistant: ai.Assistant{Command: cfg.AI.Command}}
	return setup{cfg, collectors(cfg, withFetch, stateDir), acts, log}, nil
}

func dashboard(ctx context.Context, path string, demoMode bool) error {
	s, err := load(path, demoMode, true)
	if err != nil {
		return err
	}
	cfg := s.cfg
	st := store.New()
	sched := scheduler.New(st, s.sources...)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go sched.Run(ctx)

	// The detector always runs: it fills the activity log; the rules only
	// decide which events also become notifications.
	go (&notify.Detector{
		Notifier: notify.System(),
		Rules: notify.Rules{CI: cfg.Notify.WantCI(), Health: cfg.Notify.WantHealth(),
			Agents: cfg.Notify.WantAgents(), Reviews: cfg.Notify.WantReviews()},
		Log:   s.log,
		Store: st,
	}).Watch(ctx, st)

	m := ui.New(ui.Deps{
		Config:  cfg,
		Store:   st,
		Refresh: sched.Refresh,
		Actions: s.actions,
		Focus: func(focused bool) {
			if focused {
				sched.SlowDown(1, backgroundSources...)
				sched.Refresh(backgroundSources...) // catch up on what changed meanwhile
			} else {
				sched.SlowDown(backgroundFactor, backgroundSources...)
			}
		},
		Version: version,
	})
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) {
		return nil
	}
	return err
}

func status(ctx context.Context, path string, demoMode, asJSON, fetch bool, timeout time.Duration, out io.Writer) error {
	s, err := load(path, demoMode, false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	st := store.New()
	if fetch && !demoMode {
		// Fetch first so ahead/behind reflects the remote.
		scheduler.New(st, &collector.Fetch{Projects: s.cfg.Projects, Every: time.Hour}).RunOnce(ctx)
	}
	scheduler.New(st, s.sources...).RunOnce(ctx)
	st.Put("events", events.Snapshot{Events: s.log.Recent(50)}, nil, time.Time{})
	snap := st.Snapshot()

	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			GeneratedAt time.Time   `json:"generated_at"`
			Version     string      `json:"version"`
			State       store.State `json:"state"`
		}{time.Now(), version, snap})
	}
	width := 120
	if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
		width = w
	}
	_, err = lipgloss.Fprint(out, ui.Summary(s.cfg, snap, time.Now(), width))
	return err
}

// showLog prints the activity log, oldest first, like a shell history.
func showLog(n int, asJSON bool, out io.Writer) error {
	log, dir := openLog()
	evs := log.Recent(n)
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(evs)
	}
	if len(evs) == 0 {
		where := "en memoria"
		if dir != "" {
			where = config.ContractHome(filepath.Join(dir, "events.jsonl"))
		}
		fmt.Fprintf(out, "Todavía no hay eventos (%s). Atalaya los registra mientras el panel está abierto.\n", where)
		return nil
	}
	icons := map[string]string{events.Bad: "✗", events.Good: "✓", events.Ask: "◉", events.Info: "·"}
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		line := fmt.Sprintf("%s  %s  %s", e.At.Local().Format("2006-01-02 15:04"), icons[e.Severity], e.Title)
		if e.Body != "" {
			line += "  — " + e.Body
		}
		fmt.Fprintln(out, line)
	}
	return nil
}

func initConfig(ctx context.Context, path, dir string, depth int, out io.Writer) error {
	res, err := config.Init(ctx, path, dir, depth)
	if err != nil {
		return err
	}
	where := config.ContractHome(res.Path)
	if len(res.Added) == 0 {
		fmt.Fprintf(out, "No hay repos nuevos en %s (%d ya estaban en %s).\n", config.ContractHome(config.ExpandHome(dir)), res.Skipped, where)
		return nil
	}
	verb := "Añadidos"
	if res.Created {
		verb = "Creado " + where + " con"
	}
	fmt.Fprintf(out, "%s %d proyectos:\n", verb, len(res.Added))
	for _, r := range res.Added {
		gh := ""
		if r.GitHub != "" {
			gh = "  github.com/" + r.GitHub
		}
		fmt.Fprintf(out, "  %-20s %s%s\n", r.Name, config.ContractHome(r.Path), gh)
	}
	if res.Skipped > 0 {
		fmt.Fprintf(out, "(%d ya estaban registrados)\n", res.Skipped)
	}
	fmt.Fprintf(out, "\nAñade health = [\"https://…\"] a los proyectos con producción y ejecuta: atalaya\n")
	return nil
}
