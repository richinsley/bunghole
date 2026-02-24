# Guest Config

The server exposes `GET /config` which returns a JSON object describing the guest platform. The web client fetches this on connect and uses it to adapt cursor rendering, key remapping, and clipboard behavior.

## Schema

```json
{
  "guest": {
    "os": "linux",
    "type": "desktop",
    "cursor": true,
    "clipboard": true
  }
}
```

### Fields

| Field | Type | Values | Description |
|-------|------|--------|-------------|
| `guest.os` | string | `"linux"`, `"macos"` | Guest operating system. Determines key remap rules. |
| `guest.type` | string | `"desktop"`, `"vm"` | Whether the guest is a native desktop or a virtual machine. |
| `guest.cursor` | bool | | Whether the guest cursor is visible in the video stream. When `false`, the client renders a cursor dot overlay. |
| `guest.clipboard` | bool | | Whether clipboard sync is available. |

## Config Files

Stored in `web/config/` and embedded into the binary at compile time.

| File | os | type | cursor | clipboard |
|------|----|------|--------|-----------|
| `linux_desktop.json` | linux | desktop | true | true |
| `macos_desktop.json` | macos | desktop | true | true |
| `macos_vm.json` | macos | vm | false | false |

## Selection Logic

The server selects the config file at startup based on `runtime.GOOS` and the `--display`/`--vm` flag:

| GOOS | Display | Config |
|------|---------|--------|
| `darwin` | `"vm"` | `macos_vm.json` |
| `darwin` | other | `macos_desktop.json` |
| other | any | `linux_desktop.json` |

The selected JSON is cached in memory and served verbatim on every `GET /config` request.

## Client Behavior

### Cursor

| `guest.cursor` | Behavior |
|-----------------|----------|
| `true` | Browser cursor hidden when focused; guest cursor visible in video stream |
| `false` | Browser cursor hidden when focused; white dot overlay tracks mouse position |

### Key Remapping

The client detects the host platform via `navigator.platform` and applies remapping based on the guest OS:

| Host | Guest OS | Remap |
|------|----------|-------|
| Mac | `linux` | `MetaLeft` -> `ControlLeft`, `MetaRight` -> `ControlRight` |
| Mac | `macos` | none |
| non-Mac | `linux` | none |
| non-Mac | `macos` | none |

This allows Mac users to use Cmd+C/V/A/Z naturally when connected to a Linux guest.

### Clipboard

Paste handling (Cmd+V on Mac, Ctrl+V otherwise):

1. Read host clipboard via `navigator.clipboard.readText()`
2. Send text over the clipboard data channel
3. Wait 50ms for the text to arrive on the guest
4. Synthesize V keydown + keyup on the remote
5. Suppress the natural V keyup to avoid double-fire

The browser Clipboard API requires a secure context. Clipboard sync works over `localhost` without TLS, but remote connections require HTTPS (`--tls` or `--tls-cert`/`--tls-key`).
