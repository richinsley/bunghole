//go:build linux

package xserver

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
	checkHeadlessPrereqs()
	cleanStaleXorgProcesses()

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

	// Add nvidia module path if the driver is installed outside the
	// default Xorg module directory (common with nvidia-580+ packages).
	if nvidiaModPath := findNvidiaModulePath(); nvidiaModPath != "" {
		xorgArgs = append(xorgArgs, "-modulepath",
			nvidiaModPath+",/usr/lib/xorg/modules")
	}

	log.Printf("starting Xorg on %s (vt%d, gpu %d)", display, vtNum, gpu)
	xorgCmd := exec.Command("Xorg", xorgArgs...)

	xorgLog, err := os.Create(filepath.Join(tmpDir, "xorg.log"))
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("create xorg log: %w", err)
	}
	xorgCmd.Stdout = xorgLog
	xorgCmd.Stderr = xorgLog
	xorgCmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:    true,
		Pdeathsig: syscall.SIGTERM,
	}

	if err := xorgCmd.Start(); err != nil {
		xorgLog.Close()
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

func (xs *XServer) StartDesktopSession(resolution, runAsUser string) error {
	log.Printf("starting desktop session on %s", xs.Display)

	// Patch gnome-shell's loginManager.js to handle null Display property.
	overlayEnv := patchGnomeShellJS(xs.tmpDir)

	// Build session environment, optionally overriding user identity.
	var sessionEnv []string
	var cred *syscall.Credential

	if runAsUser != "" {
		u, err := user.Lookup(runAsUser)
		if err != nil {
			return fmt.Errorf("lookup user %q: %w", runAsUser, err)
		}
		uid, _ := strconv.ParseUint(u.Uid, 10, 32)
		gid, _ := strconv.ParseUint(u.Gid, 10, 32)
		cred = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
		log.Printf("desktop session will run as %s (uid=%d gid=%d)", runAsUser, uid, gid)

		// Filter identity-related vars from inherited env, then set target user's values.
		for _, e := range os.Environ() {
			k := e[:strings.IndexByte(e, '=')]
			switch k {
			case "HOME", "USER", "LOGNAME", "XDG_RUNTIME_DIR":
				continue
			}
			sessionEnv = append(sessionEnv, e)
		}
		sessionEnv = append(sessionEnv,
			"HOME="+u.HomeDir,
			"USER="+u.Username,
			"LOGNAME="+u.Username,
		)
	} else {
		sessionEnv = append([]string(nil), os.Environ()...)
	}

	sessionEnv = append(sessionEnv,
		"DISPLAY="+xs.Display,
		"XAUTHORITY="+xs.Xauthority,
		"XDG_SESSION_TYPE=x11",
		"XDG_CURRENT_DESKTOP=pop:GNOME",
		"XDG_SESSION_DESKTOP=pop",
		"GNOME_SHELL_SESSION_MODE=pop",
		"GDK_BACKEND=x11",
	)
	if overlayEnv != "" {
		sessionEnv = append(sessionEnv, "G_RESOURCE_OVERLAYS="+overlayEnv)
	}

	pwRuntimeDir := filepath.Join(xs.tmpDir, "runtime")
	os.MkdirAll(pwRuntimeDir, 0700)
	xs.PulseServer = fmt.Sprintf("unix:%s/pulse/native", pwRuntimeDir)

	// If dropping privileges, make the tmpDir traversable and let the target
	// user own the runtime dir and read Xauthority.
	if cred != nil {
		os.Chmod(xs.tmpDir, 0755)
		os.Chown(pwRuntimeDir, int(cred.Uid), int(cred.Gid))
		os.Chmod(xs.Xauthority, 0644)
	}

	launcherPath := filepath.Join(xs.tmpDir, "launch-desktop.sh")
	launcher := fmt.Sprintf(`#!/bin/bash
export XDG_RUNTIME_DIR="%s"

# Disable lock screen and screensaver so the desktop is immediately visible
gsettings set org.gnome.desktop.screensaver lock-enabled false 2>/dev/null
gsettings set org.gnome.desktop.screensaver idle-activation-enabled false 2>/dev/null
gsettings set org.gnome.desktop.session idle-delay 0 2>/dev/null
gsettings set org.gnome.desktop.lockdown disable-lock-screen true 2>/dev/null

# Start PipeWire + WirePlumber + PipeWire-Pulse
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

	sessionLog, err := os.Create(filepath.Join(xs.tmpDir, "session.log"))
	if err != nil {
		return fmt.Errorf("create session log: %w", err)
	}
	cmd.Stdout = sessionLog
	cmd.Stderr = sessionLog
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:    true,
		Pdeathsig: syscall.SIGTERM,
	}
	if cred != nil {
		cmd.SysProcAttr.Credential = cred
	}

	if err := cmd.Start(); err != nil {
		sessionLog.Close()
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
		xs.xorgCmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- xs.xorgCmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			xs.xorgCmd.Process.Kill()
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
		// Check if Xorg exited early
		if xs.xorgCmd.ProcessState != nil {
			break
		}
		socketPath := fmt.Sprintf("/tmp/.X11-unix/X%s", strings.TrimPrefix(xs.Display, ":"))
		if _, err := os.Stat(socketPath); err == nil {
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
	// Dump Xorg log so the failure reason is visible
	logPath := filepath.Join(xs.tmpDir, "xorg.log")
	if data, err := os.ReadFile(logPath); err == nil && len(data) > 0 {
		log.Printf("--- Xorg log ---\n%s--- end Xorg log ---", data)
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

func patchGnomeShellJS(tmpDir string) string {
	overlayDir := filepath.Join(tmpDir, "gnome-overlay", "misc")
	os.MkdirAll(overlayDir, 0755)

	out, err := exec.Command("gresource", "extract",
		"/usr/lib/gnome-shell/libgnome-shell.so",
		"/org/gnome/shell/misc/loginManager.js").Output()
	if err != nil {
		log.Printf("warning: can't extract loginManager.js: %v", err)
		return ""
	}

	js := string(out)

	old1 := "let [session, objectPath] = this._userProxy.Display;\n            if (session) {"
	new1 := "let _display = this._userProxy.Display;\n            let [session, objectPath] = _display || ['', ''];\n            if (session) {"
	if !strings.Contains(js, old1) {
		log.Printf("warning: loginManager.js doesn't match expected pattern (Display), skipping patch")
		return ""
	}
	js = strings.Replace(js, old1, new1, 1)

	old2 := "for ([session, objectPath] of this._userProxy.Sessions) {"
	new2 := "for ([session, objectPath] of (this._userProxy.Sessions || [])) {"
	if strings.Contains(js, old2) {
		js = strings.Replace(js, old2, new2, 1)
	}

	patchedPath := filepath.Join(overlayDir, "loginManager.js")
	if err := os.WriteFile(patchedPath, []byte(js), 0644); err != nil {
		log.Printf("warning: can't write patched loginManager.js: %v", err)
		return ""
	}

	return fmt.Sprintf("/org/gnome/shell=%s", filepath.Join(tmpDir, "gnome-overlay"))
}

func findAvailableVT() int {
	for vt := 7; vt <= 12; vt++ {
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
    Option         "MetaModes" "DFP-0: %s +0+0 {ForceFullCompositionPipeline=On}"
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
	raw, err := getRawGPUBusID(index)
	if err != nil {
		return "", err
	}
	return nvidiaToXorgBusID(raw), nil
}

func getRawGPUBusID(index int) (string, error) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=pci.bus_id", "--format=csv,noheader").Output()
	if err != nil {
		return "", fmt.Errorf("nvidia-smi: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if index >= len(lines) {
		return "", fmt.Errorf("GPU index %d out of range (have %d GPUs)", index, len(lines))
	}

	return strings.TrimSpace(lines[index]), nil
}

func nvidiaToXorgBusID(nvBusID string) string {
	nvBusID = strings.TrimSpace(nvBusID)

	parts := strings.Split(nvBusID, ":")
	if len(parts) == 3 {
		domain := parts[0]
		bus := parts[1]
		devFunc := strings.Split(parts[2], ".")

		d, _ := strconv.ParseInt(domain, 16, 64)
		b, _ := strconv.ParseInt(bus, 16, 64)
		dev, _ := strconv.ParseInt(devFunc[0], 16, 64)
		fn := int64(0)
		if len(devFunc) > 1 {
			fn, _ = strconv.ParseInt(devFunc[1], 16, 64)
		}

		_ = d
		return fmt.Sprintf("PCI:%d:%d:%d", b, dev, fn)
	}

	return "PCI:" + nvBusID
}

// cleanStaleXorgProcesses finds and kills Xorg processes left behind by
// previous bunghole runs that weren't cleaned up (e.g. bunghole was killed
// with SIGKILL, or the parent process crashed). Orphaned Xorg processes
// hold DRM master and prevent new instances from starting.
func cleanStaleXorgProcesses() {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	myPID := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == myPID {
			continue
		}
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		args := string(cmdline)
		if !strings.Contains(args, "Xorg") || !strings.Contains(args, "bunghole-x-") {
			continue
		}
		log.Printf("killing stale Xorg process %d", pid)
		proc, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		proc.Signal(syscall.SIGTERM)
		// Wait briefly for it to exit
		for i := 0; i < 10; i++ {
			time.Sleep(100 * time.Millisecond)
			if err := proc.Signal(syscall.Signal(0)); err != nil {
				break
			}
		}
	}
	// Clean up any stale lock files and sockets from bunghole temp dirs
	for i := 1; i <= 99; i++ {
		lock := fmt.Sprintf("/tmp/.X%d-lock", i)
		data, err := os.ReadFile(lock)
		if err != nil {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			continue
		}
		// Check if the PID is still alive
		if err := syscall.Kill(pid, 0); err != nil {
			// Process is gone — clean up stale files
			log.Printf("removing stale X lock file for display :%d (pid %d)", i, pid)
			os.Remove(lock)
			os.Remove(fmt.Sprintf("/tmp/.X11-unix/X%d", i))
		}
	}
}

// checkHeadlessPrereqs checks system configuration required for starting
// Xorg from a non-console session (e.g. SSH).
func checkHeadlessPrereqs() {
	if os.Getuid() != 0 {
		log.Printf("warning: --start-x requires root — run with sudo")
	}
}

// findNvidiaModulePath returns the directory containing nvidia_drv.so
// if it lives outside the default Xorg module path (e.g. nvidia-580+
// installs to /usr/lib/x86_64-linux-gnu/nvidia/xorg/).
func findNvidiaModulePath() string {
	// Check default path first — if it's there, no override needed
	if _, err := os.Stat("/usr/lib/xorg/modules/drivers/nvidia_drv.so"); err == nil {
		return ""
	}
	// Known alternate location used by newer nvidia packages
	alt := "/usr/lib/x86_64-linux-gnu/nvidia/xorg"
	if _, err := os.Stat(filepath.Join(alt, "nvidia_drv.so")); err == nil {
		return alt
	}
	return ""
}

