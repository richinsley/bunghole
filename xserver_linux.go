//go:build linux

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type XServer struct {
	Display     string
	Xauthority  string
	PulseServer string
	xorgCmd     *exec.Cmd
	sessionCmd  *exec.Cmd
	tmpDir      string
}

func StartXServer(resolution string, gpu int) (*XServer, error) {
	// Find an available display number
	displayNum := findAvailableDisplay()
	display := fmt.Sprintf(":%d", displayNum)

	tmpDir, err := os.MkdirTemp("", "bunghole-x-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	xauth := filepath.Join(tmpDir, "Xauthority")

	// Generate xorg.conf for headless nvidia
	confPath := filepath.Join(tmpDir, "xorg.conf")
	if err := writeXorgConf(confPath, resolution, gpu); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("write xorg.conf: %w", err)
	}

	// Generate Xauthority cookie
	cookie := generateXauthCookie()
	xauthCmd := exec.Command("xauth", "-f", xauth, "add", display, "MIT-MAGIC-COOKIE-1", cookie)
	if out, err := xauthCmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("xauth add: %w: %s", err, out)
	}

	// Start Xorg
	vtNum := findAvailableVT()
	xorgArgs := []string{
		display,
		fmt.Sprintf("vt%d", vtNum),
		"-config", confPath,
		"-auth", xauth,
		"-noreset",
		"-keeptty",
		"-novtswitch",
		"-verbose", "3",
	}

	log.Printf("starting Xorg on %s (vt%d, gpu %d)", display, vtNum, gpu)
	xorgCmd := exec.Command("sudo", append([]string{"Xorg"}, xorgArgs...)...)
	xorgCmd.Stdout = os.Stdout
	xorgCmd.Stderr = os.Stderr

	if err := xorgCmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("start Xorg: %w", err)
	}

	xs := &XServer{
		Display:    display,
		Xauthority: xauth,
		xorgCmd:    xorgCmd,
		tmpDir:     tmpDir,
	}

	// Wait for X server to be ready
	if err := xs.waitReady(10 * time.Second); err != nil {
		xs.Stop()
		return nil, fmt.Errorf("Xorg not ready: %w", err)
	}

	log.Printf("Xorg ready on %s", display)
	return xs, nil
}

func (xs *XServer) configureDisplay(resolution string) error {
	env := append(os.Environ(),
		"DISPLAY="+xs.Display,
		"XAUTHORITY="+xs.Xauthority,
	)

	out, err := xs.runCmd(env, "xrandr", "--query")
	if err != nil {
		return fmt.Errorf("xrandr query: %w", err)
	}

	// Find a connected output and its current mode
	var output string
	var currentMode string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "connected" {
			output = fields[0]
			for _, f := range fields[2:] {
				if strings.Contains(f, "+") {
					currentMode = strings.Split(f, "+")[0]
					break
				}
			}
			break
		}
	}

	if output == "" {
		log.Printf("no connected outputs — resolution set by xorg.conf Virtual")
		return nil
	}

	if currentMode == resolution {
		log.Printf("output %s already at %s", output, resolution)
		return nil
	}

	log.Printf("configuring output %s from %s to %s", output, currentMode, resolution)

	// First try setting the mode directly (nvidia provides many built-in modes)
	_, err = xs.runCmd(env, "xrandr", "--output", output, "--mode", resolution)
	if err == nil {
		log.Printf("set %s to %s", output, resolution)
		return nil
	}

	// Mode doesn't exist as built-in — create it via CVT modeline
	log.Printf("built-in mode %s not available, creating via cvt", resolution)
	cvtOut, err := xs.runCmd(env, "cvt",
		strings.Split(resolution, "x")[0],
		strings.Split(resolution, "x")[1],
		"60")
	if err != nil {
		return fmt.Errorf("cvt: %w", err)
	}

	var modeName, modeParams string
	for _, line := range strings.Split(cvtOut, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Modeline") {
			if idx := strings.Index(line, "\""); idx >= 0 {
				end := strings.Index(line[idx+1:], "\"")
				if end >= 0 {
					modeName = line[idx+1 : idx+1+end]
					modeParams = strings.TrimSpace(line[idx+1+end+1:])
				}
			}
		}
	}

	if modeName == "" {
		return fmt.Errorf("cvt produced no modeline for %s", resolution)
	}

	xs.runCmd(env, "xrandr", "--newmode", modeName, modeParams)
	xs.runCmd(env, "xrandr", "--addmode", output, modeName)
	_, err = xs.runCmd(env, "xrandr", "--output", output, "--mode", modeName)
	if err != nil {
		return fmt.Errorf("xrandr set mode %s: %w", modeName, err)
	}

	log.Printf("set %s to %s (custom mode)", output, resolution)
	return nil
}

