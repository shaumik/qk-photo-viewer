#!/bin/sh
# Install or update QK without the Gatekeeper warning:
#
#   curl -fsSL https://raw.githubusercontent.com/shaumik/qk-photo-viewer/main/install.sh | sh
#
# Terminal downloads carry no quarantine flag, so the app opens like any
# other. Safe to re-run any time — it always fetches the latest release.
set -e

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading the latest QK release..."
curl -fsSL -o "$tmp/qk.zip" \
  https://github.com/shaumik/qk-photo-viewer/releases/latest/download/QK-macos.zip
ditto -x -k "$tmp/qk.zip" "$tmp"

rm -rf /Applications/QK.app
ditto "$tmp/QK.app" /Applications/QK.app

echo "Done — QK is in /Applications. No 'unverified developer' prompt."
