# bunghole

Remote desktop streaming over WebRTC. Captures an X11 display, encodes with hardware-accelerated H.264/H.265, and streams to a browser with mouse, keyboard, and clipboard support.

## Features

- **Low-latency video** via NVIDIA NVENC (GPU) with software fallback
- **H.264 and H.265** codec support
- **Opus audio** capture from PulseAudio/PipeWire
- **Full input** — mouse (absolute + relative with pointer lock), keyboard, scroll
- **Clipboard sync** — bidirectional copy/paste between browser and remote desktop
- **Headless mode** — can start its own Xorg + GNOME Shell + PipeWire stack
- **Single binary** — everything embedded, including the web client

## Requirements

**System libraries:**
- X11, XTest, XShm, Xfixes
- FFmpeg libraries: libavcodec, libavutil, libswscale
- PulseAudio client library

**Optional:**
- NVIDIA GPU + drivers (for hardware encoding)
- Xorg, GNOME Shell, PipeWire (for `--start-x` headless mode)

## Build

```
go build -tags nolibopusfile
```

## Usage

```
bunghole --token SECRET [flags]
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | (required) | Bearer token for authentication |
| `--addr` | `:8080` | HTTP listen address |
| `--display` | auto | X11 display to capture |
| `--start-x` | `false` | Start a headless Xorg + GNOME Shell |
| `--resolution` | `1920x1080` | Screen resolution (with `--start-x`) |
| `--fps` | `30` | Capture frame rate |
| `--bitrate` | `4000` | Video bitrate in kbps |
| `--codec` | `h264` | Video codec (`h264` or `h265`) |
| `--gop` | `0` | Keyframe interval in frames (0 = 2x FPS) |
| `--gpu` | `0` | GPU index for encoding and Xorg |

### Examples

Capture an existing display:
```
bunghole --token mysecret --display :0
```

Start a headless desktop and stream it:
```
bunghole --token mysecret --start-x --resolution 2560x1440 --codec h265 --bitrate 8000
```

Then open `http://<host>:8080` in a browser, enter the token, and connect. Click the video to engage pointer lock for relative mouse input.

## Browser Compatibility

- **H.264**: All modern browsers
- **H.265**: Chrome 130+, Safari, Edge

## Protocol

Uses [WHEP](https://www.ietf.org/archive/id/draft-murillo-whep-03.html) (WebRTC-HTTP Egress Protocol) for session negotiation:

- `POST /whep` — send SDP offer, receive SDP answer
- `PATCH /whep/{id}` — trickle ICE candidates
- `DELETE /whep/{id}` — disconnect

Authentication is via `Authorization: Bearer <token>` header. Only one session is active at a time; a new connection replaces the previous one.
