# macOS VM Audio

This document covers setup and troubleshooting for bunghole VM-mode audio.

## Goal

Stream macOS guest audio to remote WebRTC clients **without** playing guest audio on host physical speakers.

## Approaches

### 1. CoreAudio HAL Driver (Recommended)

A CoreAudio AudioServerPlugIn that creates virtual "Bunghole Output" and "Bunghole Input" devices. Apps play to the output device; the driver captures samples, Opus-encodes them, and sends to the host over virtio-vsock. No TCC/Screen Recording permission needed.

### 2. Guest Agent (Legacy)

A user-space helper (`bunghole-vm-audio`) that captures audio via ScreenCaptureKit. Requires TCC Screen Recording permission, which is fragile in LaunchAgent sessions.

## Architecture (Driver)

```
Guest VM                                          Host
┌──────────────────────────┐        ┌─────────────────────────┐
│ Apps → "Bunghole Output" │        │                         │
│   ↓                      │        │                         │
│ HAL Plugin (coreaudiod)  │        │                         │
│   ↓ ring buffer          │        │                         │
│   ↓ Opus encode          │ vsock  │ vsock listener :5000    │
│   ↓ vsock send ──────────────────→│   ↓                     │
│                          │ :5000  │ VsockAudioCapture       │
│                          │        │   ↓                     │
│ "Bunghole Input" → Apps  │        │ WebRTC audio track      │
│   ↑                      │        │                         │
│   ↑ ring buffer          │        │                         │
│   ↑ Opus decode          │ vsock  │ (future: host mic →     │
│   ↑ vsock recv ←─────────────────←│  Opus encode → vsock)   │
│                          │ :5001  │                         │
└──────────────────────────┘        └─────────────────────────┘
```

- Output: apps mix into "Bunghole Output" → IO callback writes to ring → transport thread reads 960 frames (20ms) → Float32→Int16 + volume → `opus_encode` → 2-byte BE length-prefixed frame → vsock CID 2 port 5000 → host `VsockAudioCapture` → WebRTC audio track
- Input (future): host sends Opus on vsock port 5001 → driver decodes → ring → IO callback reads into "Bunghole Input"
- Both transport threads auto-reconnect on vsock errors (1s backoff)

### Transport Priority (Host-side)

1. If `--audio-udp-listen` is set, host uses UDP ingest.
2. If `--vm` is set (no UDP flag), host uses vsock (auto-started on port 5000).
3. If neither (host desktop mode), ScreenCaptureKit audio capture is used directly.

## Quick Start (Driver)

### 1) Build

```bash
cmake -S . -B build
cmake --build build
```

This builds both `bunghole` and the `BungholeAudio.driver` bundle.

### 2) Stage driver into VM shared folder

```bash
./scripts/vm-audio-stage.sh --vm-share "$HOME"
```

(Uses `--mode driver` by default.)

### 3) Start host

```bash
./build/bunghole --vm --token SECRET --vm-share "$HOME"
```

Expected log:
```text
vsock audio listener started on port 5000
audio: source=guest-vsock
```

### 4) Install driver (inside VM)

```bash
cd "/Volumes/My Shared Files/.bunghole-audio-driver"
sudo ./install.sh
```

The script copies the `.driver` bundle to `/Library/Audio/Plug-Ins/HAL/` and restarts coreaudiod.

### 5) Set default output

In System Settings → Sound → Output, select "Bunghole Output".

Or via command line:
```bash
brew install switchaudio-osx
SwitchAudioSource -s 'Bunghole Output'
```

### 6) Verify

Play audio in the guest (e.g., YouTube in Safari). Host logs should show:
```text
audio: vsock guest connected
audio: first vsock packet (N bytes)
```

Connect a browser to the stream — audio should play.

## Quick Start (Legacy Guest Agent)

### 1) Stage agent

```bash
./scripts/vm-audio-stage.sh --vm-share "$HOME" --mode agent
```

### 2) Install in VM

```bash
cd "/Volumes/My Shared Files/.bunghole-vm-audio"
./install.sh
```

See the "Legacy Agent Troubleshooting" section below for TCC permission issues.

## Security

The HAL driver installs on a fresh VM with no security changes required:

- **SIP stays ON.** `/Library/Audio/Plug-Ins/HAL/` is not SIP-protected — it's a standard third-party plugin directory. The only SIP-blocked operation is `launchctl kickstart -k` for system daemons (macOS 14.4+), which is why install.sh uses `killall coreaudiod` instead (launchd auto-respawns it).
- **No TCC permissions.** Unlike the legacy ScreenCaptureKit agent, HAL plugins run inside coreaudiod and don't require Screen Recording or Microphone access.
- **No kernel extension.** HAL plugins are user-space bundles, not kexts. No Secure Boot policy, no Startup Security Utility, no `csrutil disable`.
- **Ad-hoc code signing is sufficient.** The CMake build signs the bundle on the host; SCP preserves the signature. No Apple Developer certificate needed.
- **No dynamic libraries.** libopus is statically linked into the driver binary. macOS 26 runs HAL plugins out-of-process in a helper with AMFI library validation, which rejects ad-hoc signed dylibs — static linking avoids this entirely.

