#!/usr/bin/env sh
set -eu

REPO_SLUG="${LARK_REPO:-richardsondx/IronLark}"
VERSION="${LARK_VERSION:-latest}"
INSTALL_DIR="${LARK_INSTALL_DIR:-$HOME/.local/bin}"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
esac

if [ "$VERSION" = "latest" ]; then
  RELEASE_URL="https://github.com/${REPO_SLUG}/releases/latest/download/lark_${OS}_${ARCH}.tar.gz"
else
  RELEASE_URL="https://github.com/${REPO_SLUG}/releases/download/${VERSION}/lark_${OS}_${ARCH}.tar.gz"
fi

TMP_DIR="$(mktemp -d)"
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

mkdir -p "$INSTALL_DIR"

echo "Downloading ${RELEASE_URL}"
if ! curl -fsSL "$RELEASE_URL" -o "$TMP_DIR/lark.tar.gz"; then
  echo >&2
  echo >&2 "Failed to download release artifact from:"
  echo >&2 "  $RELEASE_URL"
  echo >&2
  echo >&2 "If this repository was renamed or moved, update LARK_REPO or publish a GitHub release first."
  echo >&2 "Expected asset name: lark_${OS}_${ARCH}.tar.gz"
  exit 1
fi
tar -xzf "$TMP_DIR/lark.tar.gz" -C "$TMP_DIR"

install "$TMP_DIR/lark" "$INSTALL_DIR/lark"
ln -sf "$INSTALL_DIR/lark" "$INSTALL_DIR/lk"

echo "Installed:"
echo "  $INSTALL_DIR/lark"
echo "  $INSTALL_DIR/lk"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    echo
    echo "If $INSTALL_DIR is not on your PATH yet, let IronLark add it for you after setup:"
    echo "  $INSTALL_DIR/lk init"
    echo "  $INSTALL_DIR/lk \"add export PATH=\\\"$INSTALL_DIR:\\\$PATH\\\" to my shell profile and apply it\""
    ;;
esac

echo
echo "Quick start:"
echo "  $INSTALL_DIR/lk init"
echo "  $INSTALL_DIR/lk version"
echo "  $INSTALL_DIR/lk update"
echo "  lk \"what can you help me do on this server?\""
