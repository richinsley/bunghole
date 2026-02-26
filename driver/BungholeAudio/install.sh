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

# ── Install HAL driver (root) ──

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

# ── Install clipboard agent (user-level) ──

CLIP_BIN_SRC="$SCRIPT_DIR/bunghole-vm-clipboard"
if [[ ! -f "$CLIP_BIN_SRC" ]]; then
    echo
    echo "note: bunghole-vm-clipboard not found, skipping clipboard agent install"
    exit 0
fi

REAL_USER="${SUDO_USER:-$(logname 2>/dev/null || echo "")}"
if [[ -z "$REAL_USER" ]]; then
    echo "warning: cannot determine real user, skipping clipboard agent install" >&2
    exit 0
fi

REAL_HOME="$(dscl . -read "/Users/$REAL_USER" NFSHomeDirectory | awk '{print $2}')"
CLIP_LABEL="com.bunghole.vmclipboard"
CLIP_APP_DIR="$REAL_HOME/Library/Application Support/bunghole/clipboard"
CLIP_BIN_DST="$CLIP_APP_DIR/bunghole-vm-clipboard"
CLIP_AGENT_DIR="$REAL_HOME/Library/LaunchAgents"
CLIP_PLIST="$CLIP_AGENT_DIR/$CLIP_LABEL.plist"
CLIP_LOG_OUT="$REAL_HOME/Library/Logs/bunghole-vm-clipboard.log"
CLIP_LOG_ERR="$REAL_HOME/Library/Logs/bunghole-vm-clipboard.err.log"

echo
echo "Installing clipboard agent for user $REAL_USER ..."

su "$REAL_USER" -c "mkdir -p '$CLIP_APP_DIR' '$CLIP_AGENT_DIR'"
install -m 0755 -o "$REAL_USER" "$CLIP_BIN_SRC" "$CLIP_BIN_DST"

# Generate LaunchAgent plist
cat > "$CLIP_PLIST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>$CLIP_LABEL</string>

    <key>ProgramArguments</key>
    <array>
        <string>$CLIP_BIN_DST</string>
    </array>

    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>

    <key>LimitLoadToSessionType</key>
    <array>
        <string>Aqua</string>
    </array>
    <key>ProcessType</key>
    <string>Background</string>

    <key>StandardOutPath</key>
    <string>$CLIP_LOG_OUT</string>
    <key>StandardErrorPath</key>
    <string>$CLIP_LOG_ERR</string>
</dict>
</plist>
PLIST
chown "$REAL_USER" "$CLIP_PLIST"

REAL_UID="$(id -u "$REAL_USER")"
DOMAIN="gui/$REAL_UID"

launchctl bootout "$DOMAIN/$CLIP_LABEL" >/dev/null 2>&1 || true
launchctl bootstrap "$DOMAIN" "$CLIP_PLIST"
launchctl kickstart -k "$DOMAIN/$CLIP_LABEL"

echo "Clipboard agent installed: $CLIP_BIN_DST"
echo "LaunchAgent: $CLIP_PLIST"
echo "Logs: $CLIP_LOG_OUT"
