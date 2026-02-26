#!/usr/bin/env bash
set -euo pipefail

DRIVER_NAME="BungholeAudio.driver"
INSTALL_DIR="/Library/Audio/Plug-Ins/HAL"

if [[ $(id -u) -ne 0 ]]; then
    echo "This script requires sudo."
    exec sudo "$0" "$@"
fi

if [[ ! -d "$INSTALL_DIR/$DRIVER_NAME" ]]; then
    echo "$DRIVER_NAME is not installed."
    exit 0
fi

echo "Removing $INSTALL_DIR/$DRIVER_NAME ..."
rm -rf "$INSTALL_DIR/$DRIVER_NAME"

echo "Restarting coreaudiod ..."
killall coreaudiod 2>/dev/null || true
sleep 1

echo "Done. Bunghole audio devices have been removed."
