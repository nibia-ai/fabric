#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$ROOT/dist}"
if [[ -z "$OUT" || "$OUT" == "/" ]]; then
  echo "refusing unsafe release output directory: '$OUT'" >&2
  exit 1
fi
TMP="$(mktemp -d "${TMPDIR:-/tmp}/nibia-release.XXXXXX")"
# Normalize archive metadata so repeated builds from identical source/toolchain
# produce identical release ZIP bytes. Override only when intentionally cutting
# a different release date.
PACKAGE_MTIME="${NIBIA_RELEASE_MTIME:-202609240000}"
trap 'rm -rf "$TMP"' EXIT

for cmd in go zip unzip; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "release packaging requires '$cmd'" >&2
    exit 1
  fi
done


if [[ "$(go env GOVERSION)" != "go1.23.2" ]]; then
  echo "release packaging requires Go 1.23.2; detected $(go env GOVERSION)" >&2
  exit 1
fi
if [[ "$(cd "$ROOT" && go list -m)" != "github.com/nibia-ai/fabric" ]]; then
  echo "unexpected Go module; release packaging must run from github.com/nibia-ai/fabric" >&2
  exit 1
fi

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "release packaging requires sha256sum or shasum" >&2
    exit 1
  fi
}

build_triplet() {
  local goos="$1" goarch="$2" package_os="$3" package_arch="$4" suffix="$5"
  local pkg="nibia-v0.7.0-alpha-${suffix}"
  local pkgdir="$TMP/$pkg"
  local ext=""
  if [[ "$goos" == "windows" ]]; then
    ext=".exe"
  fi

  mkdir -p "$pkgdir"
  for bin in nibia nibia-agent nibia-controller; do
    local cmdpath="./cmd/$bin"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -buildvcs=false -o "$pkgdir/${bin}${ext}" "$cmdpath"
  done

  cp "$ROOT/LICENSE" "$ROOT/NOTICE" "$pkgdir/"
  if [[ "$goos" == "windows" ]]; then
    cp "$ROOT/packaging/install.ps1" "$pkgdir/install.ps1"
    cp "$ROOT/packaging/uninstall.ps1" "$pkgdir/uninstall.ps1"
  else
    sed \
      -e "s|^EXPECTED_OS=.*$|EXPECTED_OS=\"$package_os\"|" \
      -e "s|^EXPECTED_ARCH=.*$|EXPECTED_ARCH=\"$package_arch\"|" \
      "$ROOT/packaging/install-unix.sh" > "$pkgdir/install.sh"
    cp "$ROOT/packaging/uninstall-unix.sh" "$pkgdir/uninstall.sh"
    chmod 0755 "$pkgdir/nibia" "$pkgdir/nibia-agent" "$pkgdir/nibia-controller" \
      "$pkgdir/install.sh" "$pkgdir/uninstall.sh"
  fi

  find "$pkgdir" -exec touch -t "$PACKAGE_MTIME" {} +
  touch -t "$PACKAGE_MTIME" "$pkgdir"

  if [[ "$goos" == "windows" ]]; then
    (cd "$TMP" && zip -X -q "$OUT/$pkg.zip" \
      "$pkg/" \
      "$pkg/nibia-controller.exe" \
      "$pkg/nibia-agent.exe" \
      "$pkg/NOTICE" \
      "$pkg/LICENSE" \
      "$pkg/nibia.exe" \
      "$pkg/install.ps1" \
      "$pkg/uninstall.ps1")
  else
    (cd "$TMP" && zip -X -q "$OUT/$pkg.zip" \
      "$pkg/" \
      "$pkg/NOTICE" \
      "$pkg/uninstall.sh" \
      "$pkg/LICENSE" \
      "$pkg/install.sh" \
      "$pkg/nibia" \
      "$pkg/nibia-agent" \
      "$pkg/nibia-controller")
  fi
  unzip -tq "$OUT/$pkg.zip" >/dev/null
}

rm -rf "$OUT"
mkdir -p "$OUT"
cd "$ROOT"

build_triplet darwin arm64 darwin arm64 macos-arm64
build_triplet linux amd64 linux amd64 linux-amd64
build_triplet windows amd64 windows amd64 windows-amd64

manifest="$OUT/NIBIA_v0.7.0-alpha_SHA256SUMS.txt"
: > "$manifest"
for asset in \
  nibia-v0.7.0-alpha-macos-arm64.zip \
  nibia-v0.7.0-alpha-linux-amd64.zip \
  nibia-v0.7.0-alpha-windows-amd64.zip; do
  printf '%s  %s\n' "$(hash_file "$OUT/$asset")" "$asset" >> "$manifest"
done

printf 'Release assets written to %s\n' "$OUT"
cat "$manifest"
