# Architecture — macOS

## Overview

On macOS, bunghole operates in two modes:

1. **Desktop mode** — captures the host display via ScreenCaptureKit and streams it over WebRTC
2. **VM mode** (`--vm`) — runs a macOS VM via Apple's Virtualization.framework, captures its display via ScreenCaptureKit, and streams the isolated VM desktop over WebRTC

VM mode turns bunghole into a macOS terminal server — full session isolation with Metal GPU access.

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

## Desktop Mode

### Frame Capture (`capture_darwin.go`, `capture_darwin.m`)

Uses ScreenCaptureKit to capture the main display. An `SCStream` is configured with:
- `SCContentFilter` targeting the main display
- BGRA pixel format, configurable FPS, `queueDepth=3`
- `showsCursor=YES` — the system cursor is composited into the stream

The `SCStreamOutput` delegate receives `CMSampleBuffer` frames, locks the backing `CVPixelBuffer`, and stores the latest frame in a double-buffered struct protected by a pthread mutex. `sck_capture_grab()` returns a pointer to the locked BGRA pixel data.

### Input Injection (`input_darwin.go`)

Uses CoreGraphics event injection via `CGEventPost(kCGHIDEventTap, ...)`:
- **Mouse movement**: `CGEventCreateMouseEvent` with appropriate event types — `kCGEventMouseMoved` normally, `kCGEventLeftMouseDragged` / `kCGEventRightMouseDragged` when buttons are held (tracked via a bitmask)
- **Mouse buttons**: `CGEventCreateMouseEvent` at the coordinates sent from the browser
- **Scroll**: `CGEventCreateScrollWheelEvent` with pixel units, values negated to match macOS convention
- **Keyboard**: `CGEventCreateKeyboardEvent` with macOS virtual keycodes mapped from the browser's `KeyboardEvent.code`

The keycode mapping table (`codeMap`) maps web key codes (e.g., `KeyA`, `ShiftLeft`, `ArrowUp`) to macOS virtual keycodes from `HIToolbox/Events.h`.

### Clipboard (`clipboard_darwin.go`, `clipboard_darwin.m`)

Uses NSPasteboard for bidirectional clipboard sync:
- **Server → Client**: A 250ms polling loop checks the pasteboard change count. When it changes, the text content is sent to the browser over the clipboard data channel.
- **Client → Server**: Text received from the browser is written to the general pasteboard.

## VM Mode

### Virtualization.framework (`vm_darwin.go`, `vm_darwin.m`)

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

### Setup (`bunghole setup`)

Interactive first-time VM setup:
1. Fetches the latest macOS IPSW restore URL via `VZMacOSRestoreImage`
2. Downloads the IPSW (~15 GB, cached in `~/Library/Application Support/bunghole/cache/`)
3. Creates the VM bundle with disk image and hardware config
4. Installs macOS into the VM (shows the native setup assistant window)

The macOS setup assistant must be completed manually in the native window that appears during install.

### VM Frame Capture (`vm_capture_darwin.go`, `capture_darwin.m`)

Uses the same ScreenCaptureKit infrastructure as desktop mode, but with a window filter:
- `SCContentFilter(desktopIndependentWindow:)` targeting the VM's offscreen NSWindow
- Same delegate, same double-buffered frame delivery
- `showsCursor=YES` has no effect — the guest cursor is a hardware overlay not captured by SCK

A white cursor dot is rendered in the browser as a substitute (see Web Client section).

### VM Input Injection (`vm_input_darwin.go`, `vm_input_darwin.m`)

Synthesizes NSEvents and forwards them to VZVirtualMachineView's responder methods:
- **Mouse movement**: `[vmView mouseMoved:]` or `[vmView mouseDragged:]` when buttons are held (tracked via `_vm_buttons_down` bitmask)
- **Mouse buttons**: `[vmView mouseDown:]` / `[vmView mouseUp:]` and right/other variants
- **Scroll**: `CGEventCreateScrollWheelEvent` converted to NSEvent via `[NSEvent eventWithCGEvent:]`, forwarded to `[vmView scrollWheel:]`
- **Keyboard**: `[NSEvent keyEventWithType:...]` forwarded to `[vmView keyDown:]` / `[vmView keyUp:]`

All calls dispatched to the main thread via `dispatch_async(dispatch_get_main_queue(), ...)`. Coordinates are converted from top-left (web) to bottom-left (AppKit) origin.

Reuses the `codeMap` from `input_darwin.go` for keycode mapping.

## Shared Components

### Video Encoding (`encode.go`)

Same encoder as Linux, using FFmpeg via cgo. On macOS, uses VideoToolbox hardware encoding:

| | H.264 | H.265 |
|---|---|---|
| macOS | `h264_videotoolbox` | `hevc_videotoolbox` |

Ultra-low-latency settings: `realtime=1`, `allow_sw=1`, CBR rate control, no B-frames.

### WebRTC Session (`peer.go`)

Same as Linux. On connection, creates either `InputHandler` (desktop) or `VMInputHandler` (VM mode) based on the display name. Clipboard sync is active in desktop mode; deferred in VM mode (would need a guest agent).

### Capture Loop (`main.go: startPipeline`)

Branches on display mode:
- Desktop: `NewCapturer()` → `sck_capture_start_display()`
- VM: `NewVMCapturer()` → `sck_capture_start_window()`

The rest of the pipeline (encode → write to WebRTC track) is identical.

## Web Client (`web/index.html`)

Single embedded HTML file. Behavior adapts based on `/mode` endpoint response:

**Desktop mode** (`{"mode":"desktop"}`):
- Browser cursor hidden when focused (`.active` class sets `cursor: none`)
- System cursor visible in the video stream via ScreenCaptureKit
- No cursor dot overlay

**VM mode** (`{"mode":"vm"}`):
- Browser cursor hidden when focused
- White cursor dot (12px circle) tracks mouse position as an overlay
- Guest hardware cursor is not captured by ScreenCaptureKit, so the dot is the only cursor feedback

Common behavior:
- Click video to focus input, Escape to release
- Absolute mouse coordinates mapped from browser viewport to remote desktop resolution (accounts for `object-fit: contain` letterboxing)
- Keyboard events captured only when focused
- Paste events send clipboard text to server

## HTTP / WHEP (`main.go`)

Same WHEP endpoints as Linux, plus:

| Endpoint | Method | Purpose |
|----------|--------|---------|
| `/mode` | GET | Returns `{"mode":"desktop"}` or `{"mode":"vm"}` |

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

**Build requirements:**
- macOS 14+ (Sonoma)
- Apple Silicon (for VM mode)
- Xcode Command Line Tools
- FFmpeg libraries (`brew install ffmpeg`)
- Code signing with `com.apple.security.virtualization` entitlement (for VM mode)

## Known Limitations

- **Audio**: Not yet implemented on macOS
- **VM cursor shapes**: Guest cursor is a hardware overlay inaccessible via public API; white dot serves as substitute
- **VM clipboard**: Requires a guest agent (not yet implemented)
- **VM resolution**: Hardcoded 1920x1080
- **VM limit**: Apple kernel enforces max 2 concurrent macOS VMs
