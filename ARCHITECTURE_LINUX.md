# Architecture

## Overview

Bunghole is a single-process remote desktop server. It captures an X11 display, encodes the video stream, and delivers it to a browser over WebRTC. Input flows back from the browser through WebRTC data channels and is injected into X11 via XTest.

```
Browser (WebRTC)
    │
    ├── video track ◄──── Encoder ◄──── Capturer ◄──── X11 Display
    ├── audio track ◄──── Opus ◄─────── PulseAudio ◄── PipeWire
    ├── input DC ──────►  InputHandler ──────────────►  XTest
    └── clipboard DC ◄►  ClipboardHandler ◄──────────►  X11 Selections
```

## Pipeline

### Frame Capture (`capture.go`)

Uses MIT-SHM (X Shared Memory Extension) for zero-copy screen capture. `XShmGetImage` reads the root window into a shared memory segment, returning a pointer to BGRA pixel data. The pointer is valid until the next `Grab()` call — no copy is made.

The cursor is composited into the frame buffer using `XFixesGetCursorImage`, which returns the cursor bitmap with per-pixel alpha. This is alpha-blended directly onto the SHM buffer so the cursor always appears in the stream.

### Video Encoding (`encode.go`)

The encoder receives the BGRA frame pointer and converts it to the encoder's pixel format using libswscale:
- **NVENC** (GPU): BGRA to NV12
- **Software** (CPU): BGRA to YUV420P

Codec selection by `--codec` flag:

| | H.264 | H.265 |
|---|---|---|
| GPU | `h264_nvenc` | `hevc_nvenc` |
| CPU fallback | `libx264` | `libx265` |
| Profile | baseline | main |

All paths use ultra-low-latency settings: fastest preset (`p1` / `ultrafast`), zero-latency tuning, CBR rate control, no B-frames. Keyframe interval defaults to 2x FPS (e.g., every 60 frames at 30fps).

The C `encoder_init` function tries the GPU encoder first and falls back to the CPU encoder automatically. The Go `NewEncoder` wrapper logs which encoder was selected.

### Audio Capture (`audio.go`)

Connects to PulseAudio (or PipeWire-Pulse) and records from the default sink monitor, capturing all system audio. PCM samples (48kHz, stereo, int16) are collected into 20ms frames (960 samples per channel) and encoded to Opus.

Audio failure is non-fatal — the video stream continues without audio.

### WebRTC Session (`peer.go`)

Each session creates a `PeerConnection` with a custom `MediaEngine` that registers only the selected video codec (H.264 or H.265) and Opus audio. Two `TrackLocalStaticSample` tracks carry the encoded media.

Two data channels are created by the browser client:
- **`input`**: Receives JSON-encoded mouse/keyboard events
- **`clipboard`**: Exchanges clipboard text bidirectionally

Only one session is active at a time. A new connection tears down the previous one.

### Capture Loop (`main.go: startPipeline`)

The main pipeline runs as a tight synchronous loop on a single goroutine:

```
ticker (1/fps interval)
    → Capturer.Grab()          // pointer to SHM buffer (zero-copy)
    → Encoder.Encode(frame)    // reads SHM, converts color, encodes
    → Session.WriteVideoSample // writes to WebRTC track
```

No channels or frame copies sit between capture and encode. The SHM pointer is read by the encoder synchronously, then the next frame is grabbed.

Audio runs on separate goroutines — one for PulseAudio recording/Opus encoding, one for writing packets to the audio track.

## Input Handling (`input.go`)

The browser sends JSON events over the `input` data channel:

```json
{"type": "mousemove", "x": 500, "y": 300, "relative": true}
{"type": "mousedown", "button": 0}
{"type": "keydown", "key": "a", "code": "KeyA"}
{"type": "wheel", "dx": 0, "dy": -120}
```

The input handler maps these to X11 calls:
- **Mouse movement**: `XTestFakeMotionEvent` (absolute) or `XWarpPointer` (relative, when pointer-locked)
- **Mouse buttons**: `XTestFakeButtonEvent` with JS button→X11 button mapping (0→1, 1→2, 2→3)
- **Keyboard**: Maps the `code` field (physical key position) to X11 keysyms via a lookup table, falling back to the `key` field for character literals
- **Scroll**: Accumulates delta and fires X11 button events (4/5 for vertical, 6/7 for horizontal) per 40px of travel

## Clipboard (`clipboard.go`)

Bidirectional clipboard sync uses the X11 selection protocol:

**Server → Client**: A 250ms polling loop checks clipboard ownership. When another X11 app takes clipboard ownership, the handler requests the `CLIPBOARD` selection as `UTF8_STRING` via `XConvertSelection`. When the `SelectionNotify` event arrives with the data, it's sent to the browser over the clipboard data channel.

**Client → Server**: Text received from the browser is stored locally and ownership of `CLIPBOARD` is claimed via `XSetSelectionOwner`. When other X11 apps request the clipboard (`SelectionRequest` event), the handler responds with the stored text.

## Headless X Server (`xserver.go`)

When `--start-x` is used, bunghole manages its own display stack:

1. **Xorg**: Finds an available display number, generates an `xorg.conf` targeting the specified NVIDIA GPU (queries BusID via `nvidia-smi`), and launches Xorg
2. **PipeWire**: Starts PipeWire + WirePlumber + pipewire-pulse in an isolated `XDG_RUNTIME_DIR` for audio
3. **GNOME Shell**: Launches via `dbus-run-session` and waits for the window manager to be ready (`_NET_SUPPORTING_WM_CHECK`)
4. **Resolution**: Configures the display resolution via xrandr, creating custom modes with `cvt` if needed

Cleanup kills all spawned processes and removes temporary files (X lock files, sockets, config directory).

## HTTP / WHEP (`main.go`)

The server uses standard WHEP endpoints for WebRTC signaling:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/` | GET | Serves the embedded web client |
| `/whep` | POST | Receives SDP offer, returns SDP answer + session Location |
| `/whep/{id}` | PATCH | Trickle ICE candidates |
| `/whep/{id}` | DELETE | Tear down session |

All WHEP endpoints require `Authorization: Bearer <token>`. CORS headers are set for cross-origin access.

ICE gathering completes before the answer is returned (no trickle from server side), so the client gets a complete SDP answer in the POST response.

## Web Client (`web/index.html`)

A single embedded HTML file containing the WebRTC client. Key behaviors:

- Creates the `input` and `clipboard` data channels before sending the offer
- Adds `recvonly` transceivers for video and audio
- Waits for ICE gathering to complete before POSTing the offer
- Uses pointer lock for relative mouse input — click the video to engage, Escape to release
- Scales mouse coordinates to match the video resolution (accounts for `object-fit: contain` letterboxing)
- Keyboard events are only captured while pointer-locked to avoid interfering with browser shortcuts
- Paste events send clipboard text to the server; incoming clipboard text is written to the browser clipboard API

## Dependencies

**cgo / system libraries:**
- `libavcodec`, `libavutil`, `libswscale` — video encoding + color conversion
- `libX11`, `libXtst`, `libXext`, `libXfixes` — capture, input, clipboard

**Go modules:**
- `pion/webrtc/v4` — WebRTC
- `hraban/opus` — Opus encoding (with `nolibopusfile` build tag)
- `jfreymuth/pulse` — PulseAudio client
- `google/uuid` — session IDs
