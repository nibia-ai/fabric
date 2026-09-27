#!/usr/bin/env bash
set -euo pipefail

EXPECTED_OS="${NIBIA_EXPECTED_OS:-}"
EXPECTED_ARCH="${NIBIA_EXPECTED_ARCH:-}"
HERE="$(cd "$(dirname "$0")" && pwd)"
DEST="${NIBIA_BIN_DIR:-$HOME/.local/bin}"
SKIP_SETUP=0

if [[ "${1:-}" == "--skip-setup" ]]; then
  SKIP_SETUP=1
elif [[ $# -gt 0 ]]; then
  echo "usage: ./install.sh [--skip-setup]" >&2
  exit 2
fi

actual_os="$(uname -s)"
actual_arch="$(uname -m)"
case "$actual_os" in
  Darwin) norm_os="darwin" ;;
  Linux) norm_os="linux" ;;
  *) norm_os="$(printf '%s' "$actual_os" | tr '[:upper:]' '[:lower:]')" ;;
esac
case "$actual_arch" in
  arm64|aarch64) norm_arch="arm64" ;;
  x86_64|amd64) norm_arch="amd64" ;;
  *) norm_arch="$actual_arch" ;;
esac

if [[ -n "$EXPECTED_OS" && "$norm_os" != "$EXPECTED_OS" ]]; then
  echo "This package is for $EXPECTED_OS/$EXPECTED_ARCH; detected $norm_os/$norm_arch." >&2
  exit 1
fi
if [[ -n "$EXPECTED_ARCH" && "$norm_arch" != "$EXPECTED_ARCH" ]]; then
  echo "This package is for $EXPECTED_OS/$EXPECTED_ARCH; detected $norm_os/$norm_arch." >&2
  exit 1
fi

if [[ "$norm_os" == "darwin" ]] && command -v xattr >/dev/null 2>&1 && \
   xattr -p com.apple.quarantine "$HERE/nibia" >/dev/null 2>&1; then
  cat >&2 <<EOF
macOS quarantine is still attached to the NIBIA binaries.
Verify the downloaded release ZIP SHA-256 against the checksum published with the release first.
If the checksum matches, remove quarantine from this verified extracted directory and rerun the installer:
  xattr -dr com.apple.quarantine "$HERE"
  ./install.sh
EOF
  exit 1
fi

mkdir -p "$DEST"
DEST="$(cd "$DEST" && pwd -P)"
install -m 0755 "$HERE/nibia" "$DEST/nibia"
install -m 0755 "$HERE/nibia-agent" "$DEST/nibia-agent"
install -m 0755 "$HERE/nibia-controller" "$DEST/nibia-controller"

echo "NIBIA installed to $DEST"

path_changed=0
if [[ ":$PATH:" != *":$DEST:"* ]]; then
  shell_name="$(basename "${SHELL:-sh}")"
  case "$shell_name" in
    zsh) profile="$HOME/.zprofile" ;;
    bash) profile="$HOME/.bashrc" ;;
    *) profile="$HOME/.profile" ;;
  esac
  touch "$profile"
  marker="# NIBIA user-local binaries"
  path_line="export PATH=\"$DEST:\$PATH\""
  if ! grep -Fq "$marker" "$profile"; then
    {
      printf '\n%s\n' "$marker"
      printf '%s\n' "$path_line"
    } >> "$profile"
  elif ! grep -Fq "$path_line" "$profile"; then
    printf '%s\n' "$path_line" >> "$profile"
  fi
  path_changed=1
  echo "PATH persisted in $profile"
fi

if [[ $SKIP_SETUP -eq 0 ]]; then
  "$DEST/nibia" setup
  "$DEST/nibia" doctor --local
fi

echo
if [[ $path_changed -eq 1 ]]; then
  echo "Open a new terminal, or run this once in the current shell:"
  printf '  export PATH="%s:$PATH"\n' "$DEST"
  echo "Then run: nibia version"
else
  echo "Ready. Run: nibia version"
fi
