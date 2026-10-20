# Atalaya

[Español](README.md) · **English**

**A control tower for your projects, in the terminal.** One command, `atalaya`, opens a live dashboard of all your repos: git, GitHub CI and PRs, production health, local ports, containers, Ollama models and your coding agents — what they are doing and when they are waiting for you. It remembers what happened, tells you when something breaks and, with one key, asks Claude to explain it or fix it.

![demo](demo.gif)

It replaces the ritual of opening GitHub, checking CI, making sure production answers, finding which process holds port 3000 and remembering which branch each repo was on.

> Atalaya's interface is in Spanish. *Atalaya* means watchtower.

## What it shows

| Panel | Contents |
|---|---|
| **0 Resumen** (Overview) | The home screen: one card per source with only what needs attention. `enter` opens the card's panel. |
| **1 Repos** | Branch, commits ahead/behind the remote, changes (staged, modified, untracked, conflicts), stash and last commit. **Yellow**: uncommitted work. **Red**: out of sync. |
| **2 GitHub** | Latest CI run per repo (✓ ✗ ⟳ with duration), the CI and PR of **the branch you are working on**, open PRs with their checks, PRs that request your review (◉) and failed checks. Bot PRs (dependabot, renovate…) are grouped in one row per bot. |
| **3 Producción** (Production) | HTTP health checks: status, latency, **24 h uptime and p95**, a sparkline and **days left on the TLS certificate**. History is kept: the detail view has 7-day uptime and incidents. |
| **4 Local** | Listening ports mapped to process and project (`:3000 → node · odomto`), Docker containers and models loaded in Ollama. |
| **5 Agentes** (Agents) | Claude Code or Cline sessions: repo, branch or worktree (⎇), CPU and, from Claude Code's transcript, **what it is doing** ("editando model.go", "Bash: go test"), whether it is **waiting for you** (◉) or looks stuck on a permission prompt (⏸). On top, **what they cost**: tokens and the equivalent API cost per session, per project and per day (7 days). |
| **6 Actividad** (Activity) | The tower's memory: CI broken and fixed, outages and recoveries, review requests, PRs opened and closed, agents that start, finish or wait for you. The tab counts what you have not seen. |
| **7 Plugins** | Your own sources: any program that prints a table as JSON. Three examples ship with it: disk, Homebrew and `npm audit` per project. |

**Modern terminal touches.** PR numbers, CI runs, endpoints and ports are clickable links (OSC 8, cmd+click in iTerm2, Ghostty, WezTerm…). The terminal tab shows the state (`atalaya · 2 CI rotos · 1 caído`) even in the background, and the terminal's native progress bar shows while loading or while an action runs.

**Notifications.** With Atalaya open, even in the background, you get a system notification when a CI breaks or is fixed (on the default branch or yours), when an endpoint goes down or recovers, when someone requests your review and when an agent ends its turn and waits for you. Only *changes* notify: nothing on startup for what was already broken, nothing repeated, and an endpoint is not down until it fails twice in a row.

## Install

```bash
brew install otuletta/tap/atalaya
```

Or with Go 1.27+:

```bash
go install github.com/otuletta/atalaya/cmd/atalaya@latest
```

