# macOS VM Audio (Guest Agent)

This document covers setup and troubleshooting for bunghole VM-mode audio using the guest helper (`bunghole-vm-audio`) and host UDP ingest (`--audio-udp-listen`).

It is intentionally separate from `ARCHITECTURE_MACOS.md` so we can evolve VM audio workflows/features without cluttering the core architecture doc.

## Goal

Stream macOS guest audio to remote WebRTC clients **without** playing guest audio on host physical speakers.

## Architecture (Current)

- Host bunghole listens for Opus datagrams over UDP (`--audio-udp-listen`)
- Guest helper captures audio with ScreenCaptureKit and sends raw Opus packets to host
- Host forwards received Opus packets to the shared WebRTC audio track

## Quick Start

## 1) Build host + guest components

```bash
cd /path/to/bunghole
cmake -S . -B build
cmake --build build
```

## 2) Stage guest helper into VM shared folder

Recommended (best UX, no typing in guest):

```bash
./scripts/vm-audio-stage.sh --vm-share "$HOME" --udp-target 192.168.0.109:18080
```

Notes:
- If `--udp-target` is omitted, guest installer auto-falls back to `default-gateway:18080`
- Staging includes:
  - `bunghole-vm-audio`
  - `libopus.0.dylib` (rebased to `@loader_path`)
  - install/uninstall scripts + LaunchAgent template

## 3) Start host bunghole with UDP audio ingest

```bash
./build/bunghole --vm --token SECRET --vm-share "$HOME" --audio-udp-listen :18080
```

Expected host log:

```text
audio: listening for guest Opus on udp4://0.0.0.0:18080
audio: source=guest-udp listen=:18080
```

## 4) Install guest helper (inside VM)

```bash
cd "/Volumes/My Shared Files/.bunghole-vm-audio"
./install.sh
```

Installer behavior:
- Resolves UDP target from:
  1. `BUNGHOLE_VM_AUDIO_UDP`
  2. staged `udp_target.txt`
  3. guest default gateway + `:18080`
- Runs one-shot permission probe (`--probe-permission`) before starting LaunchAgent
- Exits with guidance if Screen Recording permission is not truly active yet

## Validation

### Host-side

Look for first packet and non-zero stats:

```text
audio: first guest-udp packet from ...
audio: guest-udp stats pps=... bps=... total_packets=...
```

### Guest-side

```bash
launchctl print gui/$(id -u)/com.bunghole.vmaudio
ps -A | grep bunghole-vm-audio

tail -f ~/Library/Logs/bunghole-vm-audio.log ~/Library/Logs/bunghole-vm-audio.err.log
```

## Troubleshooting

## A) Host shows `pps=0` forever

1. Confirm guest process is running:
   ```bash
   ps -A | grep bunghole-vm-audio
   ```
2. Confirm LaunchAgent args include `--udp=...`:
   ```bash
   launchctl print gui/$(id -u)/com.bunghole.vmaudio | rg -n "ProgramArguments|--udp|state|last exit"
   ```
3. Verify host actually runs with `--audio-udp-listen` and logs `source=guest-udp`
4. Send raw UDP smoke test guest→host:
   ```bash
   GW=$(route -n get default | awk '/gateway:/{print $2}')
   echo ping | nc -u -w1 "$GW" 18080
   ```
   If this reaches host, transport is fine and issue is guest helper startup/capture.

## B) Repeated "Open System Settings" / permission prompt loop

Cause: LaunchAgent restart behavior with not-yet-effective Screen Recording permission.

Current mitigation:
- LaunchAgent `KeepAlive=false`
- Installer permission probe before service start

Recovery:
```bash
launchctl bootout gui/$(id -u)/com.bunghole.vmaudio 2>/dev/null || true
pkill -f bunghole-vm-audio || true
```
Then re-run `./install.sh`.

## C) Permission appears enabled but service still fails

macOS TCC sometimes requires session refresh after granting permission.

Try:
1. Grant permission in System Settings
2. Fully quit Terminal (or log out/in)
3. Re-open Terminal and run `./install.sh` again

## D) Terminal run works, LaunchAgent does not

This is usually still TCC/session state.

Run foreground once for truth output:

```bash
~/Library/Application\ Support/bunghole-vm-audio/bunghole-vm-audio --udp 192.168.0.109:18080 --stats=true
```

If foreground works but LaunchAgent fails, inspect `launchctl print ...` `last exit` + err log and reinstall after session restart.

## E) `libopus.0.dylib not found`

Fixed by current staging flow:
- helper binary uses `@loader_path/libopus.0.dylib`
- dylib shipped alongside helper

If seen again, restage and reinstall:

```bash
./scripts/vm-audio-stage.sh --vm-share "$HOME" --udp-target 192.168.0.109:18080
# inside guest
cd "/Volumes/My Shared Files/.bunghole-vm-audio" && ./uninstall.sh && ./install.sh
```

## F) Guest can ping host LAN IP, but audio still not arriving

Ping only confirms L3 reachability. The common failure is helper not running or missing `--udp` destination.

Check:
- process alive (`ps`)
- LaunchAgent args include `--udp=host:port`
- guest logs show non-zero packet stats

## Operational Notes

- Audio failures should remain non-fatal (video continues)
- Preferred Opus frame duration is 20ms end-to-end
- For manual control:
  - uninstall: `./uninstall.sh`
  - restart agent: `launchctl kickstart -k gui/$(id -u)/com.bunghole.vmaudio`

## Future Work

- Persistent service hardening around TCC transitions
- Better first-run UX around permission acquisition
- Optional health endpoint/heartbeat from guest helper
- Packet framing/version header for stricter host-side validation
