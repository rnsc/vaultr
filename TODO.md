# TODO

Feature ideas for vaultr. This branch is a notepad and is never merged;
each item becomes its own branch when picked up. All of them keep the
rules in AGENTS.md: only paths and key names are stored, values are read
live.

## Done

- [x] Auto-login (`auth.auto_login`), resume after login, keeping the index
  after a re-login by the same identity, token expiry countdown (v0.6.0).
- [x] `vaultr env` / `vaultr exec` (v0.7.0).
- [x] Search across all namespaces (v0.8.0), retry on HTTP 429 (v0.8.1).

## Next

- [ ] **Versions and metadata** (KV v2, branch `secret-versions`): which
  version is shown and when it was written, step to older ones, `vaultr
  versions`, `get --version`. Vault doesn't record who wrote a version.

## Later

- [ ] **Open in the Vault UI:** a TUI key and `vaultr open PATH` open the
  secret's page in the browser, in the right namespace.
- [ ] **Profiles for several servers:** `[profile.prod]`, `--profile`, a
  switcher like the namespace one.
- [ ] **Recent paths:** shown in the TUI when the search is empty, stored
  inside the encrypted index so they expire with it.
- [ ] **Auto-hide revealed values** after a few seconds in the secret view.

## Distribution

- [ ] `.deb` / `.rpm` packages and a Scoop manifest (GoReleaser).
- [ ] Sign releases with cosign, on top of checksums and provenance.
- [ ] PowerShell completion (see `internal/complete/shells/README.md`).
- [ ] Move the cask's `postflight_steps` from `custom_block` back to
  `hooks` once GoReleaser 2.19 is out (goreleaser/goreleaser#6870).

## Not planned

- Writing or editing secrets: vaultr stays a read-only lookup tool; the
  Vault UI link covers the occasional edit.
- Non-KV engines (database credentials, PKI): a different product.
