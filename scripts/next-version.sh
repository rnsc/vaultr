#!/usr/bin/env bash
# Print the next release tag for HEAD, or nothing if no release is due.
#
#   scripts/next-version.sh [auto|patch|minor|major]
#
# auto (default) reads the HEAD commit message (with squash merges that is
# the PR title and description):
#   [skip release]                         no release
#   [major], "BREAKING CHANGE", "feat!:"   major bump
#   [minor], a line starting "feat:"       minor bump
#   anything else                          patch bump
# The first release is v0.1.0. Nothing is printed if HEAD is already tagged.
set -euo pipefail

mode="${1:-auto}"

if git tag --points-at HEAD | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "HEAD is already released as $(git tag --points-at HEAD | head -1)" >&2
  exit 0
fi

msg="$(git log -1 --format=%B HEAD)"
if [[ "$mode" == auto ]]; then
  if grep -qiF '[skip release]' <<<"$msg"; then
    echo "commit asks to skip the release" >&2
    exit 0
  fi
  mode="patch"
  if grep -qF '[major]' <<<"$msg" || grep -qF 'BREAKING CHANGE' <<<"$msg" ||
    grep -qE '^[a-z]+(\([^)]*\))?!:' <<<"$msg"; then
    mode="major"
  elif grep -qF '[minor]' <<<"$msg" || grep -qE '^feat(\([^)]*\))?:' <<<"$msg"; then
    mode="minor"
  fi
fi

last="$(git tag --list 'v[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -1 || true)"
if [[ -z "$last" ]]; then
  echo "v0.1.0"
  exit 0
fi
IFS=. read -r major minor patch <<<"${last#v}"
case "$mode" in
  major) echo "v$((major + 1)).0.0" ;;
  minor) echo "v${major}.$((minor + 1)).0" ;;
  patch) echo "v${major}.${minor}.$((patch + 1))" ;;
  *) echo "unknown bump \"$mode\"" >&2; exit 2 ;;
esac
