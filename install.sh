#!/bin/sh
# headroom installer.
#
#   curl -fsSL https://headroomcli.com/install.sh | sh
#
# This script downloads a release archive and the published checksums, verifies
# the archive against them, and only then installs. A pipe into a shell asks you
# to trust the source, so the least it can do is not trust the download.
#
# Environment:
#   HEADROOM_VERSION  tag to install (default: latest)
#   HEADROOM_BIN_DIR  install directory (default: /usr/local/bin, falling back
#                     to $HOME/.local/bin when that is not writable)
#   HEADROOM_NO_VERIFY=1  skip checksum verification. Do not.

set -eu

REPO="headroom-project/headroom"
VERSION="${HEADROOM_VERSION:-latest}"

say() { printf '%s\n' "$*"; }
err() { printf 'install: %s\n' "$*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

# --- what are we running on ------------------------------------------------

detect_platform() {
  os=$(uname -s 2>/dev/null || echo unknown)
  arch=$(uname -m 2>/dev/null || echo unknown)

  case "$os" in
    Linux)   os=linux ;;
    Darwin)  os=darwin ;;
    MINGW*|MSYS*|CYGWIN*)
      err "on Windows use the archive from https://github.com/$REPO/releases/latest, or: go install github.com/$REPO/cmd/headroom@latest" ;;
    *) err "unsupported operating system: $os" ;;
  esac

  case "$arch" in
    x86_64|amd64)  arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) err "unsupported architecture: $arch. Build from source: go install github.com/$REPO/cmd/headroom@latest" ;;
  esac

  PLATFORM="${os}_${arch}"
}

# --- download --------------------------------------------------------------

fetch() { # fetch URL DEST
  if have curl; then
    curl -fsSL --proto '=https' --tlsv1.2 -o "$2" "$1" || return 1
  elif have wget; then
    wget -q --https-only -O "$2" "$1" || return 1
  else
    err "neither curl nor wget is available"
  fi
}

# --- verify ----------------------------------------------------------------

sha256_of() { # sha256_of FILE
  if have sha256sum; then
    sha256sum "$1" | cut -d' ' -f1
  elif have shasum; then
    shasum -a 256 "$1" | cut -d' ' -f1
  elif have openssl; then
    openssl dgst -sha256 "$1" | awk '{print $NF}'
  else
    return 1
  fi
}

verify() { # verify ARCHIVE CHECKSUMS NAME
  if [ "${HEADROOM_NO_VERIFY:-0}" = "1" ]; then
    say "  ! checksum verification skipped by HEADROOM_NO_VERIFY"
    return 0
  fi

  expected=$(grep " $3\$" "$2" 2>/dev/null | cut -d' ' -f1 || true)
  [ -n "$expected" ] || err "no checksum published for $3"

  actual=$(sha256_of "$1") || err "no sha256 tool found (install coreutils, or set HEADROOM_NO_VERIFY=1 and accept the risk)"

  if [ "$expected" != "$actual" ]; then
    err "checksum mismatch for $3
  expected $expected
  got      $actual
Do not use this download."
  fi
  say "  ok checksum $actual"
}

# --- where does it go ------------------------------------------------------

pick_bin_dir() {
  if [ -n "${HEADROOM_BIN_DIR:-}" ]; then
    BIN_DIR="$HEADROOM_BIN_DIR"
  elif [ -w /usr/local/bin ] 2>/dev/null; then
    BIN_DIR=/usr/local/bin
  else
    BIN_DIR="$HOME/.local/bin"
  fi
  mkdir -p "$BIN_DIR" 2>/dev/null || err "cannot create $BIN_DIR"
  [ -w "$BIN_DIR" ] || err "$BIN_DIR is not writable. Set HEADROOM_BIN_DIR to somewhere you own."
}

# --- run -------------------------------------------------------------------

main() {
  detect_platform
  pick_bin_dir

  case "$os" in
    linux|darwin) ext=tar.gz ;;
  esac
  archive="headroom_${PLATFORM}.${ext}"

  if [ "$VERSION" = "latest" ]; then
    base="https://github.com/$REPO/releases/latest/download"
  else
    base="https://github.com/$REPO/releases/download/$VERSION"
  fi

  tmp=$(mktemp -d 2>/dev/null || mktemp -d -t headroom)
  trap 'rm -rf "$tmp"' EXIT INT TERM

  say "headroom installer"
  say "  platform  $PLATFORM"
  say "  version   $VERSION"
  say "  target    $BIN_DIR/headroom"
  say ""

  say "  downloading $archive"
  fetch "$base/$archive" "$tmp/$archive" || err "could not download $base/$archive"

  if [ "${HEADROOM_NO_VERIFY:-0}" != "1" ]; then
    fetch "$base/checksums.txt" "$tmp/checksums.txt" || err "could not download the checksums"
  fi
  verify "$tmp/$archive" "$tmp/checksums.txt" "$archive"

  tar -xzf "$tmp/$archive" -C "$tmp" || err "could not extract $archive"
  [ -f "$tmp/headroom" ] || err "the archive did not contain a headroom binary"

  chmod +x "$tmp/headroom"
  mv "$tmp/headroom" "$BIN_DIR/headroom" 2>/dev/null ||
    err "could not write $BIN_DIR/headroom"

  say ""
  say "  installed $BIN_DIR/headroom"

  case ":$PATH:" in
    *":$BIN_DIR:"*) ;;
    *)
      say ""
      say "  $BIN_DIR is not on your PATH. Add it:"
      say "    export PATH=\"$BIN_DIR:\$PATH\""
      ;;
  esac

  say ""
  say "  next:"
  say "    terraform plan -out=tfplan"
  say "    terraform show -json tfplan > plan.json"
  say "    headroom analyze plan.json"
  say ""
  say "  to check the signature over the checksums as well, see SECURITY.md."
}

main "$@"
