//go:build linux

package main

import (
	"flag"
	"unsafe"

	"bunghole/internal/capture"
	"bunghole/internal/clipboard"
	"bunghole/internal/encode"
	"bunghole/internal/input"
	"bunghole/internal/platform"
	"bunghole/internal/types"
)

var (
	flagStartX            = flag.Bool("start-x", false, "Start a new Xorg server with nvidia driver")
	flagResolution        = flag.String("resolution", "1920x1080", "Screen resolution when starting X server")
	flagExperimentalNvFBC = flag.Bool("experimental-nvfbc", false, "Enable experimental NvFBC capture path (Linux/NVIDIA only)")
)

func registerPlatformFlags() {
	// flags are registered above via flag.Bool/flag.String
}

func fillPlatformConfig(cfg *platform.Config) {
	cfg.StartX = *flagStartX
	cfg.Resolution = *flagResolution
	capture.SetExperimentalNvFBC(*flagExperimentalNvFBC)
}

func newCapturer(display string, fps, gpu int) (types.MediaCapturer, error) {
	return capture.NewCapturer(display, fps, gpu)
}

func newEncoder(width, height, fps, bitrateKbps, gpu int, codec string, gop int, cudaCtx, cuMemcpy2D unsafe.Pointer) (types.VideoEncoder, error) {
	return encode.NewEncoder(width, height, fps, bitrateKbps, gpu, codec, gop, cudaCtx, cuMemcpy2D)
}

func newInputHandler(displayName string) (types.EventInjector, error) {
	return input.NewInputHandler(displayName)
}

func newClipboardHandler(displayName string, sendFn func(string)) (types.ClipboardSync, error) {
	return clipboard.NewClipboardHandler(displayName, sendFn)
}
