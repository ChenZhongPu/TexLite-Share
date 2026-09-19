#!/usr/bin/env bash
#
# TexLite Share Tunnel Client - One-Line Installer
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/ChenZhongPu/TexLite-Share/main/install-client.sh | bash
#
# Custom options:
#   BIN_DIR=~/.local/bin curl -fsSL ... | bash       # Custom install directory (default: ~/.local/bin)
#   VERSION=v0.1.9 curl -fsSL ... | bash             # Specific version (default: latest)
#
set -euo pipefail

REPO="ChenZhongPu/TexLite-Share"
BINARY_NAME="texlite-tunnel-client"
INSTALL_DIR="${BIN_DIR:-"$HOME/.local/bin"}"

# Color output helpers
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m' # No Color

info() {
  echo -e "${BLUE}==>${NC} ${BOLD}$1${NC}"
}

success() {
  echo -e "${GREEN}==>${NC} ${BOLD}$1${NC}"
}

warn() {
  echo -e "${YELLOW}Warning:${NC} $1"
}

error() {
  echo -e "${RED}Error:${NC} $1" >&2
  exit 1
}

# 1. Check required tools
command -v curl >/dev/null 2>&1 || error "curl is required but not installed."
command -v tar >/dev/null 2>&1 || error "tar is required but not installed."

# 2. Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux)
    TARGET_OS="linux"
    ;;
  darwin)
    TARGET_OS="darwin"
    ;;
  *)
    error "Unsupported operating system: $OS. texlite-tunnel-client currently supports Linux and macOS via this script."
    ;;
esac

# 3. Detect Architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)
    TARGET_ARCH="amd64"
    ;;
  aarch64|arm64)
    TARGET_ARCH="arm64"
    ;;
  *)
    error "Unsupported architecture: $ARCH (supported: amd64, arm64)."
    ;;
esac

info "Detected platform: ${TARGET_OS}/${TARGET_ARCH}"

# 4. Resolve version to install
TARGET_TAG="${VERSION:-}"
if [ -z "$TARGET_TAG" ]; then
  info "Fetching latest release information from GitHub..."
  # Try resolving latest tag from redirect header without hitting GitHub API rate limits
  REDIRECT_URL="$(curl -fsSLI -o /dev/null -w "%{url_effective}" "https://github.com/${REPO}/releases/latest" 2>/dev/null || true)"
  TARGET_TAG="${REDIRECT_URL##*/}"

  # Fallback to GitHub API if redirect did not return a tag
  if [ -z "$TARGET_TAG" ] || [ "$TARGET_TAG" = "latest" ]; then
    TARGET_TAG="$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' || true)"
  fi
fi

if [ -z "$TARGET_TAG" ] || [ "$TARGET_TAG" = "latest" ]; then
  error "Could not determine the latest release version. Please specify manually via: VERSION=v0.1.9 curl ... | bash"
fi

# Strip leading 'v' for the filename (e.g. v0.1.9 -> 0.1.9)
CLEAN_VERSION="${TARGET_TAG#v}"
ARCHIVE_NAME="texlite-share_${CLEAN_VERSION}_${TARGET_OS}_${TARGET_ARCH}.tar.gz"
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${TARGET_TAG}/${ARCHIVE_NAME}"

info "Selected version: ${TARGET_TAG}"
info "Downloading from: ${DOWNLOAD_URL}"

# 5. Create temporary working directory
TMP_DIR="$(mktemp -d -t texlite-install-XXXXXX)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

# 6. Download release archive
HTTP_CODE="$(curl -fSL --progress-bar -w "%{http_code}" -o "$TMP_DIR/$ARCHIVE_NAME" "$DOWNLOAD_URL" || true)"
if [ "$HTTP_CODE" != "200" ] && [ ! -s "$TMP_DIR/$ARCHIVE_NAME" ]; then
  error "Failed to download $ARCHIVE_NAME (HTTP $HTTP_CODE). Please check if release $TARGET_TAG exists."
fi

# 7. Extract client binary
info "Extracting ${BINARY_NAME}..."
tar -xzf "$TMP_DIR/$ARCHIVE_NAME" -C "$TMP_DIR" "$BINARY_NAME" || error "Failed to extract $BINARY_NAME from archive."

# 8. Install to target directory
mkdir -p "$INSTALL_DIR"
mv "$TMP_DIR/$BINARY_NAME" "$INSTALL_DIR/$BINARY_NAME"
chmod +x "$INSTALL_DIR/$BINARY_NAME"

success "Successfully installed ${BINARY_NAME} to ${INSTALL_DIR}/${BINARY_NAME}"

# 9. Verify installation
if [ -x "$INSTALL_DIR/$BINARY_NAME" ]; then
  VERSION_INFO="$("$INSTALL_DIR/$BINARY_NAME" -v 2>/dev/null || true)"
  if [ -n "$VERSION_INFO" ]; then
    echo -e "   ${BOLD}${VERSION_INFO}${NC}\n"
  fi
fi

# 10. Check PATH environment variable
PATH_OK=false
case ":$PATH:" in
  *":$INSTALL_DIR:"*)
    PATH_OK=true
    ;;
esac

if [ "$PATH_OK" = false ]; then
  echo -e "${YELLOW}------------------------------------------------------------------${NC}"
  echo -e "${YELLOW}Notice:${NC} '${INSTALL_DIR}' is not in your current PATH."
  echo -e "To run '${BINARY_NAME}' directly, add the following line to your shell profile:"
  echo
  if [ -n "${ZSH_VERSION:-}" ] || [ "${SHELL:-}" = "*/zsh" ]; then
    echo -e "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.zshrc && source ~/.zshrc"
  else
    echo -e "  echo 'export PATH=\"${INSTALL_DIR}:\$PATH\"' >> ~/.bashrc && source ~/.bashrc"
  fi
  echo -e "${YELLOW}------------------------------------------------------------------${NC}\n"
fi

# 11. Print usage instructions
echo -e "${GREEN}==================================================================${NC}"
echo -e "${BOLD} 🚀 TexLite Tunnel Client is ready to use!${NC}"
echo -e "${GREEN}==================================================================${NC}"
echo
echo -e "${BOLD}1. Direct Start (Automatic Temporary Share):${NC}"
echo -e "   Run TexLite locally (e.g. port 3000), then launch:"
echo -e "   ${BLUE}texlite-tunnel-client --server-url https://share.yourdomain.com${NC}"
echo -e "   *(Generates a temporary share URL and automatically cleans up upon exit)*"
echo
echo -e "${BOLD}2. Fixed Subdomain Mode (Pre-configured Share):${NC}"
echo -e "   ${BLUE}texlite-tunnel-client --server-url https://share.yourdomain.com \\${NC}"
echo -e "       ${BLUE}--share-id <YOUR_SHARE_ID> --token <YOUR_TOKEN>${NC}"
echo
echo -e "${BOLD}3. View all available options:${NC}"
echo -e "   ${BLUE}texlite-tunnel-client --help${NC}"
echo -e "${GREEN}==================================================================${NC}"
