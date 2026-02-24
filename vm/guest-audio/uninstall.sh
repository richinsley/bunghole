#!/usr/bin/env bash
set -euo pipefail

LABEL="com.bunghole.vmaudio"
APP_DIR="$HOME/Library/Application Support/bunghole-vm-audio"
AGENT_DIR="$HOME/Library/LaunchAgents"
PLIST_PATH="$AGENT_DIR/$LABEL.plist"
BIN_PATH="$APP_DIR/bunghole-vm-audio"
LIB_PATH="$APP_DIR/libopus.0.dylib"

UID_NOW="$(id -u)"
DOMAIN="gui/$UID_NOW"

launchctl bootout "$DOMAIN/$LABEL" >/dev/null 2>&1 || true
rm -f "$PLIST_PATH"
rm -f "$BIN_PATH" "$LIB_PATH"
rmdir "$APP_DIR" >/dev/null 2>&1 || true

echo "Removed LaunchAgent: $PLIST_PATH"
echo "Removed binary: $BIN_PATH"
echo "If needed, remove logs in ~/Library/Logs/bunghole-vm-audio*.log"
