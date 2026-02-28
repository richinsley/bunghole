# Linux

## Requirements

- X11, XTest, XShm, Xfixes
- FFmpeg libraries: libavcodec, libavutil, libswscale
- PulseAudio client library (libpulse)
- Optional: NVIDIA GPU + drivers (for NVENC and NvFBC)
- Optional: Xorg, GNOME Shell, PipeWire (for headless mode)

### Packages (Ubuntu/Debian)

```
apt install libavcodec-dev libavutil-dev libswscale-dev \
            libx11-dev libxtst-dev libxext-dev libxfixes-dev \
            libpulse-dev
```

For headless mode: `xserver-xorg gnome-shell pipewire wireplumber pipewire-pulse xrandr`

**Headless mode** (`--start-x`) requires root (`sudo`) to acquire DRM master for the GPU. Use `--user` to drop privileges for the desktop session (GNOME Shell, PipeWire) while keeping Xorg as root. bunghole automatically detects the nvidia module path (nvidia 580+ moved it) and cleans up orphaned Xorg processes from previous runs.

## Build

### cmake (recommended)

```
mkdir build && cd build
cmake ..
make
```

### go build

```
go build -tags nolibopusfile -o bunghole ./cmd/bunghole
```

The `nolibopusfile` tag is required — it avoids linking against libopusfile (only the Opus encoder is needed).

## Usage

```
bunghole --token SECRET [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | (required) | Bearer token for authentication |
| `--addr` | `:8080` | HTTP listen address |
| `--fps` | `30` | Capture frame rate |
| `--bitrate` | `4000` | Video bitrate in kbps |
| `--codec` | `h264` | Video codec (`h264` or `h265`) |
| `--gop` | `0` | Keyframe interval in frames (0 = 2x FPS) |
| `--gpu` | `0` | GPU index for encoding and Xorg |
| `--display` | auto | X11 display to capture |
| `--start-x` | `false` | Start a headless Xorg + GNOME Shell (requires `sudo`) |
| `--user` | | Run desktop session as this user (with `--start-x`); Xorg stays root |
| `--resolution` | `1920x1080` | Screen resolution (with `--start-x`) |
| `--stats` | `false` | Log pipeline stats every 5 seconds |
| `--experimental-nvfbc` | `false` | Enable experimental NvFBC capture path |
| `--tls` | `false` | Enable TLS with auto-generated self-signed certificate |
| `--tls-cert` | | Path to TLS certificate file (PEM) |
| `--tls-key` | | Path to TLS private key file (PEM) |

### Examples

Capture an existing X11 display:
```
bunghole --token mysecret --display :0
```

Start a headless desktop and stream it:
```
sudo bunghole --token mysecret --start-x --resolution 2560x1440 --codec h265 --bitrate 8000
```

Start headless but run the desktop session as a non-root user:
```
sudo bunghole --token mysecret --start-x --user rich --gpu 1
```

Use a specific GPU (e.g., second GPU for NVENC + NvFBC):
```
sudo bunghole --token mysecret --start-x --gpu 1 --experimental-nvfbc
```

Enable HTTPS with a self-signed certificate (required for clipboard sync over non-localhost):
```
bunghole --token mysecret --tls
```

Use your own TLS certificate (e.g., from Let's Encrypt):
```
bunghole --token mysecret --tls-cert /etc/letsencrypt/live/example.com/fullchain.pem --tls-key /etc/letsencrypt/live/example.com/privkey.pem
```

Then open `http://<host>:8080` (or `https://<host>:8080` with TLS) in a browser, enter the token, and connect. Click the video to focus input; press Escape to release.

### Viewer Streams

In addition to the interactive browser session, you can connect multiple view-only streams. Viewers receive video and audio but cannot send input.

Connect a second browser tab as a viewer:
```js
// POST to /whep/view instead of /whep
const resp = await fetch("http://host:8080/whep/view", {
  method: "POST",
  headers: { "Authorization": "Bearer mysecret", "Content-Type": "application/sdp" },
  body: offer.sdp,
});
```

Connect a WHEP-capable hardware decoder (Teradek Prism, etc.):
- Set the WHEP endpoint to `http://<host>:8080/whep/view`
- Set the authorization header to `Bearer <token>`

Multiple viewers can connect simultaneously. The capture/encode pipeline is shared — one encode feeds all connections. Viewers continue receiving video if the controller disconnects.

## Architecture

