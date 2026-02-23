//go:build darwin

package vm

import (
	"unsafe"

	"bunghole/internal/capture"
	"bunghole/internal/types"
)

// NewVMCapturer creates a capturer for the VM's NSWindow using ScreenCaptureKit.
func NewVMCapturer(window unsafe.Pointer, fps, w, h int) (types.MediaCapturer, error) {
	return capture.NewWindowCapturer(window, fps, w, h)
}
