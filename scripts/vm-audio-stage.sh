#!/usr/bin/env bash
set -euo pipefail

usage() {
    cat <<USAGE
usage: $(basename "$0") --vm-share <path> [--bin <path>] [--udp-target <host:port>]

Stages VM guest audio agent files into:
  <vm-share>/.bunghole-vm-audio/
USAGE
}

VM_SHARE=""
BIN_PATH="./build/bunghole-vm-audio"
UDP_TARGET=""

while [[ $# -gt 0 ]]; do
    case "$1" in
        --vm-share)
            VM_SHARE="${2:-}"
            shift 2
            ;;
        --bin)
            BIN_PATH="${2:-}"
            shift 2
            ;;
        --udp-target)
            UDP_TARGET="${2:-}"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "error: unknown argument: $1" >&2
            usage
            exit 1
            ;;
    esac
done

if [[ -z "$VM_SHARE" ]]; then
    echo "error: --vm-share is required" >&2
    usage
    exit 1
fi

if [[ ! -d "$VM_SHARE" ]]; then
    echo "error: vm-share path does not exist: $VM_SHARE" >&2
    exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN_ABS="$BIN_PATH"
if [[ "$BIN_ABS" != /* ]]; then
    BIN_ABS="$REPO_ROOT/${BIN_PATH#./}"
fi

if [[ "$BIN_PATH" == "./build/bunghole-vm-audio" && ! -f "$BIN_ABS" ]]; then
    echo "Building bunghole-vm-audio via CMake target"
    (cd "$REPO_ROOT" && cmake --build build --target bunghole-vm-audio)
fi

if [[ ! -f "$BIN_ABS" ]]; then
    echo "error: binary not found: $BIN_ABS" >&2
    exit 1
fi

STAGE_DIR="$VM_SHARE/.bunghole-vm-audio"
mkdir -p "$STAGE_DIR"

cp -f "$BIN_ABS" "$STAGE_DIR/bunghole-vm-audio"

# Make guest binary self-contained for libopus (guest may not have Homebrew paths).
OPUS_DYLIB_HOST="$(otool -L "$BIN_ABS" | awk '/libopus\.0\.dylib/{print $1; exit}')"
if [[ -n "${OPUS_DYLIB_HOST}" ]]; then
    if [[ "${OPUS_DYLIB_HOST}" == @loader_path/* ]]; then
        OPUS_FROM_BIN_DIR="$(dirname "$BIN_ABS")/$(basename "$OPUS_DYLIB_HOST")"
        if [[ -f "${OPUS_FROM_BIN_DIR}" ]]; then
            cp -fL "$OPUS_FROM_BIN_DIR" "$STAGE_DIR/libopus.0.dylib"
        fi
    elif [[ -f "${OPUS_DYLIB_HOST}" ]]; then
        cp -fL "$OPUS_DYLIB_HOST" "$STAGE_DIR/libopus.0.dylib"
        install_name_tool -change "$OPUS_DYLIB_HOST" "@loader_path/libopus.0.dylib" "$STAGE_DIR/bunghole-vm-audio"
    fi
fi

cp -f "$REPO_ROOT/vm/guest-audio/install.sh" "$STAGE_DIR/install.sh"
cp -f "$REPO_ROOT/vm/guest-audio/uninstall.sh" "$STAGE_DIR/uninstall.sh"
cp -f "$REPO_ROOT/vm/guest-audio/com.bunghole.vmaudio.plist.template" "$STAGE_DIR/com.bunghole.vmaudio.plist.template"
if [[ -n "$UDP_TARGET" ]]; then
    printf "%s\n" "$UDP_TARGET" > "$STAGE_DIR/udp_target.txt"
else
    rm -f "$STAGE_DIR/udp_target.txt"
fi
chmod 0755 "$STAGE_DIR/bunghole-vm-audio" "$STAGE_DIR/install.sh" "$STAGE_DIR/uninstall.sh"
if [[ -f "$STAGE_DIR/libopus.0.dylib" ]]; then
    chmod 0644 "$STAGE_DIR/libopus.0.dylib"
fi

echo "Staged guest audio agent files to: $STAGE_DIR"
echo
echo "Inside the macOS guest, run:"
echo "  cd '/Volumes/My Shared Files/.bunghole-vm-audio'"
echo "  ./install.sh"
if [[ -n "$UDP_TARGET" ]]; then
    echo "(preconfigured UDP target: $UDP_TARGET)"
fi
echo
echo "Optional install-time overrides in guest:"
echo "  BUNGHOLE_VM_AUDIO_UDP=<host:port>"
echo "  BUNGHOLE_VM_AUDIO_STATS_INTERVAL=<duration>"
echo "  BUNGHOLE_VM_AUDIO_SKIP_PROBE=1"