### Pipeline Overview

```
Browser (WebRTC)
    │
    ├── video track ◄──── Encoder ◄──── Capturer ◄──── X11 Display
    ├── audio track ◄──── Opus ◄─────── PulseAudio ◄── PipeWire
    ├── input DC ──────►  InputHandler ──────────────►  XTest
    └── clipboard DC ◄►  ClipboardHandler ◄──────────►  X11 Selections
```

With multiple viewers, the shared tracks broadcast to all bound PeerConnections:

```
Capturer → Encoder → videoTrack.WriteSample() ──→ Controller PC  (input + clipboard)
                                                ──→ Viewer PC 1   (video/audio only)
                                                ──→ Viewer PC 2   (video/audio only)
                   audioTrack.WriteSample()     ──→ (same broadcast)
```

The pipeline starts when the first session connects and stops when the last disconnects.

### Frame Capture

Two capture backends are available:

**MIT-SHM** (default): `XShmGetImage` reads the root window into a shared memory segment, returning a pointer to BGRA pixel data. The pointer is valid until the next `Grab()` call — no copy is made. The cursor is composited into the frame buffer using `XFixesGetCursorImage` with per-pixel alpha blending.

**NvFBC** (experimental, opt-in via `--experimental-nvfbc`): Captures directly to CUDA device memory in NV12 format via `NVFBC_TOCUDA`. Zero-copy path — the CUDA device pointer is passed directly to NVENC without any CPU-side data transfer.

### Video Encoding

The encoder receives either a CUDA device pointer (NvFBC path) or a BGRA frame pointer (XShm path) and produces H.264 or H.265 NAL units.

| | H.264 | H.265 |
|---|---|---|
| GPU | `h264_nvenc` | `hevc_nvenc` |
| CPU fallback | `libx264` | `libx265` |
| Profile | baseline | main |

NvFBC + NVENC path: The CUDA device pointer is used to create an `AVHWFramesContext`, so the encoder reads directly from GPU memory — no `sws_scale` or CPU transfer. This is the zero-copy path.

XShm + NVENC path: BGRA pixels are uploaded to GPU via `cuMemcpy2D`, then encoded.

XShm + CPU path: BGRA to YUV420P via `sws_scale`, then encoded with libx264/libx265.

All paths use ultra-low-latency settings: fastest preset (`p1` / `ultrafast`), zero-latency tuning, CBR rate control, no B-frames. Keyframe interval defaults to 2x FPS.

### Audio Capture

Connects to PulseAudio (or PipeWire-Pulse) and records from the default sink monitor, capturing all system audio. PCM samples (48kHz, stereo, int16) are collected into 20ms frames (960 samples per channel) and encoded to Opus.

Audio failure is non-fatal — the video stream continues without audio.

### WebRTC Sessions

The server owns shared `TrackLocalStaticSample` tracks for video and audio. Each session creates a `PeerConnection` with a custom `MediaEngine` registering only the selected codec. The shared tracks are added to every PC — `WriteSample()` broadcasts to all bound connections.

**Controller session**: One at a time. Has data channels for input and clipboard. A new controller replaces the old one (the old PC is closed, but the pipeline continues if viewers exist).

**Viewer sessions**: Zero or more. Video and audio tracks only — no data channels. Each viewer is independent; disconnecting one does not affect others.

Two data channels are created by the browser client (controller only):
- **`input`**: Receives JSON-encoded mouse/keyboard events
- **`clipboard`**: Exchanges clipboard text bidirectionally

### Capture Loop

The pipeline runs as a tight synchronous loop on a single goroutine:

```
ticker (1/fps interval)
    → Capturer.Grab()                // pointer to SHM buffer or CUDA ptr
    → Encoder.Encode(frame)          // encodes to H.264/H.265
    → videoTrack.WriteSample()       // broadcasts to all PeerConnections
```

No channels or frame copies sit between capture and encode. Audio runs on separate goroutines — one for PulseAudio recording/Opus encoding, one for writing packets to the audio track.

### Input Handling

The browser sends JSON events over the `input` data channel:

```json
{"type": "mousemove", "x": 500, "y": 300, "relative": true}
{"type": "mousedown", "button": 0}
{"type": "keydown", "key": "a", "code": "KeyA"}
{"type": "wheel", "dx": 0, "dy": -120}
```