func (xs *XServer) runCmd(env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (xs *XServer) StartDesktopSession(resolution string) error {
	log.Printf("starting desktop session on %s", xs.Display)

	// Find our logind session ID — gnome-shell's ScreenShield needs this
	// to query the session's Display property via logind. Without it,
	// gnome-shell crashes with "this._userProxy.Display is null".
	sessionID := findLogindSession()

	sessionEnv := append(os.Environ(),
		"DISPLAY="+xs.Display,
		"XAUTHORITY="+xs.Xauthority,
		"XDG_SESSION_TYPE=x11",
		"XDG_CURRENT_DESKTOP=pop:GNOME",
		"XDG_SESSION_DESKTOP=pop",
		"GNOME_SHELL_SESSION_MODE=pop",
		"GDK_BACKEND=x11",
	)
	if sessionID != "" {
		sessionEnv = append(sessionEnv, "XDG_SESSION_ID="+sessionID)
	}

	// Write a launcher script that starts PipeWire (for audio) and gnome-shell
	// inside the same dbus session. gnome-shell's mixer needs PipeWire on its
	// dbus session to show audio devices.
	// We use a private XDG_RUNTIME_DIR so this PipeWire doesn't conflict with
	// the user's existing PipeWire from the SSH session.
	pwRuntimeDir := filepath.Join(xs.tmpDir, "runtime")
	os.MkdirAll(pwRuntimeDir, 0700)
	xs.PulseServer = fmt.Sprintf("unix:%s/pulse/native", pwRuntimeDir)

	launcherPath := filepath.Join(xs.tmpDir, "launch-desktop.sh")
	launcher := fmt.Sprintf(`#!/bin/bash
export XDG_RUNTIME_DIR="%s"

# Start PipeWire + WirePlumber (session manager) + PipeWire-Pulse
# WirePlumber is needed so audio routing/playback works properly.
# ALSA devices won't appear (udev doesn't work in dbus-run-session)
# but the auto_null sink handles headless audio fine.
pipewire &
sleep 0.3
wireplumber &
sleep 0.3
pipewire-pulse &
sleep 1

# Start gnome-shell
exec gnome-shell --x11
`, pwRuntimeDir)
	os.WriteFile(launcherPath, []byte(launcher), 0755)

	cmd := exec.Command("dbus-run-session", "--", "bash", launcherPath)
	cmd.Env = sessionEnv
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start gnome-shell: %w", err)
	}
	xs.sessionCmd = cmd

	// Wait for gnome-shell to be ready by checking for _NET_SUPPORTING_WM_CHECK
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		checkCmd := exec.Command("xprop", "-root", "_NET_SUPPORTING_WM_CHECK")
		checkCmd.Env = append(os.Environ(),
			"DISPLAY="+xs.Display,
			"XAUTHORITY="+xs.Xauthority,
		)
		if out, err := checkCmd.Output(); err == nil && strings.Contains(string(out), "window id") {
			log.Printf("GNOME Shell is ready on %s", xs.Display)
			// Configure resolution AFTER gnome-shell starts, because mutter
			// resets the display to its own preferred mode on startup.
			if err := xs.configureDisplay(resolution); err != nil {
				log.Printf("warning: display config failed: %v", err)
			}
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	log.Printf("desktop session started on %s (gnome-shell may still be initializing)", xs.Display)
	return nil
}

func (xs *XServer) Stop() {
	if xs.sessionCmd != nil && xs.sessionCmd.Process != nil {
		log.Printf("stopping desktop session")
		xs.sessionCmd.Process.Signal(os.Interrupt)
		done := make(chan error, 1)
		go func() { done <- xs.sessionCmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			xs.sessionCmd.Process.Kill()
		}
	}

	if xs.xorgCmd != nil && xs.xorgCmd.Process != nil {
		log.Printf("stopping Xorg")
		// Xorg was started with sudo, so we need sudo to kill it
		exec.Command("sudo", "kill", strconv.Itoa(xs.xorgCmd.Process.Pid)).Run()
		done := make(chan error, 1)
		go func() { done <- xs.xorgCmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			exec.Command("sudo", "kill", "-9", strconv.Itoa(xs.xorgCmd.Process.Pid)).Run()
		}
	}

	// Clean up lock file and socket
	displayNum := strings.TrimPrefix(xs.Display, ":")
	os.Remove(fmt.Sprintf("/tmp/.X%s-lock", displayNum))
	os.Remove(fmt.Sprintf("/tmp/.X11-unix/X%s", displayNum))

	if xs.tmpDir != "" {
		os.RemoveAll(xs.tmpDir)
	}
}

func (xs *XServer) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		socketPath := fmt.Sprintf("/tmp/.X11-unix/X%s", strings.TrimPrefix(xs.Display, ":"))
		if _, err := os.Stat(socketPath); err == nil {
			// Also verify we can connect
			cmd := exec.Command("xdpyinfo")
			cmd.Env = append(os.Environ(),
				"DISPLAY="+xs.Display,
				"XAUTHORITY="+xs.Xauthority,
			)
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for X server on %s", xs.Display)
}

func findAvailableDisplay() int {
	for i := 1; i <= 99; i++ {
		socket := fmt.Sprintf("/tmp/.X11-unix/X%d", i)
		lock := fmt.Sprintf("/tmp/.X%d-lock", i)
		_, sockErr := os.Stat(socket)
		_, lockErr := os.Stat(lock)
		if os.IsNotExist(sockErr) && os.IsNotExist(lockErr) {
			return i
		}
	}
	return 99
}

func findLogindSession() string {
	// Find a logind session for the current user
	uid := strconv.Itoa(os.Getuid())
	out, err := exec.Command("loginctl", "list-sessions", "--no-legend", "--no-pager").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[1] == uid {
			return fields[0]
		}
	}
	return ""
}

