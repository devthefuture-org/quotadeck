#!/usr/bin/env bash
set -euo pipefail

binary="${1:-dist/quotadeck-desktop-linux-amd64}"
readelf_name="${READELF:-readelf}"

if [[ ! -x "$binary" ]]; then
  printf 'desktop binary is missing or not executable: %s\n' "$binary" >&2
  exit 1
fi
if ! readelf_bin="$(command -v "$readelf_name")"; then
  printf 'readelf is required to validate desktop linkage\n' >&2
  exit 1
fi

linkage="$({ "$readelf_bin" -lW "$binary"; "$readelf_bin" -dW "$binary"; } 2>/dev/null)"
if grep -Fq '/nix/store/' <<<"$linkage"; then
  printf 'refusing non-portable desktop binary: ELF linkage references /nix/store\n' >&2
  printf 'build through make desktop-build so CGO links against the host GTK/WebKit libraries\n' >&2
  exit 1
fi

printf 'Desktop linkage check: host-native ELF (no /nix/store references)\n'
