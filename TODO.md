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

- [ ] **Memory hardening:** less for another process to find in vaultr's
  memory in a long TUI session. Same-user malware could use the token
  instead, so this narrows exposure rather than closing a hole.
  - Linux: `prctl(PR_SET_DUMPABLE, 0)` at startup, so other non-root
    processes can't read vaultr's memory (`/proc/<pid>/mem`, ptrace)
    whatever `ptrace_scope` says; also no core dumps.
  - macOS: `ptrace(PT_DENY_ATTACH)`, so debuggers can't attach (root can
    still get around it).
  - An environment variable turns both off, for debugging vaultr itself.
  - Drop a secret's values when leaving the secret view, and auto-hide
    revealed values after a few seconds. Go can't wipe strings, so this
    only narrows what is in memory at any time.
  - Test on Linux that another process of the same user can't read the
    memory.

- [ ] **Open in the Vault UI:** a TUI key and `vaultr open PATH` open the
  secret's page in the browser, in the right namespace.
- [ ] **Profiles for several servers:** `[profile.prod]`, `--profile`, a
  switcher like the namespace one.
- [ ] **Recent paths:** shown in the TUI when the search is empty, stored
  inside the encrypted index so they expire with it.

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
