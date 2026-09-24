# Recorded by scripts/demo.sh (see .github/workflows/demo.yml).
Output demo/cli.gif

Require vaultr
Set Shell "bash"
Set FontFamily "JetBrains Mono"
Set FontSize 15
Set Width 1040
Set Height 540
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 65ms
Set Framerate 24
# Slower than real time so it is easy to follow.
Set PlaybackSpeed 0.7

Hide
Type `export PS1='$ ' && eval "$(vaultr completion bash)" && rm -rf "$VAULTR_CACHE_DIR" && clear`
Enter
Sleep 500ms
Show

Type "vaultr find password"
Sleep 300ms
Enter
Sleep 1.8s

Type "vaultr find prod k:api_key --values"
Sleep 300ms
Enter
Sleep 1.8s

# Tab completion: paths one folder at a time, then key names.
Type "vaultr get secret/pr"
Sleep 300ms
Tab
Sleep 600ms
Type "d"
Tab
Sleep 600ms
Tab
Sleep 200ms
Tab
Sleep 1.2s
Type "o"
Tab
Sleep 600ms
Tab
Sleep 200ms
Tab
Sleep 1.2s
Type "pa"
Tab
Sleep 600ms
Enter
Sleep 1.5s

Type "vault kv put secret/prod/payments/paypal client_secret=demo-pp-42 >/dev/null"
Sleep 300ms
Enter
Sleep 800ms

Type "vaultr find paypal"
Sleep 300ms
Enter
Sleep 1.8s

Type "vaultr find -r paypal"
Sleep 300ms
Enter
Sleep 2.5s
