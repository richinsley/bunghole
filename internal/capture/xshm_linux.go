//go:build linux

package capture

/*
#cgo pkg-config: x11 xext xfixes
#include <X11/Xlib.h>
#include <X11/Xutil.h>
#include <X11/extensions/XShm.h>
#include <X11/extensions/Xfixes.h>
#include <sys/ipc.h>
#include <sys/shm.h>
#include <stdlib.h>
#include <string.h>

// ---------------------------------------------------------------------------
// XShm capturer (fallback when NvFBC is unavailable)
// ---------------------------------------------------------------------------

typedef struct {
	Display *display;
	Window root;
	XShmSegmentInfo shminfo;
	XImage *image;
	int width;
	int height;
} XShmCapturer;

static XShmCapturer* xshm_init(const char *display_name) {
	XShmCapturer *c = (XShmCapturer*)calloc(1, sizeof(XShmCapturer));
	if (!c) return NULL;

	c->display = XOpenDisplay(display_name);
	if (!c->display) { free(c); return NULL; }

	int screen = DefaultScreen(c->display);
	c->root = RootWindow(c->display, screen);
	c->width = DisplayWidth(c->display, screen);
	c->height = DisplayHeight(c->display, screen);

	c->image = XShmCreateImage(c->display,
		DefaultVisual(c->display, screen),
		DefaultDepth(c->display, screen),
		ZPixmap, NULL, &c->shminfo,
		c->width, c->height);
	if (!c->image) {
		XCloseDisplay(c->display);
		free(c);
		return NULL;
	}

	c->shminfo.shmid = shmget(IPC_PRIVATE,
		c->image->bytes_per_line * c->image->height,
		IPC_CREAT | 0600);
	if (c->shminfo.shmid < 0) {
		XDestroyImage(c->image);
		XCloseDisplay(c->display);
		free(c);
		return NULL;
	}

	c->shminfo.shmaddr = c->image->data = (char*)shmat(c->shminfo.shmid, NULL, 0);
	c->shminfo.readOnly = False;

	if (!XShmAttach(c->display, &c->shminfo)) {
		shmdt(c->shminfo.shmaddr);
		shmctl(c->shminfo.shmid, IPC_RMID, NULL);
		XDestroyImage(c->image);
		XCloseDisplay(c->display);
		free(c);
		return NULL;
	}

	// Mark for removal so it's cleaned up when we detach
	shmctl(c->shminfo.shmid, IPC_RMID, NULL);

	return c;
}

static int xshm_grab(XShmCapturer *c) {
	if (!XShmGetImage(c->display, c->root, c->image, 0, 0, AllPlanes)) {
		return -1;
	}
	XSync(c->display, False);
	return 0;
}

static void xshm_composite_cursor(XShmCapturer *c) {
	XFixesCursorImage *cursor = XFixesGetCursorImage(c->display);
	if (!cursor) return;

	int cx = cursor->x - cursor->xhot;
	int cy = cursor->y - cursor->yhot;

	for (int y = 0; y < (int)cursor->height; y++) {
		int dy = cy + y;
		if (dy < 0 || dy >= c->height) continue;
		for (int x = 0; x < (int)cursor->width; x++) {
			int dx = cx + x;
			if (dx < 0 || dx >= c->width) continue;

			unsigned long pixel = cursor->pixels[y * cursor->width + x];
			unsigned char a = (pixel >> 24) & 0xFF;
			if (a == 0) continue;

			unsigned char cr = (pixel >> 0) & 0xFF;
			unsigned char cg = (pixel >> 8) & 0xFF;
			unsigned char cb = (pixel >> 16) & 0xFF;

			int offset = dy * c->image->bytes_per_line + dx * 4;
			unsigned char *dst = (unsigned char*)c->image->data + offset;

			if (a == 255) {
				dst[0] = cb;
				dst[1] = cg;
				dst[2] = cr;
			} else {
				dst[0] = (cb * a + dst[0] * (255 - a)) / 255;
				dst[1] = (cg * a + dst[1] * (255 - a)) / 255;
				dst[2] = (cr * a + dst[2] * (255 - a)) / 255;
			}
		}
	}
	XFree(cursor);
}

static void xshm_destroy(XShmCapturer *c) {
	if (!c) return;
	XShmDetach(c->display, &c->shminfo);
	shmdt(c->shminfo.shmaddr);
	XDestroyImage(c->image);
	XCloseDisplay(c->display);
	free(c);
}
*/
import "C"
import (
	"fmt"
	"image"
	"image/color"
	"log"
	"unsafe"

	"bunghole/internal/types"
)

// XshmCapturer captures frames via X11 shared memory (CPU fallback).
type XshmCapturer struct {
	c   *C.XShmCapturer
	fps int
}

// NewCapturer creates an XShm screen capturer.
func NewCapturer(displayName string, fps, gpu int) (types.MediaCapturer, error) {
	cDisplay := C.CString(displayName)
	defer C.free(unsafe.Pointer(cDisplay))

	xshm := C.xshm_init(cDisplay)
	if xshm == nil {
		return nil, fmt.Errorf("failed to initialize XShm capture on %s", displayName)
	}
	log.Printf("capture: XShm (%dx%d)", int(xshm.width), int(xshm.height))
	return &XshmCapturer{c: xshm, fps: fps}, nil
}

func (c *XshmCapturer) Width() int  { return int(c.c.width) }
func (c *XshmCapturer) Height() int { return int(c.c.height) }

func (c *XshmCapturer) Grab() (*types.Frame, error) {
	if C.xshm_grab(c.c) != 0 {
		return nil, fmt.Errorf("XShmGetImage failed")
	}
	C.xshm_composite_cursor(c.c)

	return &types.Frame{
		Ptr:    unsafe.Pointer(c.c.image.data),
		Width:  int(c.c.width),
		Height: int(c.c.height),
		Stride: int(c.c.image.bytes_per_line),
	}, nil
}

// GrabImage grabs a frame and returns it as a Go image (for debug endpoint).
func (c *XshmCapturer) GrabImage() (image.Image, error) {
	if C.xshm_grab(c.c) != 0 {
		return nil, fmt.Errorf("XShmGetImage failed")
	}
	C.xshm_composite_cursor(c.c)
	w := int(c.c.width)
	h := int(c.c.height)
	stride := int(c.c.image.bytes_per_line)
	size := stride * h
	bgra := C.GoBytes(unsafe.Pointer(c.c.image.data), C.int(size))
	return bgraToImage(bgra, w, h, stride), nil
}

func (c *XshmCapturer) Close() {
	C.xshm_destroy(c.c)
}

// bgraToImage converts BGRA pixel data to an RGBA image.
func bgraToImage(bgra []byte, w, h, stride int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			off := y*stride + x*4
			img.SetRGBA(x, y, color.RGBA{bgra[off+2], bgra[off+1], bgra[off], 255})
		}
	}
	return img
}
