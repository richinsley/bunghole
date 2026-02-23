//go:build linux

package platform

import (
	"fmt"
	"log"
	"os"

	"bunghole/internal/xserver"

	"golang.org/x/sys/unix"
)

func Init(cfg *Config) (func(), error) {
	if cfg.StartX || cfg.Display == "" {
		if cfg.Display == "" {
			cfg.Display = os.Getenv("DISPLAY")
		}

		if cfg.Display == "" || cfg.StartX {
			xs, err := xserver.StartXServer(cfg.Resolution, cfg.GPU)
			if err != nil {
				return nil, fmt.Errorf("failed to start X server: %v", err)
			}
			cfg.Display = xs.Display
			os.Setenv("DISPLAY", cfg.Display)
			os.Setenv("XAUTHORITY", xs.Xauthority)

			if err := xs.StartDesktopSession(cfg.Resolution); err != nil {
				log.Printf("warning: failed to start desktop session: %v", err)
				log.Printf("X server is running on %s but no desktop — you may want to start one manually", cfg.Display)
			}

			if xs.PulseServer != "" {
				os.Setenv("PULSE_SERVER", xs.PulseServer)
				log.Printf("audio: using %s", xs.PulseServer)
			}

			return func() { xs.Stop() }, nil
		}
	}
	return func() {}, nil
}

var savedTermios *unix.Termios

func SaveTermState() {
	t, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TCGETS)
	if err == nil {
		savedTermios = t
	}
}

func RestoreTermState() {
	if savedTermios != nil {
		unix.IoctlSetTermios(int(os.Stdin.Fd()), unix.TCSETS, savedTermios)
	}
}

// IsVMMode returns false on Linux (VMs are macOS-only).
func IsVMMode() bool { return false }

// VMNSAppRun is a no-op on Linux.
func VMNSAppRun() {}

// VMNSAppStop is a no-op on Linux.
func VMNSAppStop() {}

// RunSetup is a no-op on Linux.
func RunSetup(cfg *Config) {}
