#!/usr/bin/env bash
set -euo pipefail

LABEL="com.bunghole.vmaudio"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
APP_DIR="$HOME/Library/Application Support/bunghole-vm-audio"
AGENT_DIR="$HOME/Library/LaunchAgents"
PLIST_PATH="$AGENT_DIR/$LABEL.plist"
BIN_SRC="$SCRIPT_DIR/bunghole-vm-audio"
BIN_DST="$APP_DIR/bunghole-vm-audio"
TEMPLATE="$SCRIPT_DIR/$LABEL.plist.template"

UDP_DEST="${BUNGHOLE_VM_AUDIO_UDP:-}"
STATS_INTERVAL="${BUNGHOLE_VM_AUDIO_STATS_INTERVAL:-5s}"
SKIP_PROBE="${BUNGHOLE_VM_AUDIO_SKIP_PROBE:-0}"
LOG_OUT="$HOME/Library/Logs/bunghole-vm-audio.log"
LOG_ERR="$HOME/Library/Logs/bunghole-vm-audio.err.log"

if [[ -z "$UDP_DEST" && -f "$SCRIPT_DIR/udp_target.txt" ]]; then
    UDP_DEST="$(tr -d '\r\n' < "$SCRIPT_DIR/udp_target.txt")"
fi

if [[ ! -f "$BIN_SRC" ]]; then
    echo "error: missing binary: $BIN_SRC" >&2
    exit 1
fi

if [[ ! -f "$TEMPLATE" ]]; then
    echo "error: missing plist template: $TEMPLATE" >&2
    exit 1
fi

mkdir -p "$APP_DIR" "$AGENT_DIR" "$HOME/Library/Logs"
install -m 0755 "$BIN_SRC" "$BIN_DST"
if [[ -f "$SCRIPT_DIR/libopus.0.dylib" ]]; then
    install -m 0644 "$SCRIPT_DIR/libopus.0.dylib" "$APP_DIR/libopus.0.dylib"
fi

if [[ "$SKIP_PROBE" != "1" ]]; then
    echo "Checking Screen Recording permission..."
    if ! "$BIN_DST" --probe-permission --stats=false >>"$LOG_OUT" 2>>"$LOG_ERR"; then
        echo "Screen Recording permission is not ready for $BIN_DST." >&2
        echo "Open System Settings → Privacy & Security → Screen Recording, enable bunghole-vm-audio, then rerun ./install.sh." >&2
        echo "Tip: if you just granted it, fully quit Terminal and run install.sh again." >&2
        exit 1
    fi
fi

# Determine transport arguments
TRANSPORT_ARGS=""
if [[ -n "$UDP_DEST" ]]; then
    TRANSPORT_ARGS="        <string>--transport=udp</string>
        <string>--udp=$UDP_DEST</string>"
else
    TRANSPORT_ARGS="        <string>--transport=auto</string>"
fi

sed \
    -e "s|__PROGRAM_PATH__|$BIN_DST|g" \
    -e "s|__STATS_INTERVAL__|$STATS_INTERVAL|g" \
    -e "s|__TRANSPORT_ARGS__|$TRANSPORT_ARGS|g" \
    -e "s|__STDOUT_PATH__|$LOG_OUT|g" \
    -e "s|__STDERR_PATH__|$LOG_ERR|g" \
    "$TEMPLATE" > "$PLIST_PATH"

UID_NOW="$(id -u)"
DOMAIN="gui/$UID_NOW"

launchctl bootout "$DOMAIN/$LABEL" >/dev/null 2>&1 || true
launchctl bootstrap "$DOMAIN" "$PLIST_PATH"
launchctl kickstart -k "$DOMAIN/$LABEL"

echo "Installed: $BIN_DST"
echo "LaunchAgent: $PLIST_PATH"
if [[ -n "$UDP_DEST" ]]; then
    echo "Transport: udp ($UDP_DEST)"
else
    echo "Transport: auto (vsock preferred, UDP fallback)"
fi
echo "stdout log: $LOG_OUT"
echo "stderr log: $LOG_ERR"
echo "status: launchctl print $DOMAIN/$LABEL"
