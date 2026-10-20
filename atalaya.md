# Atalaya: torre de control para tus proyectos en la terminal

## La idea

Un solo comando, `atalaya`, abre en la terminal un panel con el estado de todos tus proyectos en tiempo real. Sustituye el ritual de abrir GitHub, revisar CI, verificar que producción responde, buscar qué proceso ocupa el puerto 3000 y recordar en qué rama quedó cada repo.

El objetivo es que sea algo que uses de verdad todos los días, no un demo.

## Qué muestra

**1. Repos.** De cada proyecto registrado: rama actual, commits adelante o atrás del remoto, archivos modificados, stash y último commit (autor, mensaje y hace cuánto). Marca en amarillo lo que tiene trabajo sin commitear y en rojo lo que está desincronizado.

**2. GitHub.** Por repo: el último run de CI (✓ ✗ ⟳ con duración), PRs abiertos, PRs donde te piden review y checks fallidos.

**3. Producción.** Health checks HTTP a tus endpoints: código de estado, latencia, un sparkline con la latencia de los últimos minutos y **días restantes del certificado TLS**.

**4. Local.** Los puertos en escucha, mapeados al proceso y al repo que los abrió (`:3000 → node · odomto`), contenedores Docker activos y los modelos cargados en Ollama.

**5. Agentes.** Las sesiones de Claude Code o Cline que están corriendo, en qué repo y desde hace cuánto.

## Interacción

| Tecla | Acción |
|---|---|
| `tab` / `1-5` | Cambiar de panel |
| `j/k` o flechas | Navegar |
| `/` | Filtrar proyectos |
| `enter` | Detalle del item |
| `o` | Abrir repo en el editor / PR en el navegador |
| `p` | `git pull` del repo seleccionado |
| `x` | Matar el proceso de un puerto (con confirmación) |
| `r` | Refrescar todo |
| `?` | Ayuda |

Además trae un modo sin interfaz: `atalaya status --json` imprime todo como JSON para scripts. Eso demuestra que la arquitectura separa los datos de la UI.

## Stack

| Pieza | Elección | Por qué |
|---|---|---|
| Lenguaje | **Go 1.27** | Es la versión estable actual. Produce un binario único, con concurrencia nativa y arranque instantáneo. |
| TUI | **Bubble Tea v2**, Bubbles v2 y Lip Gloss v2 | Es el estándar de facto para TUIs en Go. Usa la arquitectura Elm (Model → Update → View). |
| Git | `git status --porcelain=v2 --branch` + parser propio | Es más rápido y fiable que go-git en repos grandes, y el parser propio se puede testear. |
| GitHub | **API GraphQL** (`githubv4`) | Una sola query trae PRs y checks de todos tus repos. Con REST serían decenas de requests. |
| Auth | Reutiliza `gh auth token` | No hay que pedir ni guardar tokens nuevos. |
| Procesos y puertos | `gopsutil` | Funciona en macOS y Linux. |
| Config | TOML en `~/.config/atalaya/config.toml` | Es legible y fácil de editar a mano. |
| Tests | `testing` + **teatest** (golden files de la UI) | Permite probar la interfaz, no solo la lógica. |
| Calidad | `golangci-lint`, `go test -race` | Detecta race conditions, algo clave con muchas goroutines. |
| Release | **GoReleaser** + Homebrew tap | Se instala con `brew install otuletta/tap/atalaya`. |
| Demo | **VHS** de Charm | Graba el GIF del README desde un script, reproducible. |

## Arquitectura

```
               ┌──────────────── Bubble Tea (UI) ────────────────┐
               │  Model ← Update(msg) → View                     │
               └───────────────▲─────────────────────────────────┘
                               │ msgs tipados (RepoUpdated, CIUpdated…)
                     ┌─────────┴─────────┐
                     │   Scheduler        │  intervalos por collector,
                     │   + caché + bus    │  backoff ante errores
                     └─────────▲─────────┘
       ┌──────────┬────────────┼────────────┬───────────┐
   GitCollector  GitHubCollector  HealthCollector  PortsCollector  AgentsCollector
   (cada 5s)     (cada 60s, ETag)  (cada 30s)      (cada 3s)       (cada 5s)
```

- **Interfaz `Collector`.** Cada fuente implementa `Collect(ctx) (Snapshot, error)`. Añadir una fuente nueva (Vercel, Sentry, Railway) no toca la UI.
- **Concurrencia.** Cada collector corre en su propia goroutine con su propio `context`. Si GitHub está caído, el resto sigue funcionando y ese panel muestra el error.
- **Rate limits.** Requests condicionales con ETag y caché, y backoff exponencial ante errores.
- **Estado separado de la UI.** El mismo store alimenta la TUI y el modo `--json`.

## Configuración de ejemplo

```toml
[[project]]
name = "odomto"
path = "~/dev/odomto"
github = "otuletta/odomto"
health = ["https://api.odomto.com/health"]

[[project]]
name = "kdx"
path = "~/dev/kdx"

[agents]
patterns = ["claude", "cline"]
```

Además, `atalaya init` escanea una carpeta, detecta los repos y genera el archivo solo.

## Estructura del repo

```
atalaya/
├── cmd/atalaya/          # main, flags, subcomandos (init, status)
├── internal/
│   ├── collector/        # interfaz + git, github, health, ports, agents
│   ├── gitparse/         # parser de porcelain v2 (con tests exhaustivos)
│   ├── scheduler/        # intervalos, backoff, caché
│   ├── store/            # estado compartido
│   ├── config/           # carga y validación TOML
│   └── ui/               # modelos Bubble Tea por panel + estilos
├── testdata/             # fixtures de git y golden files de la UI
├── demo.tape             # script VHS para el GIF
├── .goreleaser.yaml
└── .github/workflows/    # lint + test -race + release
```

## Alcance por fases

| Fase | Contenido |
|---|---|
| **MVP** | Config, panel de repos y panel de health checks. Ya resulta útil. |
| **v1** | GitHub (CI + PRs), puertos, acciones (`o`, `p`, `x`), `--json` e `init` |
| **v1.1** | Agentes, Docker y Ollama; release con Homebrew y GIF con VHS |
| **Extra** | Notificaciones de macOS cuando falla CI o se cae producción |

## Qué demuestra en el portafolio

- **Concurrencia real** en Go: goroutines, contexts, cancelación y manejo de race conditions.
- **Diseño extensible**: añadir una fuente es implementar una interfaz.
- **Integración con APIs**, consumidas con eficiencia: GraphQL, caché y rate limits.
- **Parsing y testing**: parser propio con fixtures y tests de UI con golden files.
- **Producto terminado**: se instala con un comando, está documentado y tiene demo grabada.

## Decisiones y riesgos

- **Primero macOS.** Linux funcionará casi gratis; Windows queda fuera de alcance.
- **La detección de agentes es frágil.** Se basa en el nombre del proceso, por eso los patrones son configurables y no van fijos en el código.
- **Las acciones son destructivas.** Matar procesos y hacer `pull` siempre pide confirmación; no hay borrados.
- **Hay que confirmar versiones al iniciar.** Bubble Tea v2 cambió varias APIs respecto a v1 (por ejemplo, `KeyMsg` pasó a `KeyPressMsg`), así que las versiones exactas se fijan al crear el proyecto.

## Referencias

- [Go Release History](https://go.dev/doc/devel/release)
- [Bubbles v2 migration notes](https://tessl.io/registry/tpscloud/charmedimsure/files/skills/charmed-bubbles/references/migration.md)
