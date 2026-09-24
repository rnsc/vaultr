# Recorded by scripts/demo.sh (see .github/workflows/demo.yml).
Output demo/tui.gif

Require vaultr
Set Shell "bash"
Set FontFamily "JetBrains Mono"
Set FontSize 15
Set Width 1040
Set Height 540
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 75ms
Set Framerate 24
# Slower than real time so it is easy to follow.
Set PlaybackSpeed 0.7

Hide
Type "export PS1='$ ' && vaultr index >/dev/null 2>&1 && vault kv put secret/teams/web/sentry dsn=https://demo@sentry.example/1 >/dev/null && clear"
Enter
Sleep 2s
Show

Type "vaultr"
Sleep 300ms
Enter
Sleep 1.2s

# Search across paths and key names.
Type "stripe"
Sleep 1s
Down
Sleep 400ms
Enter
Sleep 900ms
# Values are masked until revealed.
Type "r"
Sleep 1.8s
Escape
Sleep 400ms

# esc clears the search.
Escape
Type "k:password"
Sleep 1.8s
Escape

# A secret added after the index was built: enter refreshes the index.
Type "sentry"
Sleep 1.8s
Enter
Sleep 1.8s
Enter
Sleep 700ms
Type "r"
Sleep 1.8s
Escape
Sleep 300ms
Ctrl+C
Sleep 200ms
Ctrl+C
Sleep 500ms