The input handler maps these to X11 calls:
- **Mouse movement**: `XTestFakeMotionEvent` (absolute) or `XWarpPointer` (relative, when pointer-locked)
- **Mouse buttons**: `XTestFakeButtonEvent` with JS button to X11 button mapping (0→1, 1→2, 2→3)
- **Keyboard**: Maps the `code` field (physical key position) to X11 keysyms via a lookup table, falling back to the `key` field for character literals
- **Scroll**: Accumulates delta and fires X11 button events (4/5 for vertical, 6/7 for horizontal) per 40px of travel

### Clipboard

> **Note:** The browser Clipboard API (`navigator.clipboard`) requires a [secure context](https://developer.mozilla.org/en-US/docs/Web/API/Clipboard_API#security_considerations). Clipboard sync works over `localhost` without TLS, but remote connections require HTTPS (`--tls` or `--tls-cert`/`--tls-key`).

Bidirectional clipboard sync uses the X11 selection protocol:

**Server to client**: A 250ms polling loop checks clipboard ownership. When another X11 app takes ownership, the handler requests `CLIPBOARD` as `UTF8_STRING` via `XConvertSelection`. The data arrives via `SelectionNotify` and is sent to the browser over the clipboard data channel.

**Client to server**: Text from the browser is stored locally and ownership of `CLIPBOARD` is claimed via `XSetSelectionOwner`. When other X11 apps request the clipboard (`SelectionRequest`), the handler responds with the stored text.

### Headless X Server

When `--start-x` is used (requires `sudo`), bunghole manages its own display stack:

1. **Xorg**: Runs as root (needs DRM master). Finds an available display number, generates an `xorg.conf` targeting the specified NVIDIA GPU (queries BusID via `nvidia-smi`), and launches Xorg
2. **PipeWire**: Starts PipeWire + WirePlumber + pipewire-pulse in an isolated `XDG_RUNTIME_DIR`
3. **GNOME Shell**: Launches via `dbus-run-session` and waits for the window manager to be ready (`_NET_SUPPORTING_WM_CHECK`)
4. **Resolution**: Configures the display resolution via xrandr, creating custom modes with `cvt` if needed

When `--user` is specified, steps 2-3 run as the target user via `syscall.Credential` (the process drops privileges). The Xauthority file is made readable and the runtime directory is owned by the target user so PipeWire and GNOME Shell can operate normally.

Cleanup kills all spawned processes and removes temporary files (X lock files, sockets, config directory).

### HTTP Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/` | GET | Serves the embedded web client |
| `/config` | GET | Returns guest config JSON (os, type, cursor, clipboard) |
| `/whep` | POST | Controller: SDP offer → answer |
| `/whep/{id}` | PATCH | Controller: trickle ICE candidates |
| `/whep/{id}` | DELETE | Controller: disconnect |
| `/whep/view` | POST | Viewer: SDP offer → answer |
| `/whep/view/{id}` | PATCH | Viewer: trickle ICE candidates |
| `/whep/view/{id}` | DELETE | Viewer: disconnect |
| `/debug/frame` | GET | Returns a PNG screenshot |

All WHEP endpoints require `Authorization: Bearer <token>`. CORS headers are set for cross-origin access. ICE gathering completes server-side before the answer is returned.

## Dependencies

**cgo / system libraries:**
- `libavcodec`, `libavutil`, `libswscale` — video encoding + color conversion
- `libX11`, `libXtst`, `libXext`, `libXfixes` — capture, input, clipboard
- `libpulse` — audio capture

**Go modules:**
- `pion/webrtc/v4` — WebRTC
- `hraban/opus` — Opus encoding (with `nolibopusfile` build tag)
- `jfreymuth/pulse` — PulseAudio client
- `google/uuid` — session IDs

## Web Client

A single embedded HTML file containing the WebRTC client. Fetches `/config` on connect to adapt behavior based on the guest platform.

- Creates `input` and `clipboard` data channels before sending the offer (controller only)
- Adds `recvonly` transceivers for video and audio
- Waits for ICE gathering to complete before POSTing the offer
- Click the video to focus input; press Escape to release
- Scales mouse coordinates to match the video resolution (accounts for `object-fit: contain` letterboxing)
- Keyboard events are only captured while focused
- Mac host + Linux guest: Meta (Cmd) keys are remapped to Control
- Paste (Cmd+V / Ctrl+V) reads the host clipboard, sends text over the clipboard data channel, then synthesizes the V keystroke after a 50ms delay
