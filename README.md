# Atalaya

**Torre de control para tus proyectos en la terminal.** Un comando, `atalaya`, abre un panel con el estado en tiempo real de todos tus repos: git, CI y PRs de GitHub, salud de producción, puertos locales, contenedores, modelos de Ollama y agentes de código corriendo.

![demo](demo.gif)

Sustituye el ritual de abrir GitHub, revisar el CI, comprobar que producción responde, buscar qué proceso ocupa el puerto 3000 y recordar en qué rama quedó cada repo.

## Qué muestra

| Panel | Contenido |
|---|---|
| **0 Resumen** | La pantalla de inicio: una tarjeta por fuente con solo lo que necesita atención. `enter` abre el panel de la tarjeta. |
| **1 Repos** | Rama, commits adelante/atrás del remoto, cambios (staged, modificados, sin seguimiento, conflictos), stash y último commit. **Amarillo**: trabajo sin commitear. **Rojo**: desincronizado. |
| **2 GitHub** | Último run de CI por repo (✓ ✗ ⟳ con duración), el CI y el PR de **la rama en la que estás trabajando**, PRs abiertos con el estado de sus checks, PRs donde te piden review (◉) y checks fallidos. Los PRs de bots (dependabot, renovate…) se agrupan en una fila por bot. |
| **3 Producción** | Health checks HTTP: código, latencia, **uptime de 24 h y p95**, sparkline y **días restantes del certificado TLS**. El historial se guarda: el detalle tiene uptime de 7 días e incidentes. |
| **4 Local** | Puertos en escucha mapeados a proceso y proyecto (`:3000 → node · odomto`), contenedores Docker y modelos cargados en Ollama. |
| **6 Actividad** | La memoria de la torre: CI rotos y arreglados, caídas y recuperaciones, reviews que te piden, PRs nuevos y cerrados, agentes que empiezan, terminan o te esperan. La pestaña cuenta lo que no has visto. |
| **7 Plugins** | Tus propias fuentes: cualquier programa que imprima una tabla en JSON. Vienen tres de ejemplo: disco, Homebrew y `npm audit` por proyecto. |
| **5 Agentes** | Sesiones de Claude Code o Cline: repo, rama o worktree (⎇), CPU y, leyendo la transcripción de Claude Code, **qué está haciendo** («editando model.go», «Bash: go test») o si **te espera** (◉) o parece atascada en un permiso (⏸). Los worktrees de cada repo se ven en su detalle. |

**Detalles de terminal moderna.** Los números de PR, los runs de CI, los endpoints y los puertos son enlaces clicables (OSC 8, cmd+clic en iTerm2, Ghostty, WezTerm…). La pestaña de la terminal muestra el estado (`atalaya ✗2 CI · ✗1 prod`) aunque esté en segundo plano, y mientras carga o refresca aparece la barra de progreso nativa de la terminal.

**Notificaciones.** Con Atalaya abierto aunque esté en segundo plano, recibes una notificación del sistema cuando un CI se rompe o se arregla (en la rama por defecto o en la tuya) cuando un endpoint se cae o se recupera, y cuando un agente termina su turno y te espera. Solo se avisan los *cambios*: nada al arrancar por lo que ya estaba roto, nada repetido, y un endpoint no cuenta como caído hasta fallar dos veces seguidas.

## Instalación

```bash
brew install otuletta/tap/atalaya
```

O con Go 1.27+:

```bash
go install github.com/otuletta/atalaya/cmd/atalaya@latest
```

