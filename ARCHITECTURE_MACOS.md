# macOS

## Requirements

- macOS 14+ (Sonoma)
- Xcode Command Line Tools
- FFmpeg libraries (`brew install ffmpeg`)
- Apple Silicon (required for VM mode)

## Build

### cmake (recommended)

```
mkdir build && cd build
cmake ..
make
```

### go build

```
go build -o bunghole ./cmd/bunghole
codesign --force --sign - --entitlements entitlements.plist ./bunghole
```

The codesign step is required for VM mode (`com.apple.security.virtualization` entitlement). Desktop mode works without it.

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
| `--vm` | `false` | Run macOS VM and stream its display |
| `--vm-share` | `$HOME` | Directory to share with VM via VirtioFS |
| `--disk` | `64` | VM disk size in GB (used with `setup`) |
| `--stats` | `false` | Log pipeline stats every 5 seconds |

### Examples

Capture the host desktop:
```
bunghole --token mysecret
```

Capture with H.265 at higher bitrate:
```
bunghole --token mysecret --codec h265 --bitrate 8000
```

First-time VM setup (interactive — shows the macOS setup assistant window):
```
bunghole setup --disk 64
```

Stream a macOS VM desktop:
```
bunghole --vm --token mysecret --vm-share ~/Projects
```

Then open `http://<host>:8080` in a browser, enter the token, and connect. Click the video to focus input; press Escape to release.

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

### Overview

On macOS, bunghole operates in two modes:

1. **Desktop mode** — captures the host display via ScreenCaptureKit and streams it over WebRTC
2. **VM mode** (`--vm`) — runs a macOS VM via Virtualization.framework, captures its display via ScreenCaptureKit, and streams the isolated VM desktop over WebRTC

VM mode turns bunghole into a macOS terminal server — full session isolation with Metal GPU access.

### Pipeline Overview

```
Browser (WebRTC)
    │
    ├── video track ◄──── Encoder ◄──── ScreenCaptureKit
    │                                        │
    │                              ┌─────────┴─────────┐
    │                         Desktop mode          VM mode
    │                         (main display)    (VZVirtualMachineView
    │                                            NSWindow capture)
    │
    ├── audio track ◄──── (not yet implemented on macOS)
    ├── input DC ──────►  InputHandler / VMInputHandler
    └── clipboard DC ◄►  ClipboardHandler (desktop mode only)
```

With multiple viewers, the shared tracks broadcast to all bound PeerConnections:

```
Capturer → Encoder → videoTrack.WriteSample() ──→ Controller PC  (input + clipboard)
                                                ──→ Viewer PC 1   (video/audio only)
                                                ──→ Viewer PC 2   (video/audio only)
```

The pipeline starts when the first session connects and stops when the last disconnects.

## Desktop Mode

### Frame Capture

Uses ScreenCaptureKit to capture the main display. An `SCStream` is configured with:
- `SCContentFilter` targeting the main display
- BGRA pixel format, configurable FPS, `queueDepth=3`
- `showsCursor=YES` — the system cursor is composited into the stream

The `SCStreamOutput` delegate receives `CMSampleBuffer` frames, locks the backing `CVPixelBuffer`, and stores the latest frame in a double-buffered struct protected by a pthread mutex. `sck_capture_grab()` returns a pointer to the locked BGRA pixel data.

### Input Injection

Uses CoreGraphics event injection via `CGEventPost(kCGHIDEventTap, ...)`:
- **Mouse movement**: `CGEventCreateMouseEvent` — `kCGEventMouseMoved` normally, `kCGEventLeftMouseDragged` / `kCGEventRightMouseDragged` when buttons are held
- **Mouse buttons**: `CGEventCreateMouseEvent` at the coordinates sent from the browser
- **Scroll**: `CGEventCreateScrollWheelEvent` with pixel units, values negated to match macOS convention
- **Keyboard**: `CGEventCreateKeyboardEvent` with macOS virtual keycodes mapped from the browser's `KeyboardEvent.code`

### Clipboard

Uses NSPasteboard for bidirectional clipboard sync:
- **Server to client**: A 250ms polling loop checks the pasteboard change count. When it changes, the text content is sent to the browser over the clipboard data channel.
- **Client to server**: Text received from the browser is written to the general pasteboard.

## VM Mode

### Virtualization.framework

Creates and manages a macOS VM using Apple's Virtualization.framework (Apple Silicon only, macOS 14+).

**VM Configuration:**
- `VZMacPlatformConfiguration` with hardware model + machine identifier persisted in the bundle
- `VZMacGraphicsDeviceConfiguration` with `VZMacGraphicsDisplayConfiguration` (1920x1080 @ 72ppi) — Metal GPU
- `VZVirtioBlockDeviceConfiguration` for the disk image
- `VZVirtioFileSystemDeviceConfiguration` with `macOSGuestAutomountTag` for VirtioFS shared directory
- `VZUSBKeyboardConfiguration` + `VZUSBScreenCoordinatePointingDeviceConfiguration`
- `VZVirtioNetworkDeviceConfiguration` with NAT
- CPU count: host physical cores. Memory: host RAM / 2 (capped at 16 GB)

**Threading model:** VM mode requires an NSApplication RunLoop on the main OS thread. Go's main goroutine locks to the main thread via `runtime.LockOSThread()` and calls `vm_nsapp_run()` (which calls `[NSApp run]`). The HTTP server runs on a background goroutine. VM/AppKit operations dispatch to the main thread via GCD.

**VZVirtualMachineView** is hosted in a borderless NSWindow positioned offscreen at (-10000, -10000) — not minimized (ScreenCaptureKit pauses on minimize).

