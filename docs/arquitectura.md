# Cómo está hecho Atalaya

Atalaya es un panel de terminal que vigila a la vez tus repos, su CI en GitHub, tus endpoints de producción, los puertos de tu máquina y los agentes de código que trabajan en ella. Este texto cuenta las decisiones que hay detrás: qué problema resuelve cada pieza, qué alternativas se descartaron y qué se midió para decidir.

## La forma general

```
   TUI local     sesiones SSH     detector               status.json
   (Bubble Tea)  (Wish, lectura)  (eventos, avisos)      (prompt, SwiftBar)
        └──────────────┴──────────────┬──────────────────────┘
                                      │ cada uno su suscripción
                            ┌─────────┴─────────┐
                            │   Store            │
                            └─────────▲─────────┘
                            ┌─────────┴─────────┐
                            │   Scheduler        │
                            └─────────▲─────────┘
   Git · Fetch · GitHub · Health · Ports · Docker · Ollama · Agents · Usage · Plugins
```

Tres ideas sostienen todo lo demás:

1. **Las fuentes no saben nada de la pantalla.** Cada fuente es un `Collector` con tres métodos: `Name()`, `Interval()` y `Collect(ctx) (Snapshot, error)`. Devuelve un valor inmutable y termina. No pinta, no avisa, no guarda.
2. **El store es la única verdad.** Guarda, por fuente, el último dato bueno, el último error y cuándo será el próximo intento. Quien quiera enterarse de los cambios se suscribe.
3. **La interfaz es una vista más.** La TUI, `atalaya status --json`, el detector de eventos, cada sesión SSH y `status.json` leen el mismo estado. Ninguno es especial.

## Scheduler: una goroutine por fuente

Cada collector corre en su propia goroutine, con su intervalo y un timeout derivado de él. Esto aísla los fallos: si GitHub tarda 30 segundos, git sigue refrescándose cada 5; si una fuente entra en pánico, el scheduler lo convierte en un error de esa fuente y el resto sigue.

Los errores reintentan con backoff exponencial y un poco de jitter, para que varias fuentes caídas no se sincronicen. Una fuente puede pedir una espera concreta devolviendo un `RetryAfterError` (GitHub lo hace con `Retry-After` y `X-RateLimit-Reset`): el scheduler espera hasta ese momento en vez de insistir.

`Refresh(nombres...)` despierta a una fuente antes de tiempo sin bloquear (un canal con hueco para un aviso; si ya hay uno pendiente, se descarta). `SlowDown(factor, nombres...)` multiplica intervalos: la TUI lo usa cuando la terminal pierde el foco.

## Store: una señal que no se pierde ni bloquea

El store podría haber enviado cada cambio por un canal. Pero un lector lento (la UI mientras dibuja) bloquearía a los collectors, o habría que descartar mensajes. La solución es una **señal coalescente**: un canal con capacidad uno por suscriptor. Al cambiar algo, el store intenta dejar una señal; si ya hay una, no hace nada. El lector, al despertar, pide una copia completa del estado. Nunca se pierde un cambio (siempre lee el último estado) y nadie se bloquea.

Las copias son baratas: el mapa de entradas se clona, y las instantáneas dentro se comparten porque nadie las modifica.

## Git sin tocar tus repos

Consultar `git status` cada 5 segundos parece inocente, pero por defecto git refresca el índice y para eso toma `index.lock`. Si justo entonces haces `git commit`, falla con «Another git process seems to be running». Por eso todas las llamadas llevan `GIT_OPTIONAL_LOCKS=0`: git lee el índice sin escribirlo.

La salida se lee con `--porcelain=v2 -z`, un formato estable pensado para máquinas. El parser (`internal/gitparse`) se prueba con fixtures generados desde repos reales en cada estado raro (renombrados, conflictos, HEAD separado, upstream borrado) y con fuzzing.

`fetch` es una fuente aparte, cada 10 minutos: así «adelante/atrás» refleja el remoto real sin frenar el sondeo local.

## GitHub en una sola consulta

Pedir el estado de 10 repos por REST serían decenas de peticiones por minuto. Con GraphQL es una: cada repo es un alias (`r0: repository(...)`, `r1: ...`) y cada rama local otro (`b0: repository(...) { ref(qualifiedName: "refs/heads/...") }`). La librería `githubv4` deriva la consulta de un tipo Go con tags, y los alias dependen de tu config, así que el tipo se construye en tiempo de ejecución con `reflect.StructOf`. Cuesta un punto de rate limit por minuto.

El token se toma de `gh auth token`: Atalaya no pide ni guarda credenciales.

## Agentes: el proceso y su transcripción

Detectar un agente es fácil: un proceso llamado `claude`. Saber qué hace no lo es. Claude Code guarda cada sesión en `~/.claude/projects/<carpeta>/<id>.jsonl`, y Atalaya empareja cada proceso con la suya: la que nombra su `--resume`, o la más reciente de su carpeta que ningún otro proceso reclamó.

Del final de la transcripción (solo los últimos 512 KB, cacheados por tamaño y fecha) sale el estado: si el último mensaje terminó el turno, el agente **te espera**; si pidió una herramienta y ni la transcripción ni la CPU se mueven en 30 segundos, probablemente está **atascado en un permiso**. El formato es interno de Claude Code, así que todo lo que depende de él vive en un archivo, y si cambia el panel deja de mostrar la sesión sin romper nada más.

