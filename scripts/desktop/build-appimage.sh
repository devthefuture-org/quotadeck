#!/usr/bin/env bash
set -euo pipefail

architecture="${1:-amd64}"
version="${VERSION:-0.1.0}"
linuxdeploy_version="1-alpha-20251107-1"
appimagetool_version="1.9.1"
runtime_version="20251108"
gtk_plugin_commit="3b67a1d1c1b0c8268f57f2bce40fe2d33d409cea"
gtk_plugin_sha256="b0f4cbc684a0103a9651f0955b635eaea0096b3a66c0f5a2c2aa337960375171"
project_root="$(cd "$(dirname "$0")/../.." && pwd)"
binary="$project_root/dist/quotadeck-desktop-linux-${architecture}"
appdir="$project_root/dist/QuotaDeck.AppDir"
trap 'rm -rf "$appdir"' EXIT

case "$architecture" in
  amd64)
    appimage_arch="x86_64"
    helper_dir="/usr/lib/x86_64-linux-gnu/webkit2gtk-4.1"
    linuxdeploy_sha256="c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d"
    appimagetool_sha256="ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0"
    runtime_sha256="2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d"
    ;;
  arm64)
    appimage_arch="aarch64"
    helper_dir="/usr/lib/aarch64-linux-gnu/webkit2gtk-4.1"
    linuxdeploy_sha256="620095110d693282b8ebeb244a95b5e911cf8f65f76c88b4b47d16ae6346fcff"
    appimagetool_sha256="f0837e7448a0c1e4e650a93bb3e85802546e60654ef287576f46c71c126a9158"
    runtime_sha256="00cbdfcf917cc6c0ff6d3347d59e0ca1f7f45a6df1a428a0d6d8a78664d87444"
    ;;
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

cache_dir="${XDG_CACHE_HOME:-$HOME/.cache}/quotadeck-appimage-${appimage_arch}-${linuxdeploy_version}-${appimagetool_version}"
mkdir -p "$cache_dir"
linuxdeploy="$cache_dir/linuxdeploy"
gtk_plugin="$cache_dir/linuxdeploy-plugin-gtk"
appimagetool="$cache_dir/appimagetool"
runtime="$cache_dir/runtime-${runtime_version}-${appimage_arch}"

download_verified() {
  local url="$1"
  local destination="$2"
  local expected_sha256="$3"
  local temporary="${destination}.download"
  if [[ -f "$destination" ]] && echo "${expected_sha256}  ${destination}" | sha256sum --check --status; then
    chmod +x "$destination"
    return
  fi
  rm -f "$destination" "$temporary"
  curl -fsSL -o "$temporary" "$url"
  echo "${expected_sha256}  ${temporary}" | sha256sum --check
  chmod +x "$temporary"
  mv "$temporary" "$destination"
}

download_verified \
  "https://github.com/linuxdeploy/linuxdeploy/releases/download/${linuxdeploy_version}/linuxdeploy-${appimage_arch}.AppImage" \
  "$linuxdeploy" \
  "$linuxdeploy_sha256"
download_verified \
  "https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/${gtk_plugin_commit}/linuxdeploy-plugin-gtk.sh" \
  "$gtk_plugin" \
  "$gtk_plugin_sha256"
download_verified \
  "https://github.com/AppImage/appimagetool/releases/download/${appimagetool_version}/appimagetool-${appimage_arch}.AppImage" \
  "$appimagetool" \
  "$appimagetool_sha256"
download_verified \
  "https://github.com/AppImage/type2-runtime/releases/download/${runtime_version}/runtime-${appimage_arch}" \
  "$runtime" \
  "$runtime_sha256"

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
  --exclude-library='libcrypto.so*'

# The GTK plugin may add host-specific crypto libraries after linuxdeploy has
# evaluated the exclusions. Let the target system provide these paired libs.
find "$appdir" -type f \( \
  -name 'libgcrypt.so*' -o \
  -name 'libgpg-error.so*' -o \
  -name 'libssl.so*' -o \
  -name 'libcrypto.so*' \
\) -delete

rm -f "$OUTPUT"
ARCH="$appimage_arch" "$appimagetool" --appimage-extract-and-run --runtime-file "$runtime" "$appdir" "$OUTPUT"

echo "Built $OUTPUT"
