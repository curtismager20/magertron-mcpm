#!/usr/bin/env bash
#
# Copy every mcpctl binary into the extension, under the names VS Code expects.
#
# ⚠ THE MAPPING IS THE WHOLE POINT. Go and Node name platforms differently, and
# the extension resolves its binary from `${process.platform}-${process.arch}`:
#
#   Go              Node            directory
#   darwin/arm64    darwin/arm64    darwin-arm64
#   darwin/amd64    darwin/x64      darwin-x64      ⚠ amd64 → x64
#   linux/amd64     linux/x64       linux-x64       ⚠ amd64 → x64
#   linux/arm64     linux/arm64     linux-arm64
#   windows/amd64   win32/x64       win32-x64       ⚠ windows → win32, .exe
#
# Get one of these wrong and the extension reports "no mcpctl binary bundled
# for <platform>" — which reads like a broken install rather than a packaging
# mistake, on a machine you do not have.
set -euo pipefail

DIST="$(cd "$(dirname "$0")/../mcpctl/dist" && pwd)"
HERE="$(cd "$(dirname "$0")" && pwd)"

copy() {  # copy <dist-name> <node-dir> <target-name>
    local src="$DIST/$1" dir="$HERE/bin/$2" name="$3"
    if [ ! -f "$src" ]; then
        echo "  MISSING $1 — run 'make dist' in mcpctl first" >&2
        return 1
    fi
    mkdir -p "$dir"
    cp "$src" "$dir/$name"
    chmod 755 "$dir/$name"
    echo "  $2/$name"
}

echo "bundling mcpctl binaries:"
copy mcpctl-darwin-arm64        darwin-arm64 mcpctl
copy mcpctl-darwin-amd64        darwin-x64   mcpctl
copy mcpctl-linux-amd64         linux-x64    mcpctl
copy mcpctl-linux-arm64         linux-arm64  mcpctl
copy mcpctl-windows-amd64.exe   win32-x64    mcpctl.exe
