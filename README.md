# vaultr

Fast keyword search over HashiCorp Vault KV secrets. It searches every
path and every key name across all KV mounts. Values are fetched live, only
when you ask for them.

- **Recursive index** of every KV v1/v2 mount the token can see (concurrent crawl).
- **Instant search**: all terms must match a path or a key name. `k:` / `p:` limit a term to keys or paths.
- **TUI** for browsing and copying. The **CLI** (`find`, `get`) works for scripts and pipes.
- **Encrypted, time-bombed local cache** holding paths and key names only. It never stores values.

## Install

Download a binary from the
[releases page](https://github.com/rnsc/vaultr/releases): macOS (Apple
silicon, signed and notarized), Linux amd64/arm64, Windows amd64. Then
check it against `SHA256SUMS`.

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
vaultr get secret/prod/db password
vaultr index                    # force a rebuild
vaultr status                   # cache age, expiry, binding
vaultr purge                    # delete cache and its key
```

### Query syntax

| Query              | Matches                                                     |
|--------------------|-------------------------------------------------------------|
| `stripe`           | any path or key name containing `stripe`                    |
| `prod password`    | rows matching both terms (for example path has `prod`, key is `password`) |
| `k:token`          | key names containing `token`                                |
| `p:payments api`   | paths containing `payments`, with `api` in the path or key  |

Matching is case-insensitive. Exact key names rank first, then key prefixes,
then the last path segment.

### TUI keys

| List              |                        | Secret view          |                   |
|-------------------|------------------------|----------------------|-------------------|
| type              | filter                 | `↑` `↓`              | select key        |
| `↑` `↓` / `^n` `^p` | move                 | `r` / space          | reveal / hide     |
| `enter`           | open the secret        | `enter` / `c`        | copy value        |
| `^y`              | copy the row's value   | `y`                  | copy path         |
| `^o`              | copy the path          | `R`                  | reload            |
| `^r`              | rebuild the index      | `esc`                | back              |
| `esc` / `^c`      | quit                   |                      |                   |

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
| `cache_dir`  | `VAULTR_CACHE_DIR`  | user cache dir | Where the encrypted index lives                           |

Mounts are discovered through `sys/internal/ui/mounts`, which any token can
read (Vault filters the result by policy). If discovery fails, vaultr falls
back to `sys/mounts`. Set `mounts` to skip discovery.

Paths the token can't list or read are skipped and counted as "denied".
Paths that can be listed but not read are still indexed, just without key
names.

### Namespaces

Namespaces (Vault Enterprise, OpenBao) work through `VAULT_NAMESPACE` or
`namespace` in the config file. Each namespace gets its own cache file,
and the cache key lives in the token's cubbyhole inside that namespace.
vaultr searches only the configured namespace, not its child namespaces.

## Cache security

The cache holds secret **paths and key names, never values**. It is written
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

The first release is `v0.1.0`. You can also release by hand: push a
`vX.Y.Z` tag, or run the **release** workflow from the Actions tab and pick
the bump.

### macOS signing and notarization

GoReleaser signs and notarizes the macOS binary from Linux. This needs an
[Apple Developer Program](https://developer.apple.com/programs/) membership
and these repository secrets:

| Secret                   | Value                                                          |
|--------------------------|----------------------------------------------------------------|
| `MACOS_SIGN_P12`         | base64 of your **Developer ID Application** certificate exported as `.p12` (`base64 -i cert.p12`) |
| `MACOS_SIGN_PASSWORD`    | the `.p12` export password                                     |
| `MACOS_NOTARY_ISSUER_ID` | App Store Connect API issuer ID                                |
| `MACOS_NOTARY_KEY_ID`    | App Store Connect API key ID                                   |
| `MACOS_NOTARY_KEY`       | base64 of the API key `.p8` file                               |

Create the API key under App Store Connect, Users and Access,
Integrations, with the Developer role. Until the secrets are set, releases
still go out, but the macOS binary is unsigned and the release run shows a
warning.
