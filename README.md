# vaultr

Fast keyword search over HashiCorp Vault KV secrets. It searches every
path and every key name across all KV mounts. Values are fetched live, only
when you ask for them.

![vaultr TUI: search, reveal a value, refresh the index](demo/tui.gif)

![vaultr CLI: find, get, and refresh](demo/cli.gif)

- **Recursive index** of every KV v1/v2 mount the token can see (concurrent crawl).
- **Instant search**: all terms must match a path or a key name. `k:` / `p:` limit a term to keys or paths.
- **TUI** for browsing and copying. The **CLI** (`find`, `get`) works for scripts and pipes.
- **Encrypted, time-bombed local cache** holding paths and key names only. It never stores values.

## Install

On macOS (Apple silicon) or Linux, with [Homebrew](https://brew.sh):

```sh
brew install rnsc/tap/vaultr
```

`brew upgrade vaultr` picks up new releases. Homebrew also installs the
bash, zsh and fish completion scripts (see [Tab completion](#tab-completion)).

Prebuilt binaries are published on the
[releases page](https://github.com/rnsc/vaultr/releases) for macOS (Apple
silicon), Linux amd64/arm64 and Windows amd64.

On macOS and Linux, the install script picks the right archive, checks it
against `SHA256SUMS`, and installs it to `~/.local/bin`:

```sh
curl -fsSL https://raw.githubusercontent.com/rnsc/vaultr/main/scripts/install.sh | bash
# or, from a clone:
scripts/install.sh [--version v0.1.0] [--dir /usr/local/bin]
```

The macOS binary is ad-hoc signed but not notarized, so there's no Apple
Developer account behind it. Installed with Homebrew, the script, `gh` or
`curl`, it just runs. If you download it **with a browser**, macOS quarantines it and
refuses to open it. Clear the flag once with
`xattr -d com.apple.quarantine ./vaultr`.

Or build from source:

```sh
go install github.com/rnsc/vaultr@latest
```

## Usage

```sh
export VAULT_ADDR=https://vault.example.com
vault login -method=oidc        # or VAULT_TOKEN=...

vaultr                          # interactive search
vaultr prod db                  # interactive, pre-filled query
vaultr find stripe k:key        # print "path<TAB>key" lines (exit 1 if none)
vaultr find --values ldap       # also fetch the matching values
vaultr find --json -n 20 redis  # JSON lines
vaultr find -r paypal           # refresh the index first (secret added since)
vaultr -r                       # TUI, refreshing the index at startup
vaultr get secret/prod/db password
vaultr login                    # log in with the [auth] defaults from the config
vaultr login -method ldap -username jdoe
vaultr index                    # force a rebuild
vaultr status                   # cache age, expiry, binding
vaultr purge                    # delete cache and its key
```

### Tab completion

Installed with Homebrew, completion works in every shell that loads
Homebrew's completions (fish does by default; for bash and zsh see
[Homebrew's shell completion guide](https://docs.brew.sh/Shell-Completion)).
Otherwise:

```sh
vaultr completion install   # detects your shell from $SHELL (bash, zsh, fish)
```

Open a new shell, then:

```
$ vaultr get secret/pr<TAB>              ->  vaultr get secret/prod/
$ vaultr get secret/prod/<TAB><TAB>      ->  db/  payments/  ...
$ vaultr get secret/prod/db/postgres p<TAB>  ->  password  port
```

Commands, flags, `login -method` values and namespace names (after `--ns`)
complete too.

- **Where candidates come from:** paths and key names come from the local
  index when it's valid, which is instant. With no index, an expired one, or
  nothing matching (a secret added after the index was built), vaultr lists
  just that folder live in Vault instead. Completion never builds the full
  index.
- **When it can't complete paths:** with no token, an expired one, or an
  unreachable server, you still get commands and flags. Paths just don't
  complete, and nothing is printed to your terminal.
- **What install changes:** bash and zsh get one line in `~/.bashrc` or
  `~/.zshrc` (`eval "$(vaultr completion <shell>)"`) that loads the script at
  startup, so it always matches the installed vaultr. fish gets
  `~/.config/fish/completions/vaultr.fish`. Running install again changes
  nothing.
- **Setting it up yourself:** print the script with
  `vaultr completion bash|zsh|fish`. Release archives also include the
  scripts in `completions/`.

Another shell? See [Contributing](#contributing).

### Query syntax

| Query              | Matches                                                     |
|--------------------|-------------------------------------------------------------|
| `stripe`           | any path or key name containing `stripe`                    |
| `prod password`    | rows matching both terms (for example path has `prod`, key is `password`) |
| `k:token`          | key names containing `token`                                |
| `p:payments api`   | paths containing `payments`, with `api` in the path or key  |

Matching is case-insensitive. Exact key names rank first, then key prefixes,
then the last path segment.

### Secrets as environment variables

`vaultr env` prints a secret's keys as shell exports, and `vaultr exec`
runs a command with them in its environment. Values are read live and
never written to disk.

```sh
eval "$(vaultr env secret/prod/db/postgres)"      # PASSWORD, USERNAME, ... in this shell
vaultr env --prefix DB_ secret/prod/db/postgres   # DB_PASSWORD, DB_USERNAME, ...
vaultr env --format fish secret/app | source      # fish; --format json for tools
vaultr exec secret/prod/db/postgres secret/prod/payments/stripe -- ./run.sh
```

- **Names:** key names become upper case, with anything but letters,
  digits and `_` turned into `_` (`api-key` becomes `API_KEY`).
- **Several paths:** when two secrets have the same key, the later path
  wins, and vaultr warns about it.
- **`exec`:** everything after `--` is the command, run with your
  environment plus the secrets. vaultr exits with the command's exit
  status.

### Versions (KV v2)

The secret view shows which version you're looking at and when it was
written, for example `version 3 of 3 (current) · written 2h ago`. `[` and
`]` step to older and newer versions; reveal and copy work on the version
shown. Deleted and destroyed versions are marked as such.

```sh
vaultr versions secret/prod/db/postgres           # VERSION, CREATED, STATE (current, deleted, destroyed)
vaultr get --version 2 secret/prod/db/postgres password
```

Vault records when each version was written, not who wrote it (only its
audit log knows). KV v1 mounts keep no versions.

### Recent secrets

With an empty search, the TUI lists the last 10 secrets you opened or
copied from first, marked `recent`. They are kept inside the encrypted
index, so they expire with it (at most 2 hours, or when the token dies)
and never include values. Each namespace has its own list.

### Opening a secret in the Vault UI

Press `o` in the secret view, or run `vaultr open PATH` (`--print` just
prints the address), to open the secret in the Vault web UI, in the right
namespace. Use it for what vaultr doesn't do, like editing. The UI may
ask you to log in first; it then takes you to the secret. On Vault 1.15
and later, KV v2 secrets open on their Overview tab; the values are on the
Secret tab.

### Finding secrets added recently

The index is a snapshot, rebuilt at most every 2 hours. If a secret was
added since, refresh it:

- **TUI:** when nothing matches, the list says so and shows how old the
  index is. Press `enter` (or `^r` at any time) to refresh it; your search
  stays in place. `vaultr -r` refreshes at startup.
- **CLI:** `vaultr find -r QUERY` refreshes before searching. Without
  `-r`, a search with no results says how old the index is and suggests
  the `-r` retry. `vaultr refresh` (or `vaultr index`) refreshes without
  searching.

A refresh re-crawls every mount, which takes about a second for a few
thousand secrets.

### TUI keys

| Search list         |                                   | Secret view     |               |
|---------------------|-----------------------------------|-----------------|---------------|
| type                | filter                            | `↑` `↓`         | select key    |
| `↑` `↓` / `^j` `^k` | move                              | `r` / space     | reveal / hide |
| `enter`             | open the secret (refresh the index when nothing matches) | `enter` / `c`   | copy value    |
| `^y`                | copy the row's value              | `y`             | copy path     |
| `^o`                | copy the path                     | `R`             | reload        |
| `^r`                | refresh the index                 | `esc`           | back          |
| `^l`                | log in (again)                    | `^c`            | quit          |
| `^e`                | edit the config file              | `[` / `]`       | older / newer version (KV v2) |
|                     |                                   | `o`             | open in the Vault UI |
| `^n`                | switch namespace ([more](#switching-namespaces)) |  |               |
| `esc`               | clear the search (never quits)    |                 |               |
| `^c`                | clear the search, or quit if it's empty |           |               |

Copying uses the system clipboard (pbcopy, xclip, xsel, wl-copy). If none is
available it falls back to OSC 52, which works over SSH in most terminals.
Values copied to the system clipboard are cleared after 45s if still there.

## Configuration

Each setting is resolved in this order: **environment variable, then the
config file, then the built-in default**.

### Connection

| Setting          | Environment                                | Config file   |
|------------------|--------------------------------------------|---------------|
| Server address   | `VAULT_ADDR`, then `VAULT_URL`             | `address`     |
| Token            | `VAULT_TOKEN`, then `~/.vault-token`       | never         |
| Namespace        | `VAULT_NAMESPACE` (`/` forces the root one) | `namespace`   |
| Token namespace  | `VAULTR_TOKEN_NAMESPACE`                   | `token_namespace` (auto-detected) |
| CA certificate   | `VAULT_CACERT`                             | `ca_cert`     |
| Client cert/key  | `VAULT_CLIENT_CERT`, `VAULT_CLIENT_KEY`    | `client_cert`, `client_key` |
| Skip TLS verify  | `VAULT_SKIP_VERIFY`                        | no            |

The token is never read from the config file. `vault login` writes
`~/.vault-token`, and vaultr picks it up from there.

### Config file

The config file lives at `~/.config/vaultr/config.toml`, or
`$XDG_CONFIG_HOME/vaultr/config.toml`, or `%AppData%\vaultr\config.toml`
on Windows. Set `VAULTR_CONFIG` to use another path. `vaultr config init`
writes a commented template, and `vaultr config` shows each effective
setting and where it came from.

```toml
address   = "https://vault.example.com"
namespace = "team-a"
mounts    = ["secret", "kv-team"]
```

Misspelled keys are rejected, so a typo doesn't silently fall back to a
default.

### vaultr settings

| Config key   | Environment         | Default        | Meaning                                                   |
|--------------|---------------------|----------------|-----------------------------------------------------------|
| `mounts`     | `VAULTR_MOUNTS`     | discover       | KV mounts to index (comma separated in the env var)       |
| `workers`    | `VAULTR_WORKERS`    | `32`           | Concurrent requests while indexing                        |
| `max_age`    | `VAULTR_MAX_AGE`    | `2h`           | Cache lifetime. Values above 2h are capped at 2h          |
| `paths_only` | `VAULTR_PATHS_ONLY` | `false`        | Index paths only. Faster, and needs no `read` permission, but you can't search key names |
| `clip_clear` | `VAULTR_CLIP_CLEAR` | `45s`          | Clipboard auto-clear delay in the TUI (`0` disables)      |
| `reveal_timeout` | `VAULTR_REVEAL_TIMEOUT` | `30s`  | Hide values revealed in the TUI again after this long (`0` keeps them shown) |
| `cache_dir`  | `VAULTR_CACHE_DIR`  | user cache dir | Where the encrypted index lives                           |

Mounts are discovered through `sys/internal/ui/mounts`, which any token can
read (Vault filters the result by policy). If discovery fails, vaultr falls
back to `sys/mounts`. Set `mounts` to skip discovery.

Paths the token can't list or read are skipped and counted as "denied".
Paths that can be listed but not read are still indexed, just without key
names.

If the server has a rate limit quota (Vault Enterprise, and also Vault CE
and OpenBao), a burst of index requests can get `HTTP 429` answers. vaultr
waits and retries them, following the server's `Retry-After` when given,
so a busy server makes indexing slower rather than incomplete. If it's
still too slow, lower `workers`.

### Logging in

`vaultr login` and the TUI's login screen (`^l`, or opened automatically
when there's no token or it has expired) support four methods:

| Method     | What happens                                                                 |
|------------|------------------------------------------------------------------------------|
| `oidc`     | Opens your browser at the identity provider, like `vault login -method=oidc`. A local callback on `localhost:8250` receives the result. |
| `ldap`     | Username and password (the password is never echoed or stored).              |
| `userpass` | Username and password.                                                       |
| `token`    | Paste an existing token; vaultr checks it before using it.                   |

The token is saved to `~/.vault-token` with mode 0600, like `vault login`
does, so the `vault` CLI and later vaultr runs pick it up. Set
`save_token = false` to keep it in memory only. `vaultr login -no-save`
prints it instead. If `VAULT_TOKEN` is set in your environment, vaultr
warns you, because that variable overrides the saved file in new shells.

Defaults come from the `[auth]` section of the config file, so logging in
is usually just `vaultr login` (or enter on the login screen):

```toml
[auth]
method   = "oidc"
role     = ""        # the mount's default role
# mount  = "oidc"    # if your auth mount has another path, e.g. "corp-oidc"
# namespace = "/"    # where to log in; defaults to token_namespace, else root
# username = "jdoe"  # for ldap / userpass
# callback_port = 8250
# auto_login = true  # log in by yourself when the token is missing or expired
```

Your OIDC role must allow the redirect URI
`http://localhost:8250/oidc/callback`, the same one the `vault` CLI uses.
Flags override the config: `-method`, `-mount`, `-namespace`,
`-username`, `-role`, `-callback-port`.

#### When the token expires

- **The status line counts down** to the token's expiry ("token 1h20m").
  Under 10 minutes it turns yellow; for a token that can't be renewed it
  suggests `^l` to log in again.
- **You don't lose your place.** If opening a secret, copying a value
  (`^y`) or refreshing the index fails because the token expired or was
  revoked, vaultr asks you to log in, then finishes that action with the
  new token. Your search and selection stay as they were. A secret
  already open keeps its values on screen, so revealing and copying them
  still work after expiry.
- **The index is kept** when the same person logs in again: vaultr saves
  it under the new token instead of crawling Vault again. It checks the
  token's identity (its Vault entity) to decide. If someone else logs in,
  or the token has no identity (root tokens, tokens created by hand), the
  index is rebuilt for them.

With `auto_login = true` in `[auth]`, vaultr logs in by itself when the
token is missing, expired or revoked:

- **OIDC** opens the browser right away; you only approve the login.
- **ldap / userpass** open the login screen with your username filled in
  and the cursor on the password.
- **CLI commands** (`find`, `get`, `index`) log in first, then run, but
  only in a terminal. Scripts and pipes get the usual error instead of a
  prompt.
- A failed automatic login isn't retried by itself, and tab completion
  never logs in.

### Editing the config in the TUI

Press `^e` to edit the config file. It's created, with its directory, if
it doesn't exist. Move with `↑` `↓`, type to change a value, and use `←` `→`
or space for choices and on/off settings. `^s` validates, saves and applies
the settings. `esc` discards your changes. Settings that an environment
variable currently overrides are flagged, so you can see why a change
doesn't take effect. The file is rewritten with a comment for every
setting, so comments you added by hand are not kept.

### Namespaces

Namespaces (Vault Enterprise, OpenBao) work through `VAULT_NAMESPACE` or
`namespace` in the config file. vaultr searches only that namespace, not
its child namespaces.

A common enterprise setup is to log in at the root namespace and read
secrets in a team namespace. That works with only the namespace
configured:

```toml
# ~/.config/vaultr/config.toml
address   = "https://vault.example.com"
namespace = "team-a"
```

```sh
VAULT_NAMESPACE= vault login -method=oidc   # token issued in the root namespace
vaultr                                      # searches team-a
```

Secret calls go to `team-a`. Token calls (the lookup, and the cubbyhole
that holds the cache key) go to the namespace the token was issued in,
which vaultr detects: the server reports it, or vaultr checks whether the
token is valid in the root namespace. That keeps the time bomb in place,
so revoking or expiring your root-namespace login makes the cache
unreadable. `vaultr status` shows both namespaces. If detection picks the
wrong one, set `token_namespace` (`"/"` for root) or
`VAULTR_TOKEN_NAMESPACE`.

Each secrets namespace gets its own cache file. The TUI status line shows
the active namespace.

#### Switching namespaces

- **TUI:** press `^n` to list the namespaces your token can use, type to
  filter them, and press `enter` to switch. vaultr then loads that
  namespace's index from the cache, or builds it. The switch lasts until
  you quit.
- **CLI:** `--ns NAMESPACE` (or `--namespace`) works with every command
  except `login`, before or after the command, for that run only:

  ```sh
  vaultr --ns team-b find db       # or: vaultr find db --ns team-b
  vaultr get --ns team-a/child secret/app/db password
  vaultr --ns /                    # the TUI, in the root namespace
  ```

  It takes precedence over `VAULT_NAMESPACE` and the config file. Tab
  completes namespace names after `--ns`, and paths then complete in that
  namespace.

The list holds your token's namespace and the namespaces below it that its
policies reach. It comes from `sys/internal/ui/namespaces`, the endpoint
behind the Vault UI's namespace picker, which any token may call.

#### Searching every namespace

When you don't know which namespace holds a secret:

- **TUI:** the first entry of the `^n` list is **all namespaces**. Results
  then start with their namespace (`team-a · secret/app/db`), and opening
  one reads it in that namespace. `^r` rebuilds every namespace; picking a
  single namespace in `^n` goes back to normal.
- **CLI:** `vaultr find --all-ns QUERY` adds the namespace as the first
  column (`namespace` in `--json`).

The namespace name counts as part of the path, so `team-a db` narrows the
results to team-a. Each namespace keeps its own encrypted cache, the same
one it uses on its own: valid caches are loaded, and the others are built,
four namespaces at a time sharing the usual number of workers. Namespaces
with no KV mounts your token can see are skipped.

## Memory

vaultr keeps the token in memory, like the `vault` CLI, and the values of
a secret while it is open. To keep other programs out of that memory:

- **Linux:** vaultr marks itself non-dumpable. Other processes of your
  user (only root excepted) can't read its memory or environment, and no
  core dump is written.
- **macOS:** debuggers can't attach to it (root can get around this).
- **Values** leave memory's reach sooner: they are dropped when you leave
  the secret view, and revealed values are hidden again after
  `reveal_timeout` (30 seconds by default).

Set `VAULTR_ALLOW_DEBUG=1` to attach a debugger to vaultr. This narrows
exposure but isn't a wall: malware running as you could use the token
itself.

## Cache security

The cache holds secret **paths and key names, never values**, plus the
last 10 rows you opened or copied in the TUI (also just path and key
name). It is written
with mode `0600` to `$XDG_CACHE_HOME/vaultr/` (`~/Library/Caches/vaultr` on
macOS). There is one file per Vault address and namespace.

It is encrypted with AES-256-GCM, using a key derived with HKDF-SHA256 from:

1. **the Vault token**, and
2. **a random 256-bit data key stored in the token's cubbyhole**
   (`cubbyhole/vaultr/<id>`).

Vault deletes a token's cubbyhole when the token expires or is revoked. So
once the token is gone, the data key is gone too, and nobody can decrypt the
cache again. That includes someone who copied both the file and the old
token. This is the time bomb, and Vault enforces it on the server, not the
local machine.

On top of that:

- **Hard expiry.** Every cache expires 2 hours after it is built, or when
  the token expires if that comes first. The expiry is stored in the file
  header, which is authenticated, so editing it breaks decryption. It is
  checked against the Vault server's `Date` header, so changing the local
  clock does not extend it.
- **Deleted when stale.** vaultr deletes the file and its cubbyhole key as
  soon as it finds the cache expired, tampered with, or unreadable with the
  current token. Then it rebuilds with the current token.
- **One key per build.** Each rebuild gets a new salt and a new data key,
  and deletes the old key.
- **Fallback.** If the token's policy blocks writes to its cubbyhole (the
  `default` policy allows them), the cache falls back to token-only
  encryption plus the 2h expiry, and `vaultr status` reports it as
  `token only`.

## Development

```sh
make test          # unit tests, no Vault needed
make lint          # gofmt + go vet
make integration   # starts a throwaway Vault dev server, runs the integration suite, stops it
make dev-vault     # dev server loaded with the fixture tree, for trying vaultr by hand
VAULT_VERSION=openbao-2.5.5 make integration   # same suite on OpenBao, including namespaces
```

`make integration` and `make dev-vault` download the Vault binary into
`.cache/vault/` (Linux and macOS). Pick a version with
`VAULT_VERSION=1.15.6` and a port with `VAULT_DEV_PORT=8300`. To run the
suite against a server you already have, point it at a disposable one with
a root token:

```sh
VAULT_ADDR=http://127.0.0.1:8200 VAULT_TOKEN=root go test -tags integration ./integration
```

### What is tested

- **Unit** (`go test ./...`): search ranking and filters; the Vault
  client's URL encoding, error mapping and env parsing; the cache format
  against a fake server (tampering, server-clock expiry, a key wiped from
  the cubbyhole); the TUI model (filtering, scrolling, masked and revealed
  values, copy, clipboard auto-clear, index builds, rebuilds and
  cancellation); CLI argument and settings parsing.
- **Integration** (`integration/`, build tag `integration`): each run
  creates its own mounts and policies under a random prefix: a KV v2 tree
  with about 115 secrets, a KV v1 mount, an empty mount and a transit mount
  that must be ignored. It removes them afterwards. The tree includes
  deep nesting, a secret sharing its name with a folder, names with
  spaces, `#`, `?` and non-ASCII characters, non-string values, and
  deleted, destroyed and multi-version secrets. The fixture is written
  with its own HTTP client, so it doesn't depend on the code under test.
  The suite covers:
  - mount discovery
  - crawling with root, restricted, list-only and no-cubbyhole tokens
  - the cache time bomb against a real server: token expiry, revocation,
    stolen file copies, max age, key rotation and purge
  - the built `vaultr` binary end to end: every command, flag and setting,
    exit codes and error messages
  - the TUI, driven through a real pseudo-terminal: search, reveal,
    reindex, clearing the search, logging in, and creating the config file
    in the editor
  - `vaultr login` with userpass, token and OIDC. OIDC runs against a
    fake identity provider (`internal/testvault/fakeidp.go`) that Vault
    itself validates, with `curl` playing the browser.
  - namespaces, on OpenBao: nested namespaces, and logging in at the root
    namespace while reading secrets in a team namespace, both from the CLI
    and the TUI

CI (`.github/workflows/ci.yml`) runs on every pull request and on pushes
to `main`:

- lint: gofmt, vet, staticcheck, `go mod tidy`, shellcheck
- unit tests with the race detector
- the integration suite against Vault 1.15, 1.21 and the latest release
  and OpenBao on Linux, plus the latest Vault release on macOS. OpenBao
  also runs the namespace tests, which skip on Vault CE because namespaces
  are an Enterprise feature there.
- a dry run of the release build, with the binaries attached to the run

`ci-ok` is a single job that sums up the others, to use as the required
check for branch protection.

## Demo GIFs

The GIFs above are recorded with [VHS](https://github.com/charmbracelet/vhs)
from `demo/tui.tape` and `demo/cli.tape`, against a throwaway dev server
loaded with fake demo secrets (`go run ./tools/seed -demo`).

`.github/workflows/demo.yml` keeps them current. On a pull request that
changes something they show (Go code, the tapes, the demo scripts), it
re-records them on the runner and commits them to the PR branch. GitHub
creates that commit through its API and signs it, so it shows as
**Verified**. Then it starts CI on the new commit. When the PR merges,
`main` and the release it triggers already have the updated GIFs. A hash
of the inputs (`demo/inputs.sha256`) is stored with them, so PRs that
don't touch those inputs skip the recording.

To record them locally, install `vhs`, `ttyd` and `ffmpeg` and run
`make demo`.

## Releases

`.github/workflows/release.yml` publishes a GitHub release with binaries
and `SHA256SUMS` whenever CI passes on `main` after a merge. The version
bump comes from the merge commit message, which with squash merges is the
PR title and description:

| In the commit message                                   | Release            |
|---------------------------------------------------------|--------------------|
| nothing special                                         | patch, v0.1.**1**  |
| `feat: ...`, or `[minor]`                               | minor, v0.**2**.0  |
| `feat!: ...`, `BREAKING CHANGE`, or `[major]`           | major, v**1**.0.0  |
| `[skip release]`                                        | none               |

A merge releases only when something users download changed since the last
release: Go code (not tests), `go.mod`/`go.sum`, the completion scripts or
`.goreleaser.yaml`. Docs, tests and CI changes alone don't, unless the
message says `[release]`. `scripts/next-version.sh` holds the list.

The first release is `v0.1.0`. You can also release by hand: push a
`vX.Y.Z` tag, or run the **release** workflow from the Actions tab and pick
the bump.

## Contributing

Adding completion for another shell takes one script and one registry
entry. [`internal/complete/shells/README.md`](internal/complete/shells/README.md)
explains the (small) contract and the steps. The tests check every
registered shell automatically.
