#!/usr/bin/env bash
set -euo pipefail

BIN_PATH="${1:-}"
OUT_DYLIB_PATH="${2:-}"
INSTALL_NAME_TOOL_BIN="${3:-install_name_tool}"

if [[ -z "$BIN_PATH" || -z "$OUT_DYLIB_PATH" ]]; then
  echo "usage: rebase-opus.sh <bin-path> <out-libopus-path> [install_name_tool]" >&2
  exit 1
fi

OPUS_DYLIB="$(otool -L "$BIN_PATH" | awk '/libopus\.0\.dylib/{print $1; exit}')"
if [[ -z "$OPUS_DYLIB" ]]; then
  echo "could not detect libopus dependency in $BIN_PATH" >&2
  exit 1
fi

if [[ "$OPUS_DYLIB" == @loader_path/* ]]; then
  OPUS_DYLIB="$(dirname "$BIN_PATH")/$(basename "$OPUS_DYLIB")"
fi

if [[ "$OPUS_DYLIB" != "$OUT_DYLIB_PATH" ]]; then
  cp -f "$OPUS_DYLIB" "$OUT_DYLIB_PATH"
fi
CURRENT_INSTALL_NAME="$(otool -L "$BIN_PATH" | awk '/libopus\.0\.dylib/{print $1; exit}')"
if [[ "$CURRENT_INSTALL_NAME" != "@loader_path/libopus.0.dylib" ]]; then
  "$INSTALL_NAME_TOOL_BIN" -change "$CURRENT_INSTALL_NAME" "@loader_path/libopus.0.dylib" "$BIN_PATH"
fi