Requisitos: `git`. Para `a` y `c`, la [CLI de Claude Code](https://docs.claude.com/claude-code). Para el panel de GitHub, la [CLI `gh`](https://cli.github.com) con sesión iniciada (`gh auth login`); Atalaya reutiliza su token y no pide ni guarda credenciales. Docker y Ollama son opcionales: si no están, su sección lo indica.

## Primeros pasos

```bash
atalaya init ~/Documents     # detecta los repos y genera ~/.config/atalaya/config.toml
atalaya                      # abre el panel
atalaya --demo               # o pruébalo con datos simulados, sin config
```

`init` detecta el `owner/repo` de GitHub a partir del remoto `origin`. Si lo ejecutas otra vez sobre otra carpeta, **añade** los repos nuevos al final del archivo sin tocar lo que ya editaste.

## Teclas

| Tecla | Acción |
|---|---|
| `tab` / `shift+tab` / `0-7` | Cambiar de panel |
| `j` `k` o flechas, `g` `G`, `ctrl+u` `ctrl+d` | Navegar |
| `/` | Filtrar proyectos (`esc` limpia el filtro) |
| `enter` | Detalle del item: archivos cambiados, PRs, historial de latencia, comando del proceso… |
| `o` | Abrir: el repo en tu editor, el PR o el run fallido en el navegador, `localhost:<puerto>` |
| `p` | `git pull --ff-only` del repo seleccionado (pide confirmación) |
| `x` | Matar el proceso de un puerto con `SIGTERM` (pide confirmación) |
| `a` | Sobre un CI roto: **diagnóstico de Claude**. Descarga el log del job fallido, lo recorta a lo que rodea el error y le pide a Claude la causa y el arreglo |
| `c` | Sobre un CI roto: abre **Claude Code** en el repo con ese log como contexto (pide confirmación). Al salir, vuelves a Atalaya |
| `r` | Refrescar todas las fuentes ya |
| `?` | Ayuda |
| `q` | Salir |

Las acciones destructivas siempre piden confirmación. `p` usa `--ff-only`, así que nunca crea un merge ni deja conflictos. `x` comprueba que el PID sigue siendo el mismo proceso que viste, para no matar uno reciclado.

## Modo script

```bash
atalaya status            # todos los paneles como texto, una vez
atalaya status --json     # lo mismo en JSON, para scripts
atalaya status --fetch    # hace git fetch antes de calcular adelante/atrás
atalaya log               # el historial de eventos, como un `git log` de tus proyectos
```

```bash
# ¿Qué repos tienen el CI roto?
atalaya status --json | jq -r '.state.sources.github.data.repos[] | select(.ci.state == "failure") | .project'
```

La TUI y `--json` leen el mismo store; la UI solo es una vista más de los datos.

## Atalaya desde otra máquina (SSH)

```bash
atalaya serve                     # escucha en localhost:2222
atalaya serve --addr :2222        # en tu red local: ssh -p 2222 tu-mac.local
```

Cada conexión recibe el panel completo en su terminal, también desde el móvil con cualquier cliente SSH. Todas comparten los mismos collectors (una sola consulta a GitHub para todos) y cada sesión es **de solo lectura**: `o`, `p`, `x`, `a` y `c` se desactivan, porque actuarían en la máquina que sirve. Solo entran las claves de `~/.ssh/authorized_keys` (o el archivo que indiques con `--authorized-keys`); sin ese archivo, `serve` no arranca. La clave del host se genera una vez en la carpeta de estado. Está hecho con [Wish](https://github.com/charmbracelet/wish).

## Configuración

`~/.config/atalaya/config.toml` (respeta `$XDG_CONFIG_HOME`; otra ruta con `--config`):

```toml
editor = "code"                        # para la tecla o; por defecto $VISUAL, $EDITOR o el primero de cursor/code/zed/subl

[[project]]
name = "odomto"
path = "~/dev/odomto"
github = "otuletta/odomto"             # opcional
health = ["https://api.odomto.com/health"]   # opcional

[[project]]
name = "kdx"
path = "~/dev/kdx"

[agents]
patterns = ["claude", "cline"]         # globs sobre el nombre del ejecutable; distinguen mayúsculas

[github]
bot_authors = ["dependabot", "renovate", "github-actions"]  # sus PRs van en una sola fila; las cuentas "…[bot]" siempre cuentan

[ai]                                   # teclas a y c
command = ["claude"]                   # la CLI de Claude Code (reutiliza su sesión, sin API keys)

[[plugin]]                             # una fuente propia (ver examples/plugins)
name = "npm-audit"
command = ["python3", "~/atalaya/examples/plugins/npm-audit.py"]
interval = "30m"

[notify]                               # notificaciones del sistema (macOS; notify-send en Linux)
enabled = true
ci = true                              # un CI se rompe o se arregla
health = true                          # un endpoint se cae o se recupera
agents = true                          # un agente termina su turno o se atasca en un permiso
reviews = true                         # alguien te pide una review

[ports]
ignore = ["rapportd", "ControlCenter"] # procesos a ocultar (por defecto, demonios de macOS)
show_ephemeral = false                 # puertos ≥ 49152 que no son de ningún proyecto

[intervals]                            # opcional; estos son los valores por defecto
git = "5s"
fetch = "10m"
github = "60s"
health = "30s"
ports = "3s"
agents = "5s"
docker = "10s"
ollama = "10s"
```

Lo que Atalaya aprende vive aparte, en `~/.local/state/atalaya/` (o `$XDG_STATE_HOME/atalaya`): `events.jsonl` (la actividad) y `health.jsonl` (una semana de health checks).

La validación informa de **todos** los errores a la vez, incluidas claves mal escritas (`helth = …`).

## Arquitectura

```
               ┌──────────────── Bubble Tea v2 (UI) ─────────────┐
               │  Model ← Update(msg) → View                     │
               └───────────────▲─────────────────────────────────┘
                               │ store.State (copia inmutable)
                     ┌─────────┴─────────┐
                     │   Store            │  último dato bueno por fuente,
                     │   (caché + señal)  │  error, próximo intento
                     └─────────▲─────────┘
                     ┌─────────┴─────────┐
                     │   Scheduler        │  una goroutine y un context por
                     │                    │  collector, backoff exponencial
                     └─────────▲─────────┘
   ┌────────┬────────┬─────────┼─────────┬─────────┬────────┬────────┐
  Git     Fetch   GitHub    Health     Ports    Docker   Ollama   Agents
  5s      10m     60s       30s        3s       10s      10s      5s
```

- **Interfaz `Collector`** (`internal/collector`): `Name()`, `Interval()` y `Collect(ctx) (Snapshot, error)`. Añadir una fuente (Vercel, Sentry, Railway) es implementar esos tres métodos y registrarla en `cmd/atalaya`. Ni el scheduler ni el store cambian.
- **…o sin escribir Go: plugins.** Un `[[plugin]]` es un collector más que ejecuta un comando, le pasa tus proyectos en `$ATALAYA_PROJECTS` y lee una tabla en JSON. La UI la pinta con su panel genérico. El protocolo está en [`examples/plugins`](examples/plugins/README.md).
- **Concurrencia.** Cada collector corre en su goroutine con su timeout. Si GitHub se cae o un collector entra en pánico, el resto sigue: ese panel conserva el último dato bueno y muestra el error y la hora del reintento.
- **Store con señal coalescente.** Los collectors nunca se bloquean esperando a la UI. La UI recibe una señal, lee una copia completa del estado y no puede perderse una actualización.
- **Git sin efectos secundarios.** `git status --porcelain=v2 -z` con `GIT_OPTIONAL_LOCKS=0`: consultar cada 5 s nunca toma `index.lock` ni choca con tus comandos. El parser propio (`internal/gitparse`) se prueba con fixtures generados desde repos reales en cada estado (renombrado, conflicto, detached, upstream borrado…) y con fuzzing.
- **El fetch es una fuente aparte.** Así adelante/atrás refleja el remoto y no solo tu último `git fetch`, sin frenar el sondeo de 5 s.
- **GitHub en una sola query GraphQL**, con un alias por repo (`r0: repository(…)`, `r1: …`) y otro por rama local (`b0: repository(…){ ref(qualifiedName: "refs/heads/…") }`), construida en tiempo de ejecución con `reflect.StructOf`, porque `githubv4` deriva la query de un tipo y el nombre de la rama va en el tag. Cuesta 1 punto de rate limit por minuto. GraphQL no admite peticiones condicionales (ETag), así que el presupuesto se protege leyendo el campo `rateLimit` de la propia respuesta y las cabeceras `Retry-After` y `X-RateLimit-Reset`: el scheduler espera hasta el reset en lugar de insistir.
- **Un store, muchas pantallas.** El store admite varios suscriptores, cada uno con su propia señal: la TUI local, el detector de eventos y cada sesión SSH leen el mismo estado sin robarse avisos, y al desconectar la sesión se da de baja.
- **Cada cambio es un evento.** El detector compara cada estado con el anterior y produce eventos: van al log de actividad (siempre) y a una notificación (si `[notify]` lo pide). El primer estado visto es la línea base, así que arrancar nunca «descubre» lo que ya era así.
- **Las notificaciones son un lector más del store** (`internal/notify`): se suscriben como la UI, comparan cada estado con el anterior y solo avisan de transiciones. En macOS el texto viaja a `osascript` como argumento, nunca dentro del código AppleScript, así que unas comillas en el título de un PR no pueden romperlo.
- **Agentes por su transcripción.** Claude Code guarda cada sesión en `~/.claude/projects/<carpeta>/<id>.jsonl`. Atalaya empareja cada proceso con la suya (por `--resume=<id>` o la más reciente de su carpeta), lee solo los últimos 512 KB, cachea por tamaño y fecha, y deduce el estado del último mensaje: turno terminado → te espera; herramienta pendiente sin actividad ni CPU durante 30 s → probablemente un permiso. El formato es interno de Claude Code, así que todo vive en `collector/sessions.go` y, si cambia, el panel simplemente no muestra sesión.
- **Menos sondeo sin foco.** La TUI pide a la terminal los eventos de foco. Mientras está en segundo plano, git, puertos, agentes, Docker y Ollama se consultan 5 veces menos; GitHub y health mantienen el ritmo porque alimentan las notificaciones. Al volver, todo se refresca al instante.
- **CI honesto.** Se toma el último run de *cada* workflow y se muestra el más grave, así un "Dependabot Updates" verde no tapa un CI roto. Los runs programados antiguos sobre el mismo commit se ignoran.

## Desarrollo

```bash
go test -race ./...                 # todo, con detector de carreras
golangci-lint run                   # lint (config en .golangci.yml)
go test ./internal/ui -update       # regenera los golden files tras un cambio visual intencionado
./internal/gitparse/testdata/gen.sh # regenera los fixtures de git desde repos reales
go test -fuzz FuzzParse ./internal/gitparse
ATALAYA_LIVE=owner/repo go test -run TestGitHubLive -v ./internal/collector   # query real contra GitHub
ATALAYA_NOTIFY_TEST=1 go test -run TestRealNotification ./internal/notify     # muestra una notificación real
```

Los tests de la UI usan [teatest](https://github.com/charmbracelet/x/tree/main/exp/teatest): manejan el programa con teclas reales y comparan cada panel con un golden file (`internal/ui/testdata`).

| Paquete | Qué hace |
|---|---|
| `cmd/atalaya` | `main`, flags y subcomandos (`init`, `status`) |
| `internal/collector` | La interfaz y las fuentes: git, fetch, github, health, ports, docker, ollama, agents |
| `internal/gitparse` | Parser de `git status --porcelain=v2 -z` |
| `internal/scheduler` | Intervalos, timeouts, backoff y refresco manual |
| `internal/store` | Estado compartido y caché del último dato bueno |
| `internal/config` | Carga, validación y `init` |
| `internal/ui` | Modelo Bubble Tea, paneles, detalle y estilos |
| `internal/actions` | Lo que hacen `o`, `p` y `x` |
| `internal/notify` | Detector de cambios: eventos y notificaciones |
| `internal/events` | El log de actividad (JSON lines, compactado) |
| `internal/ai` | Diagnóstico y arreglo de CI con Claude Code |
| `internal/demo` | Fuentes simuladas para `--demo` y para el GIF |

### GIF y release

```bash
vhs demo.tape                       # graba demo.gif con datos simulados y deterministas
git tag v0.1.0 && git push --tags   # el workflow release ejecuta GoReleaser
```

El workflow de release publica binarios para macOS y Linux (amd64 y arm64) y actualiza el cask en `otuletta/homebrew-tap`. Necesita el secreto `HOMEBREW_TAP_GITHUB_TOKEN`: un token con permiso de escritura sobre ese repo.

## Limitaciones conocidas

- **Primero macOS.** Linux funciona (los tests de CI corren en los dos). Windows queda fuera de alcance.
- **La detección de agentes es por nombre de proceso.** Es frágil por naturaleza, por eso los patrones son configurables.
- **Los puertos se leen con `lsof` en macOS** (vía gopsutil). Solo ves procesos de tu usuario sin `sudo`.

## Licencia

[MIT](LICENSE) © 2026 Oscar Tuletta
