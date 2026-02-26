package platform

import "net"

// Config holds all platform-related configuration passed from CLI flags.
type Config struct {
	Display    string
	GPU        int
	StartX     bool   // Linux: start a headless Xorg server
	Resolution string // Linux: screen resolution for headless X
	VM              bool   // macOS: run a Virtualization.framework VM
	VMShare         string // macOS: directory to share with VM via VirtioFS
	VMWidth         int    // macOS: VM display width in pixels
	VMHeight        int    // macOS: VM display height in pixels
	VMAudioPassthru bool   // macOS: pass guest audio through to host speakers
	DiskGB          int    // macOS: VM disk size in GB (used with setup)

	VsockAudioCh <-chan net.Conn // macOS VM: vsock audio connections from guest
}
