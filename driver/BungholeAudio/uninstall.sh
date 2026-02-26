#!/usr/bin/env bash
set -euo pipefail

DRIVER_NAME="BungholeAudio.driver"
INSTALL_DIR="/Library/Audio/Plug-Ins/HAL"

if [[ $(id -u) -ne 0 ]]; then
    echo "This script requires sudo."
    exec sudo "$0" "$@"
fi

# ── Remove HAL driver ──

if [[ -d "$INSTALL_DIR/$DRIVER_NAME" ]]; then
    echo "Removing $INSTALL_DIR/$DRIVER_NAME ..."
    rm -rf "$INSTALL_DIR/$DRIVER_NAME"

    echo "Restarting coreaudiod ..."
    killall coreaudiod 2>/dev/null || true
    sleep 1

    echo "Bunghole audio devices have been removed."
else
    echo "$DRIVER_NAME is not installed."
fi

# ── Remove clipboard agent ──

REAL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo "")}"
if [[ -z "$REAL_USER" ]]; then
    exit 0
fi

REAL_HOME="$(dscl . -read "/Users/$REAL_USER" NFSHomeDirectory | awk '{print $2}')"
CLIP_LABEL="com.bunghole.vmclipboard"
CLIP_APP_DIR="$REAL_HOME/Library/Application Support/bunghole/clipboard"
CLIP_PLIST="$REAL_HOME/Library/LaunchAgents/$CLIP_LABEL.plist"

REAL_UID="$(id -u "$REAL_USER")"
DOMAIN="gui/$REAL_UID"

launchctl bootout "$DOMAIN/$CLIP_LABEL" >/dev/null 2>&1 || true
rm -f "$CLIP_PLIST"
rm -rf "$CLIP_APP_DIR"

echo "Clipboard agent removed."
