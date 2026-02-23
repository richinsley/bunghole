//go:build darwin

package platform

import (
	"fmt"
	"log"
	"os"

	"bunghole/internal/vm"
)

func Init(cfg *Config) (func(), error) {
	if cfg.VM {
		path := vm.BundlePath()
		if !vm.BundleExists(path) {
			if err := vm.AutoProvision(path); err != nil {
				return nil, fmt.Errorf("VM setup failed: %v", err)
			}
		}
		sharedDir := cfg.VMShare
		if sharedDir == "" {
			sharedDir, _ = os.UserHomeDir()
		}
		mgr, err := vm.NewVMManager(path, sharedDir, 1920, 1080)
		if err != nil {
			return nil, fmt.Errorf("VM create failed: %v", err)
		}
		if err := mgr.Start(); err != nil {
			return nil, fmt.Errorf("VM start failed: %v", err)
		}
		vm.SetGlobal(mgr)
		cfg.Display = "vm"
		log.Printf("VM running (bundle: %s, shared: %s)", path, sharedDir)
		return func() { mgr.Stop() }, nil
	}

	if cfg.Display == "" {
		cfg.Display = "main"
	}
	return func() {}, nil
}

func SaveTermState()    {}
func RestoreTermState() {}

func IsVMMode() bool { return vm.GetGlobal() != nil }

func VMNSAppRun() { vm.NSAppRun() }

func VMNSAppStop() { vm.NSAppStop() }

func RunSetup(cfg *Config) {
	vm.RunSetup(cfg.DiskGB)
}
