#!/usr/bin/env bash
# make-release.sh — assemble the Windows release zip for GitHub Releases.
#
# Usage (from the torview/ directory, Go 1.24+, the local bin/tor present):
#   bash scripts/make-release.sh
#
# Output: dist/torview-windows-x64.zip + dist/torview-windows-x64.zip.sha256
# The zip contains: torview.exe, bin/tor/ (official daemon), README.md,
# LICENSE, SECURITY.md. No source, no tor_data/, no wv2_profile/.
set -euo pipefail
cd "$(dirname "$0")/.."

GO=${GO:-go}
[ -x /c/Users/blion/AppData/Local/go-toolchain/go/bin/go.exe ] && \
  GO=/c/Users/blion/AppData/Local/go-toolchain/go/bin/go.exe

echo "[*] Build torview.exe (windowsgui)"
"$GO" vet .
"$GO" test .
"$GO" build -trimpath -ldflags="-s -w -H windowsgui" -o dist/pkg/torview.exe .

echo "[*] Assemble package"
rm -rf dist/pkg/bin dist/pkg/tor_data dist/pkg/wv2_profile
mkdir -p dist/pkg/bin
cp -r bin/tor dist/pkg/bin/tor
cp README.md LICENSE SECURITY.md dist/pkg/
rm -f dist/torview-windows-x64.zip

echo "[*] Zip"
cd dist/pkg && zip -q -r ../torview-windows-x64.zip . && cd ../..
sha256sum dist/torview-windows-x64.zip | tee dist/torview-windows-x64.zip.sha256
rm -rf dist/pkg
echo "[+] Ready: dist/torview-windows-x64.zip"
