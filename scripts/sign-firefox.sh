#!/usr/bin/env bash
# Sign the Firefox add-on for a release on addons.mozilla.org (AMO, as an
# unlisted add-on) and put it where the build embeds it:
#
#   scripts/sign-firefox.sh [--unsigned] vX.Y.Z
#
# writes extension/dist/vaultr.xpi. Needs WEB_EXT_API_KEY (the AMO JWT
# issuer) and WEB_EXT_API_SECRET, and npx for web-ext.
#
# --unsigned packages the add-on with web-ext build instead of signing
# it, and needs no key: CI rehearses a release with it, everything but
# the AMO call.
#
# AMO signs a version once: when it already has this one (a rerun of the
# release), the signed file is downloaded instead. Either way the result
# is checked against the files exported from this commit. Prereleases
# (vX.Y.Z-rc.1) get no signed add-on: Firefox versions are plain numbers,
# so the prerelease would take the version number of the final release.
set -euo pipefail

unsigned=""
if [[ "${1:-}" == --unsigned ]]; then
  unsigned=1
  shift
fi
tag="${1:?usage: scripts/sign-firefox.sh [--unsigned] vX.Y.Z}"
version="${tag#v}"
if [[ "$version" == *-* ]]; then
  echo "$tag is a prerelease: no signed Firefox add-on" >&2
  exit 0
fi
if [[ -z "$unsigned" && ( -z "${WEB_EXT_API_KEY:-}" || -z "${WEB_EXT_API_SECRET:-}" ) ]]; then
  echo "WEB_EXT_API_KEY and WEB_EXT_API_SECRET must be set (AMO API key)" >&2
  exit 1
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
out="$root/extension/dist/vaultr.xpi"
rm -f "$out"

go run . extension export --browser firefox --version "$tag" "$work/src"

if [[ -n "$unsigned" ]]; then
  npx --yes web-ext@10.8.0 build --source-dir "$work/src" --artifacts-dir "$work/signed"
  cp "$work"/signed/*.zip "$out"
elif npx --yes web-ext@10.8.0 sign --channel=unlisted \
  --source-dir "$work/src" --artifacts-dir "$work/signed" 2>&1 | tee "$work/sign.log"; then
  xpis=("$work"/signed/*.xpi)
  if [[ ${#xpis[@]} -ne 1 || ! -f "${xpis[0]}" ]]; then
    echo "web-ext signed, but left ${#xpis[@]} .xpi files in $work/signed" >&2
    exit 1
  fi
  cp "${xpis[0]}" "$out"
elif grep -qi 'already exists' "$work/sign.log"; then
  echo "AMO already signed $version: downloading that file" >&2
  go run ./tools/amo fetch -version "$version" -out "$out"
else
  exit 1
fi

go run ./tools/amo verify -xpi "$out" -dir "$work/src"
if [[ -n "$unsigned" ]]; then
  echo "unsigned Firefox add-on $version (rehearsal): $out" >&2
else
  echo "signed Firefox add-on $version: $out" >&2
fi