El coste de los agentes sale de las mismas transcripciones: cada respuesta trae su `usage`. Pueden sumar gigabytes, así que se leen de forma incremental (solo los bytes nuevos, con el progreso guardado en disco) y sin decodificar el JSON entero: se buscan las claves que importan. La primera lectura de 1,7 GB tarda un segundo; las siguientes, menos de un milisegundo.

## Eventos: comparar dos estados

El detector es un lector más del store. Compara cada estado con el anterior y emite eventos: un CI que pasa de verde a rojo, un endpoint que falla dos veces seguidas, una review nueva, un agente que termina su turno. El primer estado de cada fuente es la línea base, así que arrancar Atalaya nunca «descubre» lo que ya estaba roto.

Los eventos van siempre al log de actividad (JSON lines, compactado al crecer) y, si la config lo pide, a una notificación del sistema. En macOS el texto viaja a `osascript` como argumento, nunca dentro del código AppleScript, para que unas comillas en el título de un PR no puedan inyectar nada.

## Recargar la config sin reiniciar

Todo lo que depende de la config (las fuentes, el detector, `status.json`) lo posee un «motor». Un stat del archivo cada 2 segundos detecta cambios; si la config nueva es válida y distinta, el motor detiene la generación anterior, espera a que termine y arranca la nueva **sobre el mismo store**: lo ya recogido sigue en pantalla mientras llega lo nuevo. Las fuentes que desaparecen (un plugin quitado) se borran del store. Una config con errores no se aplica: la UI lo dice y sigue la anterior.

## La interfaz

La TUI es un modelo de [Bubble Tea v2](https://github.com/charmbracelet/bubbletea). Lo que la hace agradable son detalles pequeños:

- **Columnas que se adaptan.** Cada tabla declara sus columnas con ancho mínimo, máximo, cuáles pueden flexionar y en qué orden se sacrifican. A 80 columnas desaparecen primero las menos útiles.
- **Una paleta** (`:`) con búsqueda aproximada sobre todas las filas de todos los paneles, que no distingue tildes y premia las palabras enteras y los inicios de palabra.
- **Capas.** La paleta, el diálogo de confirmación y los avisos se dibujan encima del panel con el compositor de Lip Gloss v2; los modales atenúan lo de debajo.
- **El ratón** selecciona pestañas, tarjetas, filas y botones. Las zonas de clic se calculan con las mismas funciones que dibujan, así que no pueden desalinearse.
- **La terminal moderna**: enlaces OSC 8, el título de la pestaña con el estado, la barra de progreso nativa al cargar y mientras corre una acción.

Las pruebas manejan el programa de verdad con [teatest](https://github.com/charmbracelet/x/tree/main/exp/teatest): pulsan teclas, envían clics y comparan cada pantalla con un golden file a 120×30 y a 80×24. Un test recorre todos los paneles a anchos de 60 a 160 columnas y falla si una línea se sale o si se cortan las pestañas o las teclas.

## Cuánto cuesta vigilar

Un panel que vive abierto todo el día tiene que costar poco. Medirlo cambió varias ideas previas.

**Lo caro no es lo que hace git, sino arrancarlo.** Cada proceso `git` cuesta unos 8 ms de CPU solo en arrancar, y un `git status` completo, entre 10 y 23 ms. Atalaya lanzaba tres procesos por repo cada 5 segundos (`status`, `worktree list` y `log -1`). Ahora:

- el último commit solo se vuelve a leer cuando cambia el HEAD (que `git status` ya informa);
- la lista de worktrees, cuando cambia la carpeta `worktrees` de git;
- un repo sin cambios durante 2 minutos se lee cada 15 segundos en vez de cada 5, **salvo que** cambien los archivos que git escribe al moverse (el índice, HEAD y su log, FETCH_HEAD, el stash): un commit, un checkout, un `git add` o un pull lo despiertan al instante.

**Listar procesos uno a uno es caro.** Para encontrar agentes, gopsutil pedía el nombre de cada proceso por separado: 44 ms para 1 200 procesos. Una sola llamada `sysctl(kern.proc.all)` devuelve todos en 0,2 ms. Y la rama de cada agente se cacheaba como un `git rev-parse` por agente cada 5 segundos; ahora se vuelve a leer solo cuando cambia el HEAD de su repo.

**No todo lo prometedor funciona.** `netstat -anv` da los puertos en escucha con su PID gastando una décima parte que `lsof`, pero en macOS reciente, lanzado desde un binario Go, devuelve la tabla vacía. Se descartó: un panel que a veces dice «no hay puertos» es peor que uno que cuesta un poco más.

Resultado, con 5 repos y 7 sesiones de Claude Code abiertas, en régimen estable: unos **4,6 s de CPU por minuto** (≈8 % de un núcleo, contando los `git` y `lsof` que lanza), frente a 7,2 s antes, un 36 % menos, y baja más cuando los repos llevan un rato quietos. El propio proceso de Atalaya (dibujar, el store, `status.json`) gasta 1,4 s por minuto; el resto son los comandos que lanza, sobre todo `git status` y `lsof`.

Para medirlo en tu máquina:

```bash
go test -tags measure -run TestMeasure -v ./internal/collector   # CPU por fuente con tu config
ATALAYA_CPUPROFILE=cpu.prof atalaya && go tool pprof -top cpu.prof   # el propio proceso
```
