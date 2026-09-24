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
Set TypingSpeed 45ms
Set Framerate 24

Hide
Type `export PS1='$ ' && rm -rf "$VAULTR_CACHE_DIR" && clear`
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

Type "vaultr get secret/prod/db/orders password"
Sleep 300ms
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
