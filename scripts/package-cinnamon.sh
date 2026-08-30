#!/usr/bin/env bash
set -euo pipefail

version="${VERSION:-0.1.0}"
project_root="$(cd "$(dirname "$0")/.." && pwd)"
source_dir="$project_root/packaging/cinnamon/quotadeck@local"
output="$project_root/dist/quotadeck-cinnamon-applet_${version}.zip"

mkdir -p "$project_root/dist"
rm -f "$output"
(
  cd "$project_root/packaging/cinnamon"
  zip -qr "$output" quotadeck@local
)
echo "Built $output"