In short: SCP the pre-signed bundle, `sudo cp` to the HAL directory, `sudo killall coreaudiod`. No prompts, no reboots.

## Driver Details

- **Bundle**: `BungholeAudio.driver` installed to `/Library/Audio/Plug-Ins/HAL/`
- **Devices**: "Bunghole Output" (ID 2) and "Bunghole Input" (ID 3)
- **Format**: Float32, 48kHz, stereo, interleaved
- **Ring buffers**: Lock-free SPSC, 8192 frames (~170ms)
- **Opus**: 20ms frames (960 samples), 128 kbps, stereo
- **Clock period**: 480 frames (10ms)
- **Logging**: `os_log` subsystem `com.bunghole.audio` — visible via:
  ```bash
  log stream --predicate 'process == "coreaudiod"' --info
  ```

### Uninstall

```bash
cd "/Volumes/My Shared Files/.bunghole-audio-driver"
sudo ./uninstall.sh
```

## Troubleshooting (Driver)

### A) "Bunghole Output" doesn't appear in Sound settings

1. Verify the driver bundle is installed:
   ```bash
   ls /Library/Audio/Plug-Ins/HAL/BungholeAudio.driver/Contents/MacOS/BungholeAudio
   ```
2. Check coreaudiod logs for load errors:
   ```bash
   log show --predicate 'process == "coreaudiod"' --last 1m --info --debug
   ```
3. Check driver-specific logs:
   ```bash
   log show --predicate 'subsystem == "com.bunghole.audio"' --last 1m --info
   ```
4. Restart coreaudiod manually:
   ```bash
   sudo killall coreaudiod
   ```
   (launchd automatically respawns it; `launchctl kickstart` is blocked by SIP on macOS 14.4+)

### B) Driver loads but "Caught exception trying to add device"

The driver binary may have a stale code signature (common when copying via VirtioFS). Re-codesign and restart:
```bash
sudo codesign --force --sign - --deep /Library/Audio/Plug-Ins/HAL/BungholeAudio.driver
sudo killall coreaudiod
```

If the VM doesn't have Xcode Command Line Tools, use SCP from the host instead of copying via the shared folder:
```bash
# From host:
scp -r build/BungholeAudio.driver user@<vm-ip>:/tmp/
# Inside VM:
sudo rm -rf /Library/Audio/Plug-Ins/HAL/BungholeAudio.driver
sudo cp -R /tmp/BungholeAudio.driver /Library/Audio/Plug-Ins/HAL/
sudo chown -R root:wheel /Library/Audio/Plug-Ins/HAL/BungholeAudio.driver
sudo killall coreaudiod
```

### C) No audio reaching host

1. Confirm "Bunghole Output" is set as default output in System Settings
2. Check driver logs for vsock connection status:
   ```bash
   log show --predicate 'subsystem == "com.bunghole.audio"' --last 1m --info
   ```
   Should show "output vsock connected to host port 5000"
3. Ensure host is running with `--vm` flag (vsock listener active)

### D) Audio glitches/dropouts

- The ring buffer is 8192 frames (~170ms). If the transport thread can't keep up (vsock congestion), audio may underflow.
- Check for "output vsock write failed, reconnecting" in driver logs.

## Legacy Agent Troubleshooting

### Repeated "Open System Settings" / permission prompt loop

LaunchAgent restart behavior with not-yet-effective Screen Recording TCC permission.

Recovery:
```bash
launchctl bootout gui/$(id -u)/com.bunghole.vmaudio 2>/dev/null || true
pkill -f bunghole-vm-audio || true
```
Then re-run `./install.sh` after granting permission and restarting Terminal/session.

### Terminal run works, LaunchAgent does not

TCC grants don't always propagate to LaunchAgent sessions. Try logging out and back in after granting permission.

## Operational Notes

- Audio failures are non-fatal (video continues)
- Opus frame duration: 20ms end-to-end
- Vsock reconnection: driver and host both handle reconnects automatically
- Driver approach eliminates TCC dependency entirely
- By default, guest audio is silently discarded on the host (multi-user friendly). Use `--vm-audio-passthru` to also play guest audio on host speakers.

## macOS Compatibility

The driver supports macOS 15 (Sequoia) and macOS 26 (Tahoe). macOS 26 changed several CoreAudio internals:

- **AudioServerPlugInTypeUUID**: `443ABAB8-...` replaces `443ABEB8-...`
- **AudioServerPlugInDriverInterfaceUUID**: `EEA5773D-...-E7D23B17` replaces `...-35872532`
- **Out-of-process loading**: HAL plugins run inside `com.apple.audio.Core-Audio-Driver-Service.helper`, not in coreaudiod directly. This means AMFI library validation applies — the driver statically links libopus to avoid dylib rejection.
- **`kAudioDevicePropertyIsHidden`**: Required during device activation (absent in earlier macOS versions)

The Info.plist and driver binary include both old and new UUIDs for backward compatibility.
