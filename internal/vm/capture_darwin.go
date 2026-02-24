//go:build darwin

package vm

import (
	"bunghole/internal/capture"
	"bunghole/internal/types"
)

// NewVMCapturer creates a capturer for the VM window using ScreenCaptureKit.
func NewVMCapturer(windowID uint32, fps, w, h int) (types.MediaCapturer, error) {
	return capture.NewWindowCapturer(windowID, fps, w, h)
}
