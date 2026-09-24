#!/usr/bin/env bash
# Start or stop a throwaway Vault (or OpenBao) dev server for tests.
#
#   scripts/vault-dev.sh start [VERSION]   # x.y.z, "latest" (default) or openbao-x.y.z
#   scripts/vault-dev.sh stop
#   scripts/vault-dev.sh env               # print export lines
#
# Vault comes from releases.hashicorp.com and OpenBao (which, unlike Vault
# CE, supports namespaces) from its GitHub releases. Binaries are cached in
# .cache/vault/; a "vault" of the requested version already on PATH is used
# as is.
# Settings: VAULT_DEV_PORT (default 8200), VAULT_DEV_TOKEN (default "root").
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
state="$root/.cache/vault"
port="${VAULT_DEV_PORT:-8200}"
token="${VAULT_DEV_TOKEN:-root}"
addr="http://127.0.0.1:$port"

latest() {
  curl -fsSL https://releases.hashicorp.com/vault/index.json |
    jq -r '.versions | keys[]' | grep -E '^[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1
}

install_openbao() {
  local version="$1" os arch dir
  os="$(uname -s)" # Linux / Darwin
  case "$(uname -m)" in
    x86_64 | amd64) arch=x86_64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
  esac
  dir="$state/openbao-$version"
  if [[ ! -x "$dir/bao" ]]; then
    echo "downloading openbao $version ($os/$arch)" >&2
    mkdir -p "$dir"
    curl -fsSL "https://github.com/openbao/openbao/releases/download/v${version}/bao_${version}_${os}_${arch}.tar.gz" |
      tar xz -C "$dir" bao
  fi
  echo "$dir/bao"
}

install() {
  local version="$1" os arch dir
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "unsupported arch $(uname -m)" >&2; exit 1 ;;
  esac
  dir="$state/$version"
  if [[ ! -x "$dir/vault" ]]; then
    echo "downloading vault $version ($os/$arch)" >&2
    mkdir -p "$dir"
    curl -fsSL -o "$dir/vault.zip" \
      "https://releases.hashicorp.com/vault/${version}/vault_${version}_${os}_${arch}.zip"
    unzip -o -q "$dir/vault.zip" -d "$dir"
    rm "$dir/vault.zip"
  fi
  echo "$dir/vault"
}

start() {
  local version="${1:-latest}" bin
  [[ "$version" == latest ]] && version="$(latest)"
  if [[ "$version" == openbao-* ]]; then
    bin="$(install_openbao "${version#openbao-}")"
  elif command -v vault >/dev/null && vault version | grep -q "v${version} "; then
    bin="$(command -v vault)"
  else
    bin="$(install "$version")"
  fi
  stop >/dev/null 2>&1 || true
  mkdir -p "$state"
  "$bin" server -dev -dev-root-token-id="$token" -dev-listen-address="127.0.0.1:$port" \
    >"$state/server-$port.log" 2>&1 &
  echo $! >"$state/server-$port.pid"
  for _ in $(seq 1 100); do
    if curl -fsS "$addr/v1/sys/health" >/dev/null 2>&1; then
      echo "$("$bin" version | awk '{print $1, $2}') listening on $addr (token: $token, log: $state/server-$port.log)" >&2
      return 0
    fi
    sleep 0.2
  done
  echo "vault did not become healthy:" >&2
  cat "$state/server-$port.log" >&2
  exit 1
}

stop() {
  if [[ -f "$state/server-$port.pid" ]]; then
    kill "$(cat "$state/server-$port.pid")" 2>/dev/null || true
    rm -f "$state/server-$port.pid"
    echo "vault stopped" >&2
  fi
}

case "${1:-}" in
  start) start "${2:-latest}" ;;
  stop) stop ;;
  env) echo "export VAULT_ADDR=$addr VAULT_TOKEN=$token" ;;
  *) sed -n '2,10p' "$0" >&2; exit 2 ;;
esac
