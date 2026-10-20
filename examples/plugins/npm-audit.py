#!/usr/bin/env python3
"""Atalaya plugin: `npm audit` of every project with a package-lock.json.

A failing `npm audit` step is a common reason for a red CI: this shows it
before you push. It hits the npm registry, so run it every half hour:

    [[plugin]]
    name = "npm-audit"
    command = ["python3", "~/ruta/a/atalaya/examples/plugins/npm-audit.py"]
    interval = "30m"
"""
import json
import os
import shutil
import subprocess
from pathlib import Path

LEVELS = ["critical", "high", "moderate", "low"]
NAMES = {"critical": "crítica", "high": "alta", "moderate": "moderada", "low": "baja"}


def audit(path):
    # npm audit exits with 1 when it finds vulnerabilities: not an error.
    p = subprocess.run(["npm", "audit", "--json"], cwd=path, capture_output=True, text=True, timeout=120)
    return json.loads(p.stdout or "{}").get("metadata", {}).get("vulnerabilities", {})


def main():
    columns = ["PROYECTO", "TOTAL", "SEVERIDAD"]
    if not shutil.which("npm"):
        print(json.dumps({"title": "npm audit", "summary": "npm no está instalado", "columns": columns, "rows": []}))
        return
    rows = []
    for pr in json.loads(os.environ.get("ATALAYA_PROJECTS", "[]")):
        path = pr.get("path")
        if not path or not (Path(path) / "package-lock.json").exists():
            continue
        row = {"project": pr["name"]}
        if pr.get("github"):
            row["url"] = f"https://github.com/{pr['github']}/security/dependabot"
        try:
            v = audit(path)
        except Exception as e:  # a broken project must not hide the others
            rows.append({**row, "cells": [pr["name"], "?", str(e)[:80]], "status": "warn"})
            continue
        total = v.get("total", sum(v.get(level, 0) for level in LEVELS))
        found = [f"{v[level]} {NAMES[level]}" for level in LEVELS if v.get(level)]
        status = "error" if v.get("critical") or v.get("high") else "warn" if total else "ok"
        rows.append({**row, "cells": [pr["name"], str(total), ", ".join(found) or "ninguna"], "status": status,
                     "detail": "npm audit fix arregla lo que no rompe dependencias"})
    bad = sum(r["status"] == "error" for r in rows)
    projects = f"{len(rows)} proyecto" + ("" if len(rows) == 1 else "s")
    summary = f"{projects} · {bad} con vulnerabilidades altas o críticas" if rows else "ningún proyecto con package-lock.json"
    print(json.dumps({"title": "npm audit", "summary": summary, "columns": columns, "rows": rows}, ensure_ascii=False))


main()
