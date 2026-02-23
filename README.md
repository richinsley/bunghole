# bunghole

<p align="center">
  <img src="web/bunghole.png" width="200" alt="bunghole">
</p>

Remote desktop streaming over WebRTC. Captures a display, encodes with hardware-accelerated H.264/H.265, and streams to a browser with mouse, keyboard, and clipboard support.

## Platforms

### Linux

Captures an X11 display using MIT-SHM with zero-copy frame access. Encodes with NVIDIA NVENC (GPU) or libx264/libx265 (CPU fallback). Supports full input injection via XTest, bidirectional clipboard via X11 selections, and audio capture from PulseAudio/PipeWire.

Optional headless mode (`--start-x`) launches its own Xorg + GNOME Shell + PipeWire stack.

See [ARCHITECTURE_LINUX.md](ARCHITECTURE_LINUX.md) for details.

### macOS

Captures the host display or a macOS VM via ScreenCaptureKit. Encodes with VideoToolbox (hardware H.264/H.265). In **desktop mode**, captures the host screen with cursor. In **VM mode** (`--vm`), runs a full macOS VM via Apple's Virtualization.framework with Metal GPU, VirtioFS file sharing, and isolated desktop — a WebRTC-based macOS terminal server.

Requires Apple Silicon and macOS 14+ for VM mode.

See [ARCHITECTURE_MACOS.md](ARCHITECTURE_MACOS.md) for details.

## Features

- **Low-latency video** via hardware encoding (NVENC on Linux, VideoToolbox on macOS)
- **H.264 and H.265** codec support
- **Opus audio** capture (Linux — PulseAudio/PipeWire)
- **Full input** — mouse, keyboard, scroll
- **Clipboard sync** — bidirectional copy/paste (Linux and macOS desktop mode)
- **macOS VM mode** — isolated macOS desktop with Metal GPU via Virtualization.framework
- **Headless mode** — start Xorg + GNOME Shell + PipeWire (Linux)
- **Single binary** — everything embedded, including the web client

## Requirements

### Linux

- X11, XTest, XShm, Xfixes
- FFmpeg libraries: libavcodec, libavutil, libswscale
- PulseAudio client library
- Optional: NVIDIA GPU + drivers, Xorg, GNOME Shell, PipeWire

### macOS

- macOS 14+ (Sonoma)
- Xcode Command Line Tools
- FFmpeg libraries (`brew install ffmpeg`)
- Apple Silicon (for VM mode)

## Build

### cmake (recommended)

```
mkdir build && cd build
cmake ..
make
```

### go build

```
# Linux
go build -tags nolibopusfile -o bunghole ./cmd/bunghole

# macOS
go build -o bunghole ./cmd/bunghole
codesign --force --sign - --entitlements entitlements.plist ./bunghole
```

The codesign step is required for VM mode (`com.apple.security.virtualization` entitlement).

## Usage

```
bunghole --token SECRET [flags]
```

### Common Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | (required) | Bearer token for authentication |
| `--addr` | `:8080` | HTTP listen address |
| `--fps` | `30` | Capture frame rate |
| `--bitrate` | `4000` | Video bitrate in kbps |
| `--codec` | `h264` | Video codec (`h264` or `h265`) |
| `--gop` | `0` | Keyframe interval in frames (0 = 2x FPS) |
| `--stats` | `false` | Log pipeline stats every 5 seconds |

### Linux Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--display` | auto | X11 display to capture |
| `--start-x` | `false` | Start a headless Xorg + GNOME Shell |
| `--resolution` | `1920x1080` | Screen resolution (with `--start-x`) |
| `--gpu` | `0` | GPU index for encoding and Xorg |

### macOS Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--vm` | `false` | Run macOS VM and stream its display |
| `--vm-share` | `$HOME` | Directory to share with VM via VirtioFS |
| `--disk` | `64` | VM disk size in GB (used with `setup`) |

### Examples

Capture an existing X11 display (Linux):
```
bunghole --token mysecret --display :0
```

Start a headless desktop and stream it (Linux):
```
bunghole --token mysecret --start-x --resolution 2560x1440 --codec h265 --bitrate 8000
```

Capture the host desktop (macOS):
```
bunghole --token mysecret
```

First-time VM setup (macOS — interactive, shows setup assistant window):
```
bunghole setup --disk 64
```

Stream a macOS VM desktop (macOS):
```
bunghole --vm --token mysecret --vm-share ~/Projects
```

Then open `http://<host>:8080` in a browser, enter the token, and connect. Click the video to focus input; press Escape to release.

## Browser Compatibility

- **H.264**: All modern browsers
- **H.265**: Chrome 130+, Safari, Edge

## Protocol

Uses [WHEP](https://www.ietf.org/archive/id/draft-murillo-whep-03.html) (WebRTC-HTTP Egress Protocol) for session negotiation:

- `POST /whep` — send SDP offer, receive SDP answer
- `PATCH /whep/{id}` — trickle ICE candidates
- `DELETE /whep/{id}` — disconnect

Authentication is via `Authorization: Bearer <token>` header. Only one session is active at a time; a new connection replaces the previous one.
