#!/bin/sh
# Ready (rd) install script
# Usage: curl -fsSL https://ready.3dl.dev/install.sh | sh
#
# Installs rd to ~/.local/bin

set -e

REPO="campfire-net/ready"
INSTALL_DIR="${HOME}/.local/bin"

# Colors (only if terminal supports them)
if [ -t 1 ]; then
  RED='\033[0;31m'
  GREEN='\033[0;32m'
  YELLOW='\033[1;33m'
  BOLD='\033[1m'
  RESET='\033[0m'
else
  RED=''
  GREEN=''
  YELLOW=''
  BOLD=''
  RESET=''
fi

info()    { printf "${BOLD}%s${RESET}\n" "$1"; }
success() { printf "${GREEN}%s${RESET}\n" "$1"; }
warn()    { printf "${YELLOW}%s${RESET}\n" "$1" >&2; }
die()     { printf "${RED}error: %s${RESET}\n" "$1" >&2; exit 1; }

detect_os() {
  case "$(uname -s)" in
    Linux*)  echo "linux" ;;
    Darwin*) echo "darwin" ;;
    *)       die "Unsupported OS: $(uname -s). Download manually from https://github.com/${REPO}/releases" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "amd64" ;;
    aarch64|arm64) echo "arm64" ;;
    *) die "Unsupported architecture: $(uname -m). Download manually from https://github.com/${REPO}/releases" ;;
  esac
}

check_deps() {
  for cmd in curl tar sha256sum; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      if [ "$cmd" = "sha256sum" ] && command -v shasum >/dev/null 2>&1; then
        continue
      fi
      die "Required tool not found: $cmd"
    fi
  done
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

get_latest_version() {
  local url="https://api.github.com/repos/${REPO}/releases/latest"
  local version

  if command -v curl >/dev/null 2>&1; then
    version=$(curl -fsSL "$url" | grep '"tag_name"' | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  fi

  if [ -z "$version" ]; then
    die "Could not determine latest version. Check your internet connection or visit https://github.com/${REPO}/releases"
  fi

  echo "$version"
}

main() {
  info "Ready (rd) installer"
  printf "\n"

  check_deps

  OS=$(detect_os)
  ARCH=$(detect_arch)

  info "Detecting platform..."
  printf "  OS:   %s\n" "$OS"
  printf "  Arch: %s\n" "$ARCH"
  printf "\n"

  info "Finding latest release..."
  VERSION=$(get_latest_version)
  printf "  Version: %s\n" "$VERSION"
  printf "\n"

  LABEL="${OS}_${ARCH}"
  ARCHIVE="rd_${LABEL}.tar.gz"
  BASE_URL="https://github.com/${REPO}/releases/download/${VERSION}"
  ARCHIVE_URL="${BASE_URL}/${ARCHIVE}"
  CHECKSUMS_URL="${BASE_URL}/checksums.txt"

  TMP_DIR=$(mktemp -d)
  trap 'rm -rf "$TMP_DIR"' EXIT

  info "Downloading..."
  printf "  %s\n" "$ARCHIVE_URL"

  if ! curl -fsSL --progress-bar -o "${TMP_DIR}/${ARCHIVE}" "$ARCHIVE_URL"; then
    die "Download failed. Check that version ${VERSION} has a release for ${LABEL}.\nVisit https://github.com/${REPO}/releases"
  fi

  # Download checksums and signature
  CHECKSUMS_SIG_URL="${BASE_URL}/checksums.txt.sig"
  CHECKSUMS_PEM_URL="${BASE_URL}/checksums.txt.pem"

  printf "  checksums.txt\n"
  if ! curl -fsSL -o "${TMP_DIR}/checksums.txt" "$CHECKSUMS_URL"; then
    warn "Could not download checksums — skipping verification"
  else
    printf "\n"
    info "Verifying checksum..."
    EXPECTED=$(grep "${ARCHIVE}" "${TMP_DIR}/checksums.txt" | awk '{print $1}')
    if [ -z "$EXPECTED" ]; then
      warn "Checksum entry not found for ${ARCHIVE} — skipping verification"
    else
      ACTUAL=$(sha256_file "${TMP_DIR}/${ARCHIVE}")
      if [ "$ACTUAL" != "$EXPECTED" ]; then
        die "Checksum mismatch!\n  expected: ${EXPECTED}\n  got:      ${ACTUAL}"
      fi
      success "  Checksum OK"
    fi

    # Verify cosign signature if cosign is available
    if command -v cosign >/dev/null 2>&1; then
      info "Verifying signature..."
      if curl -fsSL -o "${TMP_DIR}/checksums.txt.sig" "$CHECKSUMS_SIG_URL" 2>/dev/null \
         && curl -fsSL -o "${TMP_DIR}/checksums.txt.pem" "$CHECKSUMS_PEM_URL" 2>/dev/null; then
        if cosign verify-blob \
             --certificate "${TMP_DIR}/checksums.txt.pem" \
             --signature "${TMP_DIR}/checksums.txt.sig" \
             --certificate-identity-regexp "https://github.com/campfire-net/ready/" \
             --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
             "${TMP_DIR}/checksums.txt" 2>/dev/null; then
          success "  Signature OK (cosign, GitHub Actions OIDC)"
        else
          warn "Signature verification failed — binary may not be from official CI"
        fi
      else
        warn "Could not download signature files — skipping signature verification"
      fi
    fi
  fi

  printf "\n"
  info "Extracting..."

  tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "${TMP_DIR}"

  RD_BIN="${TMP_DIR}/rd_${LABEL}/rd"

  if [ ! -f "$RD_BIN" ]; then
    die "rd binary not found in archive. Unexpected archive layout."
  fi

  printf "\n"
  info "Installing to ${INSTALL_DIR}..."
  mkdir -p "$INSTALL_DIR"

  cp "$RD_BIN" "${INSTALL_DIR}/rd"
  chmod +x "${INSTALL_DIR}/rd"

  success "  rd → ${INSTALL_DIR}/rd"

  # PATH advice
  printf "\n"
  case ":${PATH}:" in
    *":${INSTALL_DIR}:"*)
      success "Done! ${INSTALL_DIR} is already in your PATH."
      ;;
    *)
      warn "${INSTALL_DIR} is not in your PATH."
      printf "\nAdd it by running:\n\n"
      printf "  ${BOLD}echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.profile${RESET}\n"
      printf "  ${BOLD}source ~/.profile${RESET}\n"
      printf "\nOr for zsh:\n\n"
      printf "  ${BOLD}echo 'export PATH=\"\$HOME/.local/bin:\$PATH\"' >> ~/.zshrc${RESET}\n"
      printf "  ${BOLD}source ~/.zshrc${RESET}\n"
      ;;
  esac

  info "Next steps:"
  printf "\n"
  printf "  rd init --name myproject       # initialize a project\n"
  printf "  rd create --title \"First task\" # create an item\n"
  printf "  rd ready                       # what needs attention?\n"
  printf "\n"
  printf "  Docs: https://ready.3dl.dev\n"
  printf "  Source: https://github.com/campfire-net/ready\n"
  printf "\n"
}

main "$@"
