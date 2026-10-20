# Records the README GIF with VHS (https://github.com/charmbracelet/vhs):
#
#   vhs demo.tape
#
# It runs `atalaya --demo`, whose data is simulated and deterministic, so the
# GIF looks the same on any machine.

Output demo.gif

Set Shell "bash"
Set FontSize 15
Set Width 1400
Set Height 760
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 60ms

Hide
Type "go build -o /tmp/atalaya-demo ./cmd/atalaya && clear"
Enter
Sleep 4s
Show

Type "atalaya --demo"
Sleep 400ms
Hide
# Swap in the freshly built binary without showing the path.
Ctrl+U
Type "/tmp/atalaya-demo --demo"
Enter
Show

# 0 Resumen: one card per source, only what needs attention.
Sleep 3.5s
Right
Sleep 600ms
Right
Sleep 600ms

# 1 Repos: yellow is uncommitted work, red is out of sync.
Type "1"
Sleep 1.5s
Down
Enter
Sleep 2s
Escape

# 2 GitHub: CI of the default and the local branch, bots grouped.
Type "2"
Sleep 2.5s
# a: Claude explains the broken CI.
Type "a"
Sleep 4s
Escape
Sleep 500ms

# 3 Producción: uptime, p95, sparkline and TLS; landing goes down.
Type "3"
Sleep 3.5s

# 5 Agentes: what each Claude Code session is doing, or that it waits.
Type "5"
Sleep 3s

# 6 Actividad: the tower's memory.
Type "6"
Sleep 3s

# 7 Plugins: any program that prints a JSON table.
Type "7"
Sleep 2.5s

Type "0"
Sleep 2s
Type "?"
Sleep 2.5s
Type "?"
Sleep 400ms
Type "q"
Sleep 500ms
