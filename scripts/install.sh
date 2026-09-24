#!/usr/bin/env bash
# Install the latest vaultr release (or a given version) for this machine.
#
#   scripts/install.sh [--version vX.Y.Z] [--dir ~/.local/bin] [--from DIR]
#
# Uses the GitHub CLI when available (works for private repositories),
# otherwise curl. The archive is checked against SHA256SUMS. Files fetched
# this way carry no macOS quarantine flag, so the (ad-hoc signed) binary
# runs without Gatekeeper prompts. --from installs from a local dist/
# directory instead (used by CI).
set -euo pipefail

repo="rnsc/vaultr"
version=""
dir="${VAULTR_INSTALL_DIR:-$HOME/.local/bin}"
from=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="$2"; shift 2 ;;
    --dir) dir="$2"; shift 2 ;;
    --from) from="$2"; shift 2 ;;
    -h | --help) sed -n '2,11p' "$0"; exit 0 ;;
    *) echo "unknown argument $1" >&2; exit 2 ;;
  esac
done

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
esac
case "$os/$arch" in
  darwin/arm64 | linux/amd64 | linux/arm64) ;;
  *) echo "no prebuilt binary for $os/$arch; use: go install github.com/$repo@latest" >&2; exit 1 ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
pattern="vaultr_*_${os}_${arch}.tar.gz"

if [[ -n "$from" ]]; then
  cp "$from"/vaultr_*_"${os}_${arch}".tar.gz "$from/SHA256SUMS" "$tmp/"
elif command -v gh >/dev/null && gh auth status >/dev/null 2>&1; then
  gh release download ${version:+"$version"} --repo "$repo" --dir "$tmp" \
    --pattern "$pattern" --pattern SHA256SUMS
else
  if [[ -z "$version" ]]; then
    version="$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$repo/releases/latest")"
    version="${version##*/}"
  fi
  base="https://github.com/$repo/releases/download/$version"
  file="vaultr_${version#v}_${os}_${arch}.tar.gz"
  curl -fsSL -o "$tmp/$file" "$base/$file"
  curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
fi

matches=("$tmp"/vaultr_*_"${os}_${arch}".tar.gz)
archive="$(basename "${matches[0]}")"
(
  cd "$tmp"
  if command -v sha256sum >/dev/null; then
    grep " $archive\$" SHA256SUMS | sha256sum -c -
  else
    grep " $archive\$" SHA256SUMS | shasum -a 256 -c -
  fi
)
tar -xzf "$tmp/$archive" -C "$tmp" vaultr
mkdir -p "$dir"
install -m 0755 "$tmp/vaultr" "$dir/vaultr"
echo "installed $("$dir/vaultr" version) to $dir/vaultr"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "note: $dir is not on your PATH" ;;
esac