Requirements: `git`. For `a` and `c`, the [Claude Code CLI](https://docs.claude.com/claude-code). For the GitHub panel, the [`gh` CLI](https://cli.github.com), logged in (`gh auth login`); Atalaya reuses its token and never asks for or stores credentials. Docker and Ollama are optional: without them, their section says so.

## Getting started

```bash
atalaya init ~/Documents     # finds your repos and writes ~/.config/atalaya/config.toml
atalaya                      # opens the dashboard
atalaya --demo               # or try it with simulated data, no config needed
```

`init` reads the GitHub `owner/repo` from the `origin` remote. Run it again on another folder and it **appends** the new repos without touching what you already edited.

## Keys

| Key | Action |
|---|---|
| `tab` / `shift+tab` / `0-7` | Switch panel |
| `j` `k` or arrows, `g` `G`, `ctrl+u` `ctrl+d` | Move (on the overview, `h` `l` move between cards too) |
| `:` or `ctrl+k` | **Palette**: type a few letters to jump to any repo, PR, port, agent, event or panel, or run a command. Matching is fuzzy (`odo bump` finds odomto's dependabot PRs) and ignores accents |
| `/` | Filter projects (`esc` clears it) |
| `enter` | Item detail: changed files, PRs, latency history, process command… |
| `o` | Open: the repo in your editor, the PR or failed run in the browser, `localhost:<port>` |
| `p` | `git pull --ff-only` the selected repo (asks first) |
| `x` | Kill a port's process with `SIGTERM` (asks first) |
| `P` | `git pull --ff-only` **every** repo that is behind and clean: no uncommitted changes, no commits of its own to push (asks first) |
| `a` | On a broken CI: **Claude's diagnosis**. Downloads the failed job's log, trims it to what surrounds the error and asks Claude for the cause and the fix |
| `c` | On a broken CI: opens **Claude Code** in the repo with that log as context (asks first). When you exit, you are back in Atalaya |
| `R` | On a broken CI: re-runs only the failed jobs (`gh run rerun --failed`, asks first) |
| `M` | On a bot's group: merges (squash) **all** its PRs with green checks; on a PR, just that one. Falls back to a merge commit when the repo does not allow squash (asks first) |
| `r` | Refresh every source now |
| `?` | Help |
| `q` | Quit |

**The mouse works too**: click a tab, an overview card or a row to select it; double-click to open it. The wheel scrolls. In the palette, a click picks the result.

The last line changes with the selection and tells you what you can do there: on a broken CI, `a diagnóstico · c arreglar · R relanzar`; on a port, `o abrir · x matar`. While an action works (a pull, a merge, Claude's diagnosis) a spinner turns, in the terminal tab too when supported. What happens while you look at another panel (a CI that breaks, production going down, an agent waiting for you) shows for a few seconds as a toast in the corner; clicking it opens it in Activity.

Destructive actions always ask first, in a dialog: `y` confirms, `n` or `esc` cancel. Those that cannot be undone (killing a process, a merge) are red and start with the focus on "No", so a stray `enter` does not trigger them. `p` uses `--ff-only`, so it never creates a merge or leaves conflicts. `x` checks that the PID is still the process you saw, so it never kills a recycled one.

## Script mode

```bash
atalaya status            # every panel as text, once
atalaya status --json     # the same as JSON, for scripts
atalaya status --fetch    # git fetch before computing ahead/behind
atalaya log               # the event history, like a `git log` of your projects
atalaya doctor            # checks config, git, GitHub, Docker, Ollama, Claude Code…
```

```bash
# Which repos have a broken CI?
atalaya status --json | jq -r '.state.sources.github.data.repos[] | select(.ci.state == "failure") | .project'
```

The TUI and `--json` read the same store; the UI is just one more view of the data.

## In your prompt, your menu bar and your diagnostics

With Atalaya open (or `atalaya serve`), `atalaya prompt` answers in milliseconds by reading the state the dashboard keeps in `status.json`. Inside a project it shows that repo's state (`✗ CI ↓3 ◉`); outside, the global counters (`✗2 ◉1`). If Atalaya is closed it prints nothing, so your prompt never lies.

```toml
# ~/.config/starship.toml
[custom.atalaya]
command = "atalaya prompt"
when = true
format = "[$output]($style) "
style = "bold red"
```

For the **macOS menu bar** with [SwiftBar](https://swiftbar.app), copy [`examples/swiftbar/atalaya.30s.sh`](examples/swiftbar/atalaya.30s.sh) to your plugins folder: a title with the counters, one item per project (linked to the broken run or the repo) and "Abrir Atalaya".

`atalaya doctor` checks everything Atalaya depends on and says how to fix what is missing: config, git, the GitHub token and access to each repo (with a real query), Docker, Ollama, the Claude Code CLI, plugins and notifications.

## Atalaya from another machine (SSH)

```bash
atalaya serve                     # listens on localhost:2222
atalaya serve --addr :2222        # on your LAN: ssh -p 2222 your-mac.local
```

Every connection gets the full dashboard in its terminal, from a phone too with any SSH client. They all share the same collectors (one GitHub query for everyone) and every session is **read-only**: `o`, `p`, `x`, `a`, `c`, `P`, `R` and `M` are disabled because they would act on the serving machine. Only keys in `~/.ssh/authorized_keys` (or the file given with `--authorized-keys`) get in; without that file, `serve` does not start. The host key is generated once in the state folder. Built with [Wish](https://github.com/charmbracelet/wish).

## Configuration

`~/.config/atalaya/config.toml` (honors `$XDG_CONFIG_HOME`; another path with `--config`):

```toml
editor = "code"                        # for the o key; defaults to $VISUAL, $EDITOR or the first of cursor/code/zed/subl

[[project]]
name = "odomto"
path = "~/dev/odomto"
github = "otuletta/odomto"             # optional
health = ["https://api.odomto.com/health"]   # optional

[[project]]
name = "kdx"
path = "~/dev/kdx"

[agents]
patterns = ["claude", "cline"]         # globs on the executable name; case-sensitive

[github]
bot_authors = ["dependabot", "renovate", "github-actions"]  # their PRs share one row; "…[bot]" accounts always count

[ai]                                   # keys a and c
command = ["claude"]                   # the Claude Code CLI (reuses its login, no API keys)

[[plugin]]                             # a source of your own (see examples/plugins)
name = "npm-audit"
command = ["python3", "~/atalaya/examples/plugins/npm-audit.py"]
interval = "30m"

[notify]                               # system notifications (macOS; notify-send on Linux)
enabled = true
ci = true                              # a CI breaks or is fixed
health = true                          # an endpoint goes down or recovers
agents = true                          # an agent ends its turn or gets stuck on a permission
reviews = true                         # someone requests your review

[ports]
ignore = ["rapportd", "ControlCenter"] # processes to hide (macOS daemons by default)
show_ephemeral = false                 # ports ≥ 49152 that belong to no project

[intervals]                            # optional; these are the defaults
git = "5s"
fetch = "10m"
github = "60s"
health = "30s"
ports = "3s"
agents = "5s"
usage = "60s"
docker = "10s"
ollama = "10s"
```

What Atalaya learns lives elsewhere, in `~/.local/state/atalaya/` (or `$XDG_STATE_HOME/atalaya`): `events.jsonl` (activity) and `health.jsonl` (a week of health checks).

Validation reports **every** error at once, misspelled keys included (`helth = …`).

**No restart needed**: when you save the file, Atalaya reloads it within a couple of seconds and says so in the status line (`config recargada · 13 proyectos (+atalaya)`). Save it with an error and it shows you, keeping the previous config until you fix it. With `atalaya serve`, new SSH sessions see the new config.

## Architecture

The design decisions, what was measured and what was discarded are in [docs/arquitectura.md](docs/arquitectura.md) (Spanish). In short:

- **The `Collector` interface** (`internal/collector`): `Name()`, `Interval()` and `Collect(ctx) (Snapshot, error)`. Adding a source (Vercel, Sentry, Railway) is implementing those three methods and registering it in `cmd/atalaya`. Neither the scheduler nor the store change.
- **…or without writing Go: plugins.** A `[[plugin]]` is one more collector that runs a command, passes your projects in `$ATALAYA_PROJECTS` and reads a JSON table. The protocol is in [`examples/plugins`](examples/plugins/README.md).
- **Concurrency.** Every collector runs in its own goroutine with its own timeout. If GitHub goes down or a collector panics, the rest keep going: that panel keeps its last good data and shows the error and when it retries.
- **A store with a coalescing signal.** Collectors never block waiting for the UI. The UI gets a signal, reads a full copy of the state and cannot miss an update. The TUI, the event detector, every SSH session and `status.json` each have their own subscription.
- **Git without side effects.** `git status --porcelain=v2 -z` with `GIT_OPTIONAL_LOCKS=0`: polling never takes `index.lock` or collides with your own commands.
- **GitHub in a single GraphQL query**, one alias per repo and per local branch, built at runtime with `reflect.StructOf`. It costs 1 rate-limit point per minute.
- **Agents through their transcripts.** Claude Code keeps every session in `~/.claude/projects/<folder>/<id>.jsonl`; Atalaya matches each process with its own and infers the state from the last message. Cost is read incrementally from the same files.
- **Cheap at rest.** Starting a git process costs ~8 ms, so Atalaya starts as few as it can: the last commit is read again only when HEAD moves, an idle repo is polled every 15 s unless git's own files show it moved, processes are listed with a single `sysctl`. Polling slows down 5× while the terminal is in the background.

## Development

```bash
go test -race ./...                 # everything, with the race detector
golangci-lint run                   # lint (config in .golangci.yml)
go test ./internal/ui -update       # regenerates the golden files after an intended visual change
./internal/gitparse/testdata/gen.sh # regenerates the git fixtures from real repos
go test -fuzz FuzzParse ./internal/gitparse
go test -tags measure -run TestMeasure -v ./internal/collector   # CPU spent by each source with your config
ATALAYA_CPUPROFILE=cpu.prof atalaya && go tool pprof -top cpu.prof   # CPU profile of the process itself
```

The UI tests use [teatest](https://github.com/charmbracelet/x/tree/main/exp/teatest): they drive the program with real keys and clicks and compare every panel with a golden file (`internal/ui/testdata`) at 120×30 and 80×24.

## Release

```bash
vhs demo.tape                       # records demo.gif with simulated, deterministic data
git tag v0.1.0 && git push --tags   # the release workflow runs GoReleaser
```

The release workflow publishes binaries for macOS and Linux (amd64 and arm64) and updates the cask in `otuletta/homebrew-tap`, which needs the `HOMEBREW_TAP_GITHUB_TOKEN` secret. With the `MACOS_SIGN_P12`, `MACOS_SIGN_PASSWORD`, `MACOS_NOTARY_ISSUER_ID`, `MACOS_NOTARY_KEY_ID` and `MACOS_NOTARY_KEY` secrets (a Developer ID certificate and an App Store Connect API key, base64), the macOS binaries are signed and notarized from Linux; without them that step is skipped. `goreleaser release --snapshot --clean` tries it all locally without publishing.

## Known limitations

- **macOS first.** Linux works (CI runs the tests on both). Windows is out of scope.
- **Agents are detected by process name.** Fragile by nature, which is why the patterns are configurable.
- **Session state reads Claude Code's transcript**, an internal format that may change. If it does, the panel still shows the processes, just without "what it is doing".
- **Ports are read with `lsof` on macOS** (through gopsutil). Without `sudo` you only see your own user's processes.

## License

[MIT](LICENSE) © 2026 Oscar Tuletta
