//go:build darwin

package main

/*
#include <stdint.h>

typedef struct {
	void *stream;
	void *delegate;
	void *filter;
	int width;
	int height;
} SCKCaptureHandle;

int  sck_capture_start_window(void *nswindow, int fps, int w, int h, SCKCaptureHandle *out);
int  sck_capture_grab(SCKCaptureHandle *h, uint8_t **buf, int *stride, int *w, int *h_out);
void sck_capture_stop(SCKCaptureHandle *h);
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type VMCapturer struct {
	handle        C.SCKCaptureHandle
	width, height int
}

func NewVMCapturer(window unsafe.Pointer, fps, w, h int) (MediaCapturer, error) {
	var handle C.SCKCaptureHandle
	if ret := C.sck_capture_start_window(window, C.int(fps), C.int(w), C.int(h), &handle); ret != 0 {
		return nil, fmt.Errorf("ScreenCaptureKit window capture failed")
	}
	return &VMCapturer{
		handle: handle,
		width:  w,
		height: h,
	}, nil
}

func (c *VMCapturer) Width() int  { return c.width }
func (c *VMCapturer) Height() int { return c.height }

func (c *VMCapturer) Grab() (*Frame, error) {
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

func (c *VMCapturer) Close() {
	C.sck_capture_stop(&c.handle)
}
