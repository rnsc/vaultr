#!/usr/bin/env bash
# Record the README GIFs (demo/*.gif) from demo/*.tape with VHS against a
# throwaway Vault dev server seeded with fake demo secrets.
#
#   scripts/demo.sh          render the GIFs and update demo/inputs.sha256
#   scripts/demo.sh hash     print the hash of everything the GIFs depend on
#
# Needs vhs (github.com/charmbracelet/vhs), ttyd, ffmpeg and Chrome/Chromium.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

inputs_hash() {
  git ls-files -s -- '*.go' go.mod go.sum 'demo/*.tape' scripts/demo.sh scripts/vault-dev.sh |
    sha256sum | cut -c1-64
}

if [[ "${1:-}" == hash ]]; then
  inputs_hash
  exit 0
fi

export VAULT_DEV_PORT="${VAULT_DEV_PORT:-8299}"
work="$(mktemp -d)"
cleanup() {
  scripts/vault-dev.sh stop >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

scripts/vault-dev.sh start "${VAULT_VERSION:-latest}"
export VAULT_ADDR="http://127.0.0.1:$VAULT_DEV_PORT" VAULT_TOKEN=root
go run ./tools/seed -demo

mkdir -p "$work/bin"
go build -o "$work/bin/vaultr" .
ln -s "$(scripts/vault-dev.sh bin)" "$work/bin/vault"
export PATH="$work/bin:$PATH"
export VAULTR_CACHE_DIR="$work/cache" VAULTR_CONFIG="$work/config.toml" VAULTR_CLIP_CLEAR=0

for tape in demo/*.tape; do
  echo "recording $tape" >&2
  vhs "$tape"
done
inputs_hash >demo/inputs.sha256
ls -l demo/*.gif >&2
