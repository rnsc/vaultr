# Shell completion scripts

One file per shell. Each script is a thin adapter: all the logic (commands,
flags, secret paths, key names, the cache and Vault fallback) lives in Go,
behind the hidden `vaultr __complete` command, so every shell completes the
same way.

## The `vaultr __complete` contract

```
vaultr __complete WORD... CURRENT
```

- Arguments: the words typed after `vaultr`, then the word being completed
  (pass `""` when the cursor is after a space). Words may still carry shell
  escaping (`with\ space`, `'quoted`); `__complete` removes it.
- Output: one candidate per line, already filtered by `CURRENT`. Print them
  as they are. Your shell quotes special characters such as spaces or `#`.
- A candidate ending in `/` is a folder. **Don't add a space after it**, so
  the user can keep pressing tab to go deeper.
- It always exits 0 and never writes to stderr. No token or an unreachable
  server simply means no path candidates.
- It is fast: it reads the local index when one is valid, and otherwise
  lists only the folder being completed. Don't cache its output.

## How scripts get loaded

A script must work however the shell loads it:

- printed by `vaultr completion <name>` and evaluated or sourced at startup
  (what `vaultr completion install` sets up for bash and zsh);
- installed as a file in the shell's completion directory. Release archives
  ship the scripts in `completions/`, and Homebrew installs them from there.
  zsh then autoloads the file as the body of `_vaultr`, which is why
  `zsh.zsh` checks how it was loaded.

## Adding a shell

1. Add `shells/<name>.<ext>` here. It registers a completion function for the
   `vaultr` command that calls `vaultr __complete` as above. The existing
   scripts are short, working examples.
2. Add one entry to `Shells` in [`../scripts.go`](../scripts.go): the name
   (the base name of `$SHELL`), the file, and how `vaultr completion install`
   sets it up:
   - `RCFile` + `LoadLine`: append a line to the shell's startup file that
     loads `vaultr completion <name>` (bash, zsh).
   - or `CompletionFile`: write the script to a path under `~/.config`, for
     shells that autoload completion files (fish).
3. Run `go test ./internal/complete`. Every registered shell is checked
   automatically: the script is embedded and calls `vaultr __complete`, and
   install works and can be repeated safely. If CI can install your shell
   with `apt-get`, add it to the "Install fish and zsh" step in
   `.github/workflows/ci.yml` and add an end-to-end test next to the bash and
   fish ones in `integration/completion_test.go`.
4. If Homebrew casks support your shell, add it under `completions:` in the
   `homebrew_casks` section of [`.goreleaser.yaml`](../../../.goreleaser.yaml).

Everything else comes from the registry: `vaultr completion <name>`,
`vaultr completion install`, detecting the shell from `$SHELL`, and the
completion files in release archives.
