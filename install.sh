#!/bin/bash
set -euo pipefail

# Forge installer/upgrader
# Usage: curl -sSL https://raw.githubusercontent.com/initializ/forge/main/install.sh | bash

REPO="initializ/forge"
INSTALL_DIR="/usr/local/bin"
BINARY="forge"

main() {
  detect_platform
  check_existing
  fetch_latest
  install_binary
  verify
}

detect_platform() {
  OS=$(uname -s)
  ARCH=$(uname -m)

  case "$OS" in
    Darwin|Linux) ;;
    *) echo "Error: unsupported OS: $OS"; exit 1 ;;
  esac

  # Map architecture to goreleaser naming
  case "$ARCH" in
    x86_64)  ARCH="x86_64" ;;
    aarch64) ARCH="arm64" ;;
    arm64)   ARCH="arm64" ;;
    *) echo "Error: unsupported architecture: $ARCH"; exit 1 ;;
  esac

  ASSET="forge-${OS}-${ARCH}.tar.gz"
}

check_existing() {
  if command -v "$BINARY" &>/dev/null; then
    CURRENT=$("$BINARY" --version 2>/dev/null | head -1 || echo "unknown")
    echo "Forge is already installed: $CURRENT"
    echo "Upgrading..."
  else
    echo "Installing Forge..."
  fi
}

fetch_latest() {
  URL="https://github.com/${REPO}/releases/latest/download/${ASSET}"
  TMPDIR=$(mktemp -d)
  trap 'rm -rf "$TMPDIR"' EXIT

  echo "Downloading $ASSET..."
  if ! curl -fsSL "$URL" -o "$TMPDIR/$ASSET"; then
    echo "Error: failed to download $URL"
    echo "Check https://github.com/${REPO}/releases for available assets."
    exit 1
  fi

  tar xzf "$TMPDIR/$ASSET" -C "$TMPDIR"

  if [ ! -f "$TMPDIR/$BINARY" ]; then
    echo "Error: $BINARY not found in archive"
    exit 1
  fi
}

install_binary() {
  if [ -w "$INSTALL_DIR" ]; then
    mv "$TMPDIR/$BINARY" "$INSTALL_DIR/$BINARY"
  else
    echo "Need sudo to install to $INSTALL_DIR"
    sudo mv "$TMPDIR/$BINARY" "$INSTALL_DIR/$BINARY"
  fi
  chmod +x "$INSTALL_DIR/$BINARY"
}

verify() {
  VERSION=$("$INSTALL_DIR/$BINARY" --version 2>/dev/null | head -1 || echo "installed")
  echo "Forge $VERSION"
  echo "Installed to $INSTALL_DIR/$BINARY"
}

main