**VM bundle** stored at `~/Library/Application Support/bunghole/vm/`:
- `disk.img` — sparse APFS disk image
- `aux.img` — NVRAM
- `hardware.json` — base64-encoded `VZMacHardwareModel` + `VZMacMachineIdentifier`

Apple hard-limits macOS VMs to 2 concurrent instances (kernel-enforced).

### Setup

Interactive first-time VM setup (`bunghole setup`):
1. Fetches the latest macOS IPSW restore URL via `VZMacOSRestoreImage`
2. Downloads the IPSW (~15 GB, cached in `~/Library/Application Support/bunghole/cache/`)
3. Creates the VM bundle with disk image and hardware config
4. Installs macOS into the VM (shows the native setup assistant window)

The macOS setup assistant must be completed manually in the native window.

### VM Frame Capture

Uses the same ScreenCaptureKit infrastructure as desktop mode, but with a window filter:
- `SCContentFilter(desktopIndependentWindow:)` targeting the VM's offscreen NSWindow
- Same delegate, same double-buffered frame delivery
- `showsCursor=YES` has no effect — the guest cursor is a hardware overlay not captured by SCK

A white cursor dot is rendered in the browser as a substitute (see Web Client section).

### VM Input Injection

Synthesizes NSEvents and forwards them to VZVirtualMachineView's responder methods:
- **Mouse movement**: `[vmView mouseMoved:]` or `[vmView mouseDragged:]` when buttons are held
- **Mouse buttons**: `[vmView mouseDown:]` / `[vmView mouseUp:]` and right/other variants
- **Scroll**: `CGEventCreateScrollWheelEvent` converted to NSEvent, forwarded to `[vmView scrollWheel:]`
- **Keyboard**: `[NSEvent keyEventWithType:...]` forwarded to `[vmView keyDown:]` / `[vmView keyUp:]`

All calls dispatched to the main thread via `dispatch_async(dispatch_get_main_queue(), ...)`. Coordinates are converted from top-left (web) to bottom-left (AppKit) origin.

## Shared Components

### Video Encoding

Uses FFmpeg via cgo with VideoToolbox hardware encoding:

| | H.264 | H.265 |
|---|---|---|
| macOS | `h264_videotoolbox` | `hevc_videotoolbox` |

Ultra-low-latency settings: `realtime=1`, `allow_sw=1`, CBR rate control, no B-frames.

### WebRTC Sessions

The server owns shared `TrackLocalStaticSample` tracks for video and audio. Each session creates a `PeerConnection` with a custom `MediaEngine` registering only the selected codec. The shared tracks are added to every PC — `WriteSample()` broadcasts to all bound connections.

**Controller session**: One at a time. Has data channels for input and clipboard. Creates either `InputHandler` (desktop) or `VMInputHandler` (VM mode) based on the display name. A new controller replaces the old one, but the pipeline continues if viewers exist.

**Viewer sessions**: Zero or more. Video and audio tracks only — no data channels. Each viewer is independent.

### Capture Loop

Branches on display mode:
- Desktop: `NewCapturer()` → `sck_capture_start_display()`
- VM: `NewVMCapturer()` → `sck_capture_start_window()`

The rest of the pipeline (encode → write to shared WebRTC tracks) is identical.

### HTTP Endpoints

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/` | GET | Serves the embedded web client |
| `/mode` | GET | Returns `{"mode":"desktop"}` or `{"mode":"vm"}` |
| `/whep` | POST | Controller: SDP offer → answer |
| `/whep/{id}` | PATCH | Controller: trickle ICE candidates |
| `/whep/{id}` | DELETE | Controller: disconnect |
| `/whep/view` | POST | Viewer: SDP offer → answer |
| `/whep/view/{id}` | PATCH | Viewer: trickle ICE candidates |
| `/whep/view/{id}` | DELETE | Viewer: disconnect |
| `/debug/frame` | GET | Returns a PNG screenshot |

All WHEP endpoints require `Authorization: Bearer <token>`. CORS headers are set for cross-origin access. ICE gathering completes server-side before the answer is returned.

## Web Client

Single embedded HTML file. Behavior adapts based on `/mode` endpoint response:

**Desktop mode** (`{"mode":"desktop"}`):
- Browser cursor hidden when focused (`.active` class sets `cursor: none`)
- System cursor visible in the video stream via ScreenCaptureKit

**VM mode** (`{"mode":"vm"}`):
- Browser cursor hidden when focused
- White cursor dot (12px circle) tracks mouse position as an overlay
- Guest hardware cursor is not captured by ScreenCaptureKit, so the dot is the only cursor feedback

Common behavior:
- Click video to focus input, Escape to release
- Absolute mouse coordinates mapped from browser viewport to remote desktop resolution
- Keyboard events captured only when focused
- Paste events send clipboard text to server

## Dependencies

**cgo / system frameworks:**
- `ScreenCaptureKit`, `CoreMedia`, `CoreVideo` — display/window capture
- `CoreGraphics` — input injection (desktop mode)
- `Cocoa` — NSPasteboard, NSEvent, NSWindow
- `Virtualization` — macOS VM (VM mode only)
- `libavcodec`, `libavutil`, `libswscale` — video encoding + color conversion

**Go modules:**
- `pion/webrtc/v4` — WebRTC
- `google/uuid` — session IDs

## Known Limitations

- **Audio**: Not yet implemented on macOS
- **VM cursor shapes**: Guest cursor is a hardware overlay inaccessible via public API; white dot serves as substitute
- **VM clipboard**: Requires a guest agent (not yet implemented)
- **VM resolution**: Hardcoded 1920x1080
- **VM limit**: Apple kernel enforces max 2 concurrent macOS VMs
