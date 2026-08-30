#!/usr/bin/env bash
set -euo pipefail

version="${VERSION:-0.1.0}"
architecture="${ARCH:-amd64}"
project_root="$(cd "$(dirname "$0")/.." && pwd)"
dist_dir="$project_root/dist"
stage_dir="$(mktemp -d)"
trap 'rm -rf "$stage_dir"' EXIT

if [[ ! "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "VERSION must use semantic versioning (for example 0.1.0): $version" >&2
  exit 1
fi
if [[ "$architecture" != "amd64" ]]; then
  echo "Unsupported release architecture: $architecture" >&2
  exit 1
fi

cli_binary="$dist_dir/quotadeck"
desktop_binary="$dist_dir/quotadeck-desktop-linux-${architecture}"
appimage="$dist_dir/QuotaDeck-${version}-x86_64.AppImage"
deb="$dist_dir/quotadeck_${version}_${architecture}.deb"
applet="$dist_dir/quotadeck-cinnamon-applet_${version}.zip"

for artifact in "$cli_binary" "$desktop_binary" "$appimage" "$deb" "$applet"; do
  if [[ ! -e "$artifact" ]]; then
    echo "Release artifact not found: $artifact" >&2
    exit 1
  fi
done
for binary in "$cli_binary" "$desktop_binary" "$appimage"; do
  if [[ ! -x "$binary" ]]; then
    echo "Release executable is not executable: $binary" >&2
    exit 1
  fi
done

cli_root="$stage_dir/quotadeck-${version}-linux-${architecture}"
desktop_root="$stage_dir/quotadeck-desktop-${version}-linux-${architecture}"
mkdir -p "$cli_root" "$desktop_root"

install -m 0755 "$cli_binary" "$cli_root/quotadeck"
install -m 0644 "$project_root/README.md" "$cli_root/README.md"
install -m 0644 "$project_root/LICENSE" "$cli_root/LICENSE"

install -m 0755 "$desktop_binary" "$desktop_root/quotadeck-desktop"
install -m 0644 "$project_root/packaging/desktop/quotadeck.desktop" "$desktop_root/quotadeck.desktop"
install -m 0644 "$project_root/packaging/desktop/quotadeck.svg" "$desktop_root/quotadeck.svg"
install -m 0644 "$project_root/cmd/quotadeck-desktop/appicon.png" "$desktop_root/quotadeck.png"
install -m 0644 "$project_root/LICENSE" "$desktop_root/LICENSE"
cat > "$desktop_root/README.txt" <<EOF
QuotaDeck Desktop ${version} for Linux ${architecture}

This archive contains the raw Wails desktop executable. It requires GTK 3,
WebKitGTK 4.1 and libsoup 3 on the host system. Prefer the Debian package or
AppImage from the same release when possible.

Run:
  ./quotadeck-desktop
EOF

cli_archive="$dist_dir/quotadeck-cli-linux-${architecture}.tar.gz"
desktop_archive="$dist_dir/quotadeck-desktop-linux-${architecture}.tar.gz"
tar -C "$stage_dir" -czf "$cli_archive" "$(basename "$cli_root")"
tar -C "$stage_dir" -czf "$desktop_archive" "$(basename "$desktop_root")"

checksums="$dist_dir/quotadeck_${version}_checksums.txt"
(
  cd "$dist_dir"
  sha256sum \
    "$(basename "$cli_archive")" \
    "$(basename "$desktop_archive")" \
    "$(basename "$appimage")" \
    "$(basename "$deb")" \
    "$(basename "$applet")" \
    | sort -k2
) > "$checksums"

echo "Built $cli_archive"
echo "Built $desktop_archive"
echo "Built $checksums"
