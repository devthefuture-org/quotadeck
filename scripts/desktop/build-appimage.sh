#!/usr/bin/env bash
set -euo pipefail

architecture="${1:-amd64}"
version="${VERSION:-0.1.0}"
project_root="$(cd "$(dirname "$0")/../.." && pwd)"
binary="$project_root/dist/quotadeck-desktop-linux-${architecture}"
appdir="$project_root/dist/QuotaDeck.AppDir"
trap 'rm -rf "$appdir"' EXIT

case "$architecture" in
  amd64) appimage_arch="x86_64"; helper_dir="/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1" ;;
  arm64) appimage_arch="aarch64"; helper_dir="/usr/lib/aarch64-linux-gnu/webkit2gtk-4.1" ;;
  *) echo "Unsupported architecture: $architecture" >&2; exit 1 ;;
esac

if [[ ! -x "$binary" ]]; then
  echo "Desktop binary not found: $binary" >&2
  exit 1
fi
if [[ ! -d "$helper_dir" ]]; then
  echo "WebKitGTK helper directory not found: $helper_dir" >&2
  exit 1
fi

rm -rf "$appdir"
mkdir -p \
  "$appdir/usr/bin" \
  "$appdir/usr/lib/webkit2gtk-4.1" \
  "$appdir/usr/share/applications" \
  "$appdir/usr/share/icons/hicolor/512x512/apps"
install -m 0755 "$binary" "$appdir/usr/bin/quotadeck-desktop"
install -m 0755 "$project_root/packaging/desktop/AppRun" "$appdir/AppRun"
install -m 0644 "$project_root/packaging/desktop/quotadeck.desktop" "$appdir/quotadeck.desktop"
install -m 0644 "$project_root/packaging/desktop/quotadeck.desktop" "$appdir/usr/share/applications/quotadeck.desktop"
install -m 0644 "$project_root/cmd/quotadeck-desktop/appicon.png" "$appdir/quotadeck.png"
install -m 0644 "$project_root/cmd/quotadeck-desktop/appicon.png" "$appdir/usr/share/icons/hicolor/512x512/apps/quotadeck.png"
cp -a "$helper_dir/." "$appdir/usr/lib/webkit2gtk-4.1/"

cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/quotadeck-appimage-${appimage_arch}"
mkdir -p "$cache_dir"
linuxdeploy="$cache_dir/linuxdeploy"
gtk_plugin="$cache_dir/linuxdeploy-plugin-gtk"
if [[ ! -x "$linuxdeploy" ]]; then
  curl -fsSL -o "$linuxdeploy" "https://github.com/linuxdeploy/linuxdeploy/releases/download/continuous/linuxdeploy-${appimage_arch}.AppImage"
  chmod +x "$linuxdeploy"
fi
if [[ ! -x "$gtk_plugin" ]]; then
  curl -fsSL -o "$gtk_plugin" "https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/3b67a1d1c1b0c8268f57f2bce40fe2d33d409cea/linuxdeploy-plugin-gtk.sh"
  chmod +x "$gtk_plugin"
fi

export PATH="$cache_dir:$PATH"
export OUTPUT="$project_root/dist/QuotaDeck-${version}-${appimage_arch}.AppImage"
ARCH="$appimage_arch" "$linuxdeploy" --appimage-extract-and-run \
  --appdir "$appdir" \
  --desktop-file "$appdir/quotadeck.desktop" \
  --icon-file "$appdir/quotadeck.png" \
  --plugin gtk \
  --exclude-library='libgcrypt.so*' \
  --exclude-library='libgpg-error.so*' \
  --exclude-library='libssl.so*' \
  --exclude-library='libcrypto.so*' \
  --output appimage

echo "Built $OUTPUT"
