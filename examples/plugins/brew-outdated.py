#!/usr/bin/env python3
"""Atalaya plugin: Homebrew packages with a newer version available.

    [[plugin]]
    name = "brew"
    command = ["python3", "~/ruta/a/atalaya/examples/plugins/brew-outdated.py"]
    interval = "1h"
"""
import json
import shutil
import subprocess


def table(summary, rows):
    print(json.dumps({"title": "Homebrew", "summary": summary,
                      "columns": ["PAQUETE", "INSTALADO", "DISPONIBLE"], "rows": rows}, ensure_ascii=False))


def main():
    if not shutil.which("brew"):
        table("brew no está instalado", [])
        return
    out = subprocess.run(["brew", "outdated", "--json=v2"], capture_output=True, text=True, timeout=120, check=True)
    data = json.loads(out.stdout)
    rows = []
    for kind, items in (("formula", data.get("formulae", [])), ("cask", data.get("casks", []))):
        for p in items:
            name = p["name"] + (" (cask)" if kind == "cask" else "")
            rows.append({"cells": [name, ", ".join(p.get("installed_versions", [])), p.get("current_version", "")],
                         "status": "info", "url": f"https://formulae.brew.sh/{kind}/{p['name']}",
                         "detail": "brew upgrade " + p["name"]})
    table(f"{len(rows)} desactualizados · brew upgrade" if rows else "todo al día", rows)


main()