func findAvailableVT() int {
	// Try to find a free VT, start from 7 (typical for X)
	for vt := 7; vt <= 12; vt++ {
		// Check if any Xorg is already using this VT
		out, _ := exec.Command("fgconsole").Output()
		currentVT, _ := strconv.Atoi(strings.TrimSpace(string(out)))
		if vt != currentVT {
			return vt
		}
	}
	return 8
}

func generateXauthCookie() string {
	f, err := os.Open("/dev/urandom")
	if err != nil {
		return "deadbeefdeadbeefdeadbeefdeadbeef"
	}
	defer f.Close()
	buf := make([]byte, 16)
	f.Read(buf)
	return fmt.Sprintf("%x", buf)
}

func writeXorgConf(path, resolution string, gpuIndex int) error {
	// Query GPU BusID
	busID, err := getGPUBusID(gpuIndex)
	if err != nil {
		return err
	}

	conf := fmt.Sprintf(`Section "ServerLayout"
    Identifier     "Layout0"
    Screen      0  "Screen0"
EndSection

Section "Device"
    Identifier     "Device0"
    Driver         "nvidia"
    BusID          "%s"
    Option         "AllowEmptyInitialConfiguration" "True"
    Option         "ConnectedMonitor" "DFP-0"
    Option         "ModeValidation" "NoEdidModes, NoMaxPClkCheck, NoHorizSyncCheck, NoVertRefreshCheck, NoMaxSizeCheck"
EndSection

Section "Screen"
    Identifier     "Screen0"
    Device         "Device0"
    Monitor        "Monitor0"
    DefaultDepth   24
    Option         "MetaModes" "DFP-0: %s +0+0"
    SubSection "Display"
        Depth      24
        Virtual    %s
    EndSubSection
EndSection

Section "Monitor"
    Identifier     "Monitor0"
    Option         "Enable" "true"
EndSection
`, busID, resolution, strings.ReplaceAll(resolution, "x", " "))

	return os.WriteFile(path, []byte(conf), 0644)
}

func getGPUBusID(index int) (string, error) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=pci.bus_id", "--format=csv,noheader").Output()
	if err != nil {
		return "", fmt.Errorf("nvidia-smi: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if index >= len(lines) {
		return "", fmt.Errorf("GPU index %d out of range (have %d GPUs)", index, len(lines))
	}

	// nvidia-smi returns format like "00000081:00:00.0", Xorg wants "PCI:129:0:0"
	busID := strings.TrimSpace(lines[index])
	return nvidiaToXorgBusID(busID), nil
}

func nvidiaToXorgBusID(nvBusID string) string {
	// Input: "00000081:00:00.0" or similar
	// Output: "PCI:129:0:0"
	nvBusID = strings.TrimSpace(nvBusID)

	// Remove domain prefix (everything before first colon that's a long hex)
	parts := strings.Split(nvBusID, ":")
	if len(parts) == 3 {
		// "00000081:00:00.0" -> domain:bus:dev.func
		domain := parts[0]
		bus := parts[1]
		devFunc := strings.Split(parts[2], ".")

		// Parse hex values
		d, _ := strconv.ParseInt(domain, 16, 64)
		b, _ := strconv.ParseInt(bus, 16, 64)
		dev, _ := strconv.ParseInt(devFunc[0], 16, 64)
		fn := int64(0)
		if len(devFunc) > 1 {
			fn, _ = strconv.ParseInt(devFunc[1], 16, 64)
		}

		_ = d // domain is typically 0 for PCI bus ID in Xorg
		return fmt.Sprintf("PCI:%d:%d:%d", b, dev, fn)
	}

	// Fallback: try to parse as-is
	return "PCI:" + nvBusID
}

