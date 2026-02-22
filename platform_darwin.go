//go:build darwin

package main

import (
	"fmt"
	"log"
	"os"
)

func platformInit(display *string, gpu int) (func(), error) {
	if *flagVM {
		path := vmBundlePath()
		if !bundleExists(path) {
			if err := autoProvision(path); err != nil {
				return nil, fmt.Errorf("VM setup failed: %v", err)
			}
		}
		sharedDir := *flagVMShare
		if sharedDir == "" {
			sharedDir, _ = os.UserHomeDir()
		}
		vm, err := NewVMManager(path, sharedDir, 1920, 1080)
		if err != nil {
			return nil, fmt.Errorf("VM create failed: %v", err)
		}
		if err := vm.Start(); err != nil {
			return nil, fmt.Errorf("VM start failed: %v", err)
		}
		globalVM = vm
		*display = "vm"
		log.Printf("VM running (bundle: %s, shared: %s)", path, sharedDir)
		return func() { vm.Stop() }, nil
	}

	if *display == "" {
		*display = "main"
	}
	return func() {}, nil
}
