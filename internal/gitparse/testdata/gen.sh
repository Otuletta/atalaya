#!/usr/bin/env bash
# Regenerates the porcelain v2 fixtures from real repositories.
#
#   ./internal/gitparse/testdata/gen.sh
#
# Each fixture is the raw output of `git status --porcelain=v2 --branch
# --show-stash -z` for a repository in a known state. Dates and identities are
# fixed so commit ids are stable across runs.
set -euo pipefail

OUT="$(cd "$(dirname "$0")" && pwd)"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

export GIT_AUTHOR_NAME="Ada" GIT_AUTHOR_EMAIL="ada@example.com"
export GIT_COMMITTER_NAME="Ada" GIT_COMMITTER_EMAIL="ada@example.com"
export GIT_AUTHOR_DATE="2026-01-01T12:00:00Z" GIT_COMMITTER_DATE="2026-01-01T12:00:00Z"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1

g() { git -c init.defaultBranch=main -c advice.detachedHead=false "$@"; }
snap() { (cd "$1" && g status --porcelain=v2 --branch --show-stash -z) >"$OUT/$2.z"; }
commit() { echo "$2" >"$1/$3"; g -C "$1" add "$3"; g -C "$1" commit -qm "$4"; }

# A bare remote with two commits, and a clone tracking it.
g init -q --bare "$WORK/remote.git"
g clone -q "$WORK/remote.git" "$WORK/seed" 2>/dev/null
commit "$WORK/seed" one a.txt "first"
commit "$WORK/seed" two b.txt "second"
g -C "$WORK/seed" push -q origin main

clone() { g clone -q "$WORK/remote.git" "$WORK/$1"; }

# clean: up to date with upstream.
clone clean
snap "$WORK/clean" clean

# ahead_behind: two local commits, one new remote commit.
clone ab
commit "$WORK/seed" three c.txt "third"
g -C "$WORK/seed" push -q origin main
commit "$WORK/ab" x x.txt "local 1"
commit "$WORK/ab" y y.txt "local 2"
g -C "$WORK/ab" fetch -q
snap "$WORK/ab" ahead_behind

# dirty: every kind of change, including awkward file names.
clone dirty
cd "$WORK/dirty"
echo staged >>a.txt && g add a.txt                      # M.
echo both >>b.txt && g add b.txt && echo more >>b.txt   # MM
echo wt >>c.txt                                         # .M
echo new >"new file.txt" && g add "new file.txt"        # A.
printf 'x' >"$(printf 'line\nbreak.txt')"                # ? with newline
echo u >"ñandú 🦤.txt"                                   # ? with unicode
mkdir -p dir && echo u >dir/untracked.txt               # ?
cd - >/dev/null
snap "$WORK/dirty" dirty

# deleted: one staged deletion, one worktree deletion.
clone deleted
g -C "$WORK/deleted" rm -q a.txt
rm "$WORK/deleted/b.txt"
snap "$WORK/deleted" deleted

# rename: a staged rename to a path with spaces.
clone rename
g -C "$WORK/rename" mv a.txt "renamed a.txt"
snap "$WORK/rename" rename

# conflict: a merge with conflicts in progress.
clone conflict
cd "$WORK/conflict"
g checkout -qb feature
echo feature >a.txt && g commit -qam "feature"
g checkout -q main
echo main >a.txt && g commit -qam "main"
g merge -q feature >/dev/null 2>&1 || true
cd - >/dev/null
snap "$WORK/conflict" conflict

# initial: no commits yet.
g init -q "$WORK/initial"
echo hi >"$WORK/initial/README.md"
snap "$WORK/initial" initial

# detached: HEAD on a commit, not a branch.
clone detached
g -C "$WORK/detached" checkout -q HEAD~1
snap "$WORK/detached" detached

# stash: two stash entries and a clean tree.
clone stash
echo s1 >>"$WORK/stash/a.txt" && g -C "$WORK/stash" stash -q
echo s2 >>"$WORK/stash/b.txt" && g -C "$WORK/stash" stash -q
snap "$WORK/stash" stash

# no_upstream: a local branch that was never pushed.
clone noup
g -C "$WORK/noup" checkout -qb wip
snap "$WORK/noup" no_upstream

# upstream_gone: the remote branch was deleted.
clone gone
g -C "$WORK/seed" push -q origin main:topic
g -C "$WORK/gone" fetch -q
g -C "$WORK/gone" checkout -q -b topic --track origin/topic
g -C "$WORK/seed" push -q origin :topic
g -C "$WORK/gone" fetch -q --prune
snap "$WORK/gone" upstream_gone

echo "fixtures written to $OUT"
