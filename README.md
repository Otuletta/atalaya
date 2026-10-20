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
| **3 Producción** | Health checks HTTP: código, latencia, sparkline de los últimos minutos y **días restantes del certificado TLS**. |
| **4 Local** | Puertos en escucha mapeados a proceso y proyecto (`:3000 → node · odomto`), contenedores Docker y modelos cargados en Ollama. |
| **5 Agentes** | Sesiones de Claude Code o Cline corriendo: en qué repo y desde hace cuánto. |

**Detalles de terminal moderna.** Los números de PR, los runs de CI, los endpoints y los puertos son enlaces clicables (OSC 8, cmd+clic en iTerm2, Ghostty, WezTerm…). La pestaña de la terminal muestra el estado (`atalaya ✗2 CI · ✗1 prod`) aunque esté en segundo plano, y mientras carga o refresca aparece la barra de progreso nativa de la terminal.

**Notificaciones.** Con Atalaya abierto aunque esté en segundo plano, recibes una notificación del sistema cuando un CI se rompe o se arregla (en la rama por defecto o en la tuya) y cuando un endpoint se cae o se recupera. Solo se avisan los *cambios*: nada al arrancar por lo que ya estaba roto, nada repetido, y un endpoint no cuenta como caído hasta fallar dos veces seguidas.

## Instalación

```bash
brew install otuletta/tap/atalaya
```

O con Go 1.27+:

```bash
go install github.com/otuletta/atalaya/cmd/atalaya@latest
```

Requisitos: `git`. Para el panel de GitHub, la [CLI `gh`](https://cli.github.com) con sesión iniciada (`gh auth login`); Atalaya reutiliza su token y no pide ni guarda credenciales. Docker y Ollama son opcionales: si no están, su sección lo indica.

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
| `tab` / `shift+tab` / `0-5` | Cambiar de panel |
| `j` `k` o flechas, `g` `G`, `ctrl+u` `ctrl+d` | Navegar |
| `/` | Filtrar proyectos (`esc` limpia el filtro) |
| `enter` | Detalle del item: archivos cambiados, PRs, historial de latencia, comando del proceso… |
| `o` | Abrir: el repo en tu editor, el PR o el run fallido en el navegador, `localhost:<puerto>` |
| `p` | `git pull --ff-only` del repo seleccionado (pide confirmación) |
| `x` | Matar el proceso de un puerto con `SIGTERM` (pide confirmación) |
| `r` | Refrescar todas las fuentes ya |
| `?` | Ayuda |
| `q` | Salir |

Las acciones destructivas siempre piden confirmación. `p` usa `--ff-only`, así que nunca crea un merge ni deja conflictos. `x` comprueba que el PID sigue siendo el mismo proceso que viste, para no matar uno reciclado.

## Modo script

```bash
atalaya status            # todos los paneles como texto, una vez
atalaya status --json     # lo mismo en JSON, para scripts
atalaya status --fetch    # hace git fetch antes de calcular adelante/atrás
```

```bash
# ¿Qué repos tienen el CI roto?
atalaya status --json | jq -r '.state.sources.github.data.repos[] | select(.ci.state == "failure") | .project'
```

La TUI y `--json` leen el mismo store; la UI solo es una vista más de los datos.

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

[notify]                               # notificaciones del sistema (macOS; notify-send en Linux)
enabled = true
ci = true                              # un CI se rompe o se arregla
health = true                          # un endpoint se cae o se recupera

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
- **Concurrencia.** Cada collector corre en su goroutine con su timeout. Si GitHub se cae o un collector entra en pánico, el resto sigue: ese panel conserva el último dato bueno y muestra el error y la hora del reintento.
- **Store con señal coalescente.** Los collectors nunca se bloquean esperando a la UI. La UI recibe una señal, lee una copia completa del estado y no puede perderse una actualización.
- **Git sin efectos secundarios.** `git status --porcelain=v2 -z` con `GIT_OPTIONAL_LOCKS=0`: consultar cada 5 s nunca toma `index.lock` ni choca con tus comandos. El parser propio (`internal/gitparse`) se prueba con fixtures generados desde repos reales en cada estado (renombrado, conflicto, detached, upstream borrado…) y con fuzzing.
- **El fetch es una fuente aparte.** Así adelante/atrás refleja el remoto y no solo tu último `git fetch`, sin frenar el sondeo de 5 s.
- **GitHub en una sola query GraphQL**, con un alias por repo (`r0: repository(…)`, `r1: …`) y otro por rama local (`b0: repository(…){ ref(qualifiedName: "refs/heads/…") }`), construida en tiempo de ejecución con `reflect.StructOf`, porque `githubv4` deriva la query de un tipo y el nombre de la rama va en el tag. Cuesta 1 punto de rate limit por minuto. GraphQL no admite peticiones condicionales (ETag), así que el presupuesto se protege leyendo el campo `rateLimit` de la propia respuesta y las cabeceras `Retry-After` y `X-RateLimit-Reset`: el scheduler espera hasta el reset en lugar de insistir.
- **Las notificaciones son un lector más del store** (`internal/notify`): se suscriben como la UI, comparan cada estado con el anterior y solo avisan de transiciones. En macOS el texto viaja a `osascript` como argumento, nunca dentro del código AppleScript, así que unas comillas en el título de un PR no pueden romperlo.
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
| `internal/notify` | Notificaciones de transiciones (CI y producción) |
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
