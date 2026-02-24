//go:build darwin

package capture

/*
#cgo CFLAGS: -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo -framework Cocoa

#include <stdint.h>

typedef struct {
	void *stream;
	void *delegate;
	void *filter;
	int width;
	int height;
} SCKCaptureHandle;

int  sck_capture_start_display(int fps, SCKCaptureHandle *out);
int  sck_capture_start_window(uint32_t window_id, int fps, int w, int h, SCKCaptureHandle *out);
int  sck_capture_grab(SCKCaptureHandle *h, uint8_t **buf, int *stride, int *w, int *h_out);
void sck_capture_stop(SCKCaptureHandle *h);
*/
import "C"
import (
	"fmt"
	"unsafe"

	"bunghole/internal/types"
)

// DisplayCapturer wraps ScreenCaptureKit display capture.
type DisplayCapturer struct {
	handle C.SCKCaptureHandle
}

// NewCapturer creates a ScreenCaptureKit display capturer.
func NewCapturer(displayName string, fps, gpu int) (types.MediaCapturer, error) {
	var handle C.SCKCaptureHandle
	if ret := C.sck_capture_start_display(C.int(fps), &handle); ret != 0 {
		return nil, fmt.Errorf("ScreenCaptureKit display capture failed")
	}
	return &DisplayCapturer{handle: handle}, nil
}

func (c *DisplayCapturer) Width() int  { return int(c.handle.width) }
func (c *DisplayCapturer) Height() int { return int(c.handle.height) }

func (c *DisplayCapturer) Grab() (*types.Frame, error) {
	var buf *C.uint8_t
	var stride, w, h C.int

	if ret := C.sck_capture_grab(&c.handle, &buf, &stride, &w, &h); ret != 0 {
		return nil, fmt.Errorf("no frame available")
	}

	return &types.Frame{
		Ptr:    unsafe.Pointer(buf),
		Width:  int(w),
		Height: int(h),
		Stride: int(stride),
	}, nil
}

func (c *DisplayCapturer) Close() {
	C.sck_capture_stop(&c.handle)
}

// WindowCapturer wraps ScreenCaptureKit window capture (used for VM mode).
type WindowCapturer struct {
	handle        C.SCKCaptureHandle
	width, height int
}

// NewWindowCapturer creates a ScreenCaptureKit window capturer.
func NewWindowCapturer(windowID uint32, fps, w, h int) (types.MediaCapturer, error) {
	if windowID == 0 {
		return nil, fmt.Errorf("invalid window id")
	}
	var handle C.SCKCaptureHandle
	if ret := C.sck_capture_start_window(C.uint32_t(windowID), C.int(fps), C.int(w), C.int(h), &handle); ret != 0 {
		return nil, fmt.Errorf("ScreenCaptureKit window capture failed")
	}
	return &WindowCapturer{
		handle: handle,
		width:  w,
		height: h,
	}, nil
}

func (c *WindowCapturer) Width() int  { return c.width }
func (c *WindowCapturer) Height() int { return c.height }

func (c *WindowCapturer) Grab() (*types.Frame, error) {
	var buf *C.uint8_t
	var stride, w, h C.int

	if ret := C.sck_capture_grab(&c.handle, &buf, &stride, &w, &h); ret != 0 {
		return nil, fmt.Errorf("no frame available")
	}

	return &types.Frame{
		Ptr:    unsafe.Pointer(buf),
		Width:  int(w),
		Height: int(h),
		Stride: int(stride),
	}, nil
}

func (c *WindowCapturer) Close() {
	C.sck_capture_stop(&c.handle)
}
