#!/usr/bin/env bash
set -euo pipefail

version="${VERSION:-0.1.0}"
architecture="${ARCH:-$(dpkg --print-architecture)}"
project_root="$(cd "$(dirname "$0")/.." && pwd)"
package_root="$(mktemp -d)"
trap 'rm -rf "$package_root"' EXIT

mkdir -p \
  "$package_root/DEBIAN" \
  "$package_root/usr/bin" \
  "$package_root/usr/lib/systemd/user" \
  "$package_root/usr/share/applications" \
  "$package_root/usr/share/icons/hicolor/scalable/apps" \
  "$package_root/usr/share/icons/hicolor/512x512/apps" \
  "$package_root/usr/share/cinnamon/applets/quotadeck@local" \
  "$package_root/usr/share/doc/quotadeck"
install -m 0755 "$project_root/dist/quotadeck" "$package_root/usr/bin/quotadeck"
install -m 0755 "$project_root/dist/quotadeck-desktop-linux-amd64" "$package_root/usr/bin/quotadeck-desktop"
install -m 0644 "$project_root/packaging/systemd/quotadeck.service" "$package_root/usr/lib/systemd/user/quotadeck.service"
install -m 0644 "$project_root/packaging/desktop/quotadeck.desktop" "$package_root/usr/share/applications/quotadeck.desktop"
install -m 0644 "$project_root/packaging/desktop/quotadeck.svg" "$package_root/usr/share/icons/hicolor/scalable/apps/quotadeck.svg"
install -m 0644 "$project_root/cmd/quotadeck-desktop/appicon.png" "$package_root/usr/share/icons/hicolor/512x512/apps/quotadeck.png"
install -m 0644 "$project_root/packaging/cinnamon/quotadeck@local/"* "$package_root/usr/share/cinnamon/applets/quotadeck@local/"
install -m 0644 "$project_root/LICENSE" "$package_root/usr/share/doc/quotadeck/copyright"

cat > "$package_root/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true
exit 0
EOF
chmod 0755 "$package_root/DEBIAN/postinst"

cat > "$package_root/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e
command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database -q /usr/share/applications || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -q -t -f /usr/share/icons/hicolor || true
exit 0
EOF
chmod 0755 "$package_root/DEBIAN/postrm"

installed_size="$(du -sk "$package_root/usr" | cut -f1)"

printf '%s\n' \
  'Package: quotadeck' \
  "Version: $version" \
  "Architecture: $architecture" \
  'Maintainer: QuotaDeck contributors' \
  'Section: utils' \
  'Priority: optional' \
  "Installed-Size: $installed_size" \
  'Depends: libgtk-3-0 | libgtk-3-0t64, libwebkit2gtk-4.1-0, libsoup-3.0-0' \
  'Description: Local multi-provider AI quota dashboard' \
  ' Includes the CLI service, Wails desktop application, and Cinnamon applet.' \
  > "$package_root/DEBIAN/control"

mkdir -p "$project_root/dist"
dpkg-deb --build --root-owner-group "$package_root" "$project_root/dist/quotadeck_${version}_${architecture}.deb"
