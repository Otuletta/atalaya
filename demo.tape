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
Sleep 3s

# Repos: yellow is uncommitted work, red is out of sync.
Down
Sleep 800ms
Enter
Sleep 2.5s
Escape
Sleep 800ms

# GitHub: CI, PRs and review requests from a single GraphQL query.
Type "2"
Sleep 3.5s

# Production: status, latency sparkline and TLS days left.
Type "3"
Sleep 5s

# Local: ports mapped to process and project, Docker and Ollama.
Type "4"
Sleep 3s

# Agents running on each repo.
Type "5"
Sleep 2.5s

# Filter every panel by project.
Type "1"
Sleep 600ms
Type "/"
Sleep 300ms
Type "kdx"
Sleep 1s
Enter
Sleep 1.5s
Escape
Sleep 600ms

Type "?"
Sleep 3s
Type "?"
Sleep 500ms
Type "q"
Sleep 500ms
