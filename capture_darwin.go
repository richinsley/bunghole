//go:build darwin

package main

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
int  sck_capture_grab(SCKCaptureHandle *h, uint8_t **buf, int *stride, int *w, int *h_out);
void sck_capture_stop(SCKCaptureHandle *h);
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type Capturer struct {
	handle C.SCKCaptureHandle
}

func NewCapturer(displayName string, fps int) (MediaCapturer, error) {
	var handle C.SCKCaptureHandle
	if ret := C.sck_capture_start_display(C.int(fps), &handle); ret != 0 {
		return nil, fmt.Errorf("ScreenCaptureKit display capture failed")
	}
	return &Capturer{handle: handle}, nil
}

func (c *Capturer) Width() int  { return int(c.handle.width) }
func (c *Capturer) Height() int { return int(c.handle.height) }

func (c *Capturer) Grab() (*Frame, error) {
	var buf *C.uint8_t
	var stride, w, h C.int

	if ret := C.sck_capture_grab(&c.handle, &buf, &stride, &w, &h); ret != 0 {
		return nil, fmt.Errorf("no frame available")
	}

	return &Frame{
		Ptr:    unsafe.Pointer(buf),
		Width:  int(w),
		Height: int(h),
		Stride: int(stride),
	}, nil
}

func (c *Capturer) Close() {
	C.sck_capture_stop(&c.handle)
}
