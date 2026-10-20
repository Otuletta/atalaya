# Plugins de Atalaya

Un plugin es **cualquier ejecutable que imprima un JSON** por la salida estándar. Atalaya lo ejecuta cada `interval`, le pasa tus proyectos y muestra su tabla en el panel **7 Plugins** (y un resumen en la vista Resumen). No hace falta escribir Go ni recompilar.

```toml
[[plugin]]
name = "npm-audit"                                   # letras, dígitos, - o _
command = ["python3", "~/atalaya/examples/plugins/npm-audit.py"]
interval = "30m"                                     # por defecto 60s
```

## Entrada

La variable `ATALAYA_PROJECTS` lleva tus proyectos en JSON:

```json
[{"name": "odomto", "path": "/Users/tú/dev/odomto", "github": "tú/odomto", "health": ["https://…"]}]
```

## Salida

```json
{
  "title": "npm audit",
  "summary": "4 proyectos · 1 con vulnerabilidades altas o críticas",
  "columns": ["PROYECTO", "TOTAL", "SEVERIDAD"],
  "rows": [
    {
      "cells": ["ENDONSYS", "6", "1 crítica, 3 alta, 2 moderada"],
      "status": "error",
      "project": "ENDONSYS",
      "url": "https://github.com/Otuletta/ENDONSYS/security/dependabot",
      "detail": "npm audit fix arregla lo que no rompe dependencias"
    }
  ]
}
```

| Campo | |
|---|---|
| `title` | Título de la sección. Por defecto, el `name` del plugin. |
| `summary` | Una línea para la sección y la tarjeta del Resumen. |
| `columns` | Títulos de las columnas. |
| `rows[].cells` | Valores, en el orden de `columns`. |
| `rows[].status` | `ok` ✓, `warn` ●, `error` ✗ o `info` ·. Los `error` cuentan en la pestaña. |
| `rows[].url` | Lo que abre `o`. |
| `rows[].project` | Proyecto al que pertenece la fila (filtro `/`). |
| `rows[].detail` | Texto del detalle (`enter`). |

Si el comando falla (sale con error), Atalaya muestra la primera línea de su `stderr` y conserva la última tabla buena. Se leen como máximo 1 MB y 200 filas.

## Ejemplos

| Archivo | Muestra |
|---|---|
| [`disk.sh`](disk.sh) | Espacio libre del disco de tu `$HOME` (aviso al 80 %, error al 90 %). |
| [`brew-outdated.py`](brew-outdated.py) | Paquetes de Homebrew con versión nueva. |
| [`npm-audit.py`](npm-audit.py) | `npm audit` de cada proyecto con `package-lock.json`: lo que rompería un CI con un paso de auditoría. |
