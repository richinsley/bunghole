#!/usr/bin/env bash
set -euo pipefail

DRIVER_NAME="BungholeAudio.driver"
INSTALL_DIR="/Library/Audio/Plug-Ins/HAL"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DRIVER_SRC="$SCRIPT_DIR/$DRIVER_NAME"

if [[ ! -d "$DRIVER_SRC" ]]; then
    echo "error: driver bundle not found: $DRIVER_SRC" >&2
    exit 1
fi

if [[ $(id -u) -ne 0 ]]; then
    echo "This script requires sudo."
    exec sudo "$0" "$@"
fi

echo "Installing $DRIVER_NAME to $INSTALL_DIR ..."

mkdir -p "$INSTALL_DIR"
rm -rf "$INSTALL_DIR/$DRIVER_NAME"
cp -R "$DRIVER_SRC" "$INSTALL_DIR/$DRIVER_NAME"
chown -R root:wheel "$INSTALL_DIR/$DRIVER_NAME"

echo "Removing quarantine attributes ..."
xattr -dr com.apple.quarantine "$INSTALL_DIR/$DRIVER_NAME" 2>/dev/null || true

echo "Re-codesigning bundle (ad-hoc) ..."
if ! codesign --force --sign - --deep "$INSTALL_DIR/$DRIVER_NAME"; then
    echo "warning: codesign failed — coreaudiod may reject the plugin" >&2
fi

echo "Restarting coreaudiod ..."
killall coreaudiod 2>/dev/null || true
sleep 1

echo
echo "Done. \"Bunghole Output\" and \"Bunghole Input\" should now appear in:"
echo "  System Settings → Sound → Output / Input"
echo
echo "To set as default output from the command line:"
echo "  # Install switchaudio-osx (brew install switchaudio-osx), then:"
echo "  SwitchAudioSource -s 'Bunghole Output'"
