package platform

// Config holds all platform-related configuration passed from CLI flags.
type Config struct {
	Display    string
	GPU        int
	StartX     bool   // Linux: start a headless Xorg server
	Resolution string // Linux: screen resolution for headless X
	VM         bool   // macOS: run a Virtualization.framework VM
	VMShare    string // macOS: directory to share with VM via VirtioFS
	DiskGB     int    // macOS: VM disk size in GB (used with setup)
}
