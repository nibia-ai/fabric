#!/usr/bin/env bash
set -euo pipefail

DEST="${NIBIA_BIN_DIR:-$HOME/.local/bin}"
PURGE=0
if [[ "${1:-}" == "--purge-state" ]]; then
  PURGE=1
elif [[ $# -gt 0 ]]; then
  echo "usage: ./uninstall.sh [--purge-state]" >&2
  exit 2
fi

if [[ -d "$DEST" ]]; then
  DEST="$(cd "$DEST" && pwd -P)"
fi
rm -f "$DEST/nibia" "$DEST/nibia-agent" "$DEST/nibia-controller"
echo "Removed NIBIA binaries from $DEST"

marker="# NIBIA user-local binaries"
path_line="export PATH=\"$DEST:\$PATH\""
for profile in "$HOME/.zprofile" "$HOME/.bashrc" "$HOME/.profile"; do
  [[ -f "$profile" ]] || continue
  if grep -Fqx "$marker" "$profile"; then
    tmp="$(mktemp "${TMPDIR:-/tmp}/nibia-profile.XXXXXX")"
    grep -Fvx -e "$marker" -e "$path_line" "$profile" > "$tmp" || true
    cat "$tmp" > "$profile"
    rm -f "$tmp"
    echo "Removed NIBIA PATH entry from $profile"
  fi
done

if [[ $PURGE -eq 1 ]]; then
  rm -rf "$HOME/.nibia"
  echo "Removed NIBIA state/runtime from $HOME/.nibia"
else
  echo "Preserved $HOME/.nibia (pairings, state, managed runtime)."
fi
