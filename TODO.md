# TODO

Feature ideas for vaultr. This branch is a notepad and is never merged;
each item becomes its own branch when picked up. All of them keep the
rules in AGENTS.md: only paths and key names are stored, values are read
live.

## In progress

- [ ] **Auto-login** (`auth.auto_login = true`): when the token is missing,
  expired or revoked, log in with the `[auth]` settings instead of showing
  the login screen first. OIDC opens the browser right away; ldap/userpass
  open the login screen with the username filled in and the password
  focused. CLI commands do it only on a terminal; scripts still fail with
  an error. Never from tab completion.
- [ ] **Resume after login:** an action that fails because the token
  expired (open, copy value, reveal) goes to login, then finishes with the
  new token. Search, results and selection stay in place.
- [ ] **No re-crawl after re-login:** keep the in-memory index and save it
  again under the new token when it belongs to the same identity (entity);
  rebuild when it doesn't.
- [ ] **Token expiry in the status line:** "token expires in 12m", yellow
  near the end; suggest `^l` for tokens that can't be renewed.

## Next

- [ ] **Search across all namespaces:** `find --all-ns`, a TUI toggle;
  results tagged with their namespace, opening one switches there. Merge
  the per-namespace caches, build the missing ones in parallel.
- [ ] **Environment variables from a secret:** `vaultr env PATH` prints
  `export KEY=...`; `vaultr exec PATH -- cmd` runs cmd with the keys as
  environment variables, nothing written to disk.
- [ ] **Versions and metadata** (KV v2) in the secret view: who changed it,
  when, how many versions; view an older version.
- [ ] **Retry with backoff on HTTP 429** (Vault Enterprise rate limit
  quotas), honouring `Retry-After`, so a busy server slows indexing down
  instead of leaving folders out.

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
