//go:build darwin

package main

import (
	"flag"
	"fmt"
	"unsafe"

	"bunghole/internal/capture"
	"bunghole/internal/clipboard"
	"bunghole/internal/encode"
	"bunghole/internal/input"
	"bunghole/internal/platform"
	"bunghole/internal/types"
	"bunghole/internal/vm"
)

var (
	flagVM              = flag.Bool("vm", false, "Run macOS VM and stream its display")
	flagVMShare         = flag.String("vm-share", "", "Directory to share with VM via VirtioFS")
	flagVMAudioPassthru = flag.Bool("vm-audio-passthru", false, "Pass VM guest audio through to host speakers")
	flagDisk            = flag.Int("disk", 64, "VM disk size in GB (used with setup)")
)

func registerPlatformFlags() {
	// flags are registered above via flag.Bool/flag.String
}

func fillPlatformConfig(cfg *platform.Config) {
	cfg.VM = *flagVM
	cfg.VMShare = *flagVMShare
	cfg.VMAudioPassthru = *flagVMAudioPassthru
	cfg.DiskGB = *flagDisk

	if cfg.VM {
		var w, h int
		if _, err := fmt.Sscanf(cfg.Resolution, "%dx%d", &w, &h); err != nil || w <= 0 || h <= 0 {
			w, h = 1920, 1080
		}
		cfg.VMWidth = w
		cfg.VMHeight = h
	}
}

func newCapturer(display string, fps, gpu int) (types.MediaCapturer, error) {
	if display == "vm" {
		if g := vm.GetGlobal(); g != nil {
			return vm.NewVMCapturer(g.WindowID, fps, g.Width, g.Height)
		}
	}
	return capture.NewCapturer(display, fps, gpu)
}

func newEncoder(width, height, fps, bitrateKbps, gpu int, codec string, gop int, cudaCtx, cuMemcpy2D unsafe.Pointer) (types.VideoEncoder, error) {
	return encode.NewEncoder(width, height, fps, bitrateKbps, gpu, codec, gop, cudaCtx, cuMemcpy2D)
}

func newInputHandler(displayName string) (types.EventInjector, error) {
	if displayName == "vm" {
		if g := vm.GetGlobal(); g != nil {
			return vm.NewVMInputHandler(g.View()), nil
		}
	}
	return input.NewInputHandler(displayName)
}

func newClipboardHandler(displayName string, sendFn func(string)) (types.ClipboardSync, error) {
	// Clipboard sync deferred for VM mode (needs vsock guest agent)
	if displayName == "vm" {
		return nil, nil
	}
	return clipboard.NewClipboardHandler(displayName, sendFn)
}
