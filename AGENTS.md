# AGENTS.md

Notes for AI coding agents (and humans) working on vaultr. The README
describes what vaultr does; this file covers how to change it.

## What it is

A Go CLI and TUI that searches HashiCorp Vault / OpenBao KV paths and key
names from a local index, and fetches values live on demand.

- `main.go`: command dispatch, flags, the `app` type (the TUI's backend).
- `internal/vault`: minimal HTTP client (namespaces, token namespace
  detection, KV v1/v2).
- `internal/index`: concurrent crawler building the index.
- `internal/cache`: the encrypted, time-bombed index file.
- `internal/search`: query matching and ranking.
- `internal/tui`: Bubble Tea UI, one file per screen (`login.go`,
  `configedit.go`, `namespace.go`).
- `internal/config`: settings from env, `~/.config/vaultr/config.toml`
  and defaults; `fields.go` drives the TUI config editor and template.
- `internal/auth`: login methods (oidc, ldap, userpass, token).
- `internal/complete`: shell completion. Logic in Go, thin scripts in
  `shells/` (see `shells/README.md` to add a shell).
- `extension/`: the browser extension, plain JS with no build step,
  embedded in the binary (`embed.go` lists the files). One
  `manifest.json` for both browsers; the installer writes a per-browser
  copy with vaultr's version.
- `internal/nativehost`: the native messaging protocol and its handler.
  `nativehost.go` in `main` adapts the `app` to it; `isNativeHost`
  recognizes the arguments browsers start vaultr with.
- `internal/browserext`: install paths per OS and browser, host
  manifests, the Windows registry.
- `internal/testvault`: fixtures and a fake OIDC IdP for integration tests.
- `integration/`: tests against a real server (build tag `integration`).

## Rules that must not break

- **The cache never holds secret values**, only paths and key names. It is
  encrypted with a key bound to the token's cubbyhole and expires after at
  most 2 hours, checked against the server's clock. Keep all of that true.
- **Go version:** `go.mod` says `go 1.24.7`, the minimum. Don't let
  `go get` raise it; pin dependencies that would (e.g. `golang.org/x/term`
  stays at v0.33.0). Release builds use the latest Go anyway.
- **No cgo.** Releases cross-compile with `CGO_ENABLED=0`.
- **Shell completion must stay silent and fast:** `vaultr __complete`
  always exits 0, never writes to stderr, and gives up after 3 seconds.
- **In native host mode, stdout carries only protocol frames.** Values
  go only to the background script's fill, never to the popup, and only
  for the one key picked. The popup can't send `get` (`POPUP_TYPES` in
  `background.js`). The extension keeps its three permissions
  (`activeTab`, `scripting`, `nativeMessaging`): no host permissions, no
  content scripts. Directory passwords (ldap, userpass) never go through
  the extension.
- **The Chrome extension ID** comes from the public `key` in
  `extension/manifest.json` and is pinned in `browserext.ChromeID` and
  every user's host manifest: don't change the key.

## Checking changes

```sh
make lint          # gofmt, go vet (with and without the integration tag)
make test          # unit tests, -race
make integration   # starts a throwaway Vault dev server, runs ./integration
VAULT_VERSION=openbao-2.5.5 make integration   # OpenBao: covers namespaces
```

CI also runs staticcheck (`go run honnef.co/go/tools/cmd/staticcheck@2025.1.1
-tags integration ./...`) and shellcheck. Namespace tests skip on Vault CE;
run them on OpenBao. `scripts/vault-dev.sh` downloads the server binary.

CI is the source of truth; the maintainer doesn't run tests by hand. It
runs on GitHub runners: Ubuntu 24.04 and macOS 15 (bash 3.2 there, so
shell code must work with it), plus a non-blocking Ubuntu 26.04 job.
Every change needs tests: unit tests next to the code, and integration
tests when it talks to Vault. TUI screens have unit tests with a fake
backend (`internal/tui/backend_test.go`) and pty tests in
`integration/tui_test.go`. The extension has host-mode tests
(`integration/nativehost_test.go`) and browser tests
(`integration/extension_test.go`); the end-to-end one needs Chrome for
Testing or Chromium (`VAULTR_TEST_EXTENSION_CHROME`), as Google Chrome
ignores `--load-extension`.

## Commits, PRs and releases

- **Merging to main releases.** The merge commit message sets the bump:
  `feat:` or `[minor]` for minor, `!:` / `BREAKING CHANGE` / `[major]`
  for major, `[skip release]` for none, patch otherwise
  (`scripts/next-version.sh`). Only merges that change what users
  download release: Go code (not tests), modules, completion scripts,
  `.goreleaser.yaml`; `[release]` forces one. If you add a file that ends
  up in the binary or archives, add it to `shipped` in that script. GoReleaser builds darwin/arm64, linux
  amd64/arm64 and windows/amd64 and updates the Homebrew cask in
  `rnsc/homebrew-tap`.
- **Commit as the maintainer:** no `Co-Authored-By` or session-link
  trailers for AI tools. The maintainer signs commits afterwards, so add
  new commits rather than rewriting history on a branch they have touched.
- **Branch names** describe the change (`namespace-switcher`), with no
  tool names or random suffixes.
- **Demo GIFs** (`demo/*.gif`) are recorded by CI when Go code, the tapes
  or the demo scripts change, and committed back to the PR branch. Don't
  edit them or `demo/inputs.sha256` by hand; change `demo/*.tape`.

## Firefox signing (not set up yet)

Stable Firefox installs only add-ons signed by Mozilla. The plan, once
AMO API credentials exist (secrets `AMO_JWT_ISSUER`, `AMO_JWT_SECRET`):
in `release.yml`, after the version is picked and before the tag is
pushed, run `go run . extension export --browser firefox --version $TAG
DIR` and `npx web-ext sign --channel=unlisted --source-dir DIR`, put the
result at `extension/dist/vaultr.xpi` (embedded by `go:embed`, gitignored)
and add it to the release assets and `SHA256SUMS`. AMO signs a version
once: a rerun downloads the already signed file instead. If signing
fails, no tag is pushed. Until then `vaultr extension install --browser
firefox` writes the unpacked add-on for about:debugging.

## Writing style

README and help text are plain and direct: short sentences, lists for
anything with several parts, no marketing. Don't use em dashes. Explain
why when a choice isn't obvious, in comments too.
