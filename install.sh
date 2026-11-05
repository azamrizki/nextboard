#!/bin/sh
# NextBoard installer.
#
#   curl -fsSL https://raw.githubusercontent.com/azamrizki/nextboard/main/install.sh | sh
#   curl -fsSL …/install.sh | sh -s -- --system        # pasang ke /usr/local/bin
#
# Env: NEXTBOARD_REPO (owner/nama), NEXTBOARD_VERSION (mis. v0.2.0, default latest).
set -eu

REPO="${NEXTBOARD_REPO:-azamrizki/nextboard}"
VERSION="${NEXTBOARD_VERSION:-latest}"
PREFIX="$HOME/.local/bin"
SYSTEM=0
SERVICE=1

for arg in "$@"; do
  case "$arg" in
    --system) SYSTEM=1; PREFIX=/usr/local/bin ;;
    --no-service) SERVICE=0 ;;
    --repo=*) REPO="${arg#--repo=}" ;;
    --version=*) VERSION="${arg#--version=}" ;;
    *) echo "opsi tidak dikenal: $arg" >&2; exit 2 ;;
  esac
done

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in linux|darwin) ;; *) echo "OS tidak didukung: $os" >&2; exit 1 ;; esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "arsitektur tidak didukung: $arch" >&2; exit 1 ;;
esac

need() { command -v "$1" >/dev/null 2>&1 || { echo "butuh '$1'" >&2; exit 1; }; }
need curl
need tar

if [ "$VERSION" = latest ]; then
  VERSION=$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$VERSION" ] || { echo "tidak bisa membaca rilis terbaru $REPO" >&2; exit 1; }
fi
ver=${VERSION#v}
archive="nextboard_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "Mengunduh $archive ($VERSION)…"
curl -fsSL "$base/$archive" -o "$tmp/$archive"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

expected=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || { echo "checksum $archive tidak ditemukan" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
fi
[ "$expected" = "$actual" ] || { echo "checksum SHA-256 tidak cocok!" >&2; exit 1; }
echo "Checksum OK."

tar -xzf "$tmp/$archive" -C "$tmp" nextboard

SUDO=""
if [ "$SYSTEM" = 1 ] && [ "$(id -u)" != 0 ]; then SUDO=sudo; fi
$SUDO mkdir -p "$PREFIX"
$SUDO install -m 0755 "$tmp/nextboard" "$PREFIX/nextboard"
echo "Terpasang: $PREFIX/nextboard"

case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *) echo "Catatan: tambahkan $PREFIX ke PATH (mis. di ~/.profile)." ;;
esac

if [ "$SERVICE" = 1 ]; then
  # Service berjalan sebagai user ini (bukan root), walau binary di /usr/local/bin.
  "$PREFIX/nextboard" install
fi
