package main

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

typedef struct {
	Display *display;
	Window root;
	XShmSegmentInfo shminfo;
	XImage *image;
	int width;
	int height;
} Capturer;

static Capturer* capturer_init(const char *display_name) {
	Capturer *c = (Capturer*)calloc(1, sizeof(Capturer));
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

static int capturer_grab(Capturer *c) {
	if (!XShmGetImage(c->display, c->root, c->image, 0, 0, AllPlanes)) {
		return -1;
	}
	return 0;
}

static void capturer_composite_cursor(Capturer *c) {
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
				dst[0] = cb; // B
				dst[1] = cg; // G
				dst[2] = cr; // R
			} else {
				// Alpha blend
				dst[0] = (cb * a + dst[0] * (255 - a)) / 255;
				dst[1] = (cg * a + dst[1] * (255 - a)) / 255;
				dst[2] = (cr * a + dst[2] * (255 - a)) / 255;
			}
		}
	}
	XFree(cursor);
}

static void capturer_destroy(Capturer *c) {
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
	"time"
	"unsafe"
)

type Frame struct {
	Data   []byte         // populated only for non-zero-copy path
	Ptr    unsafe.Pointer // raw C pointer to BGRA pixel data (zero-copy)
	Width  int
	Height int
	Stride int
}

type Capturer struct {
	c   *C.Capturer
	fps int
}

func NewCapturer(displayName string, fps int) (*Capturer, error) {
	cDisplay := C.CString(displayName)
	defer C.free(unsafe.Pointer(cDisplay))

	c := C.capturer_init(cDisplay)
	if c == nil {
		return nil, fmt.Errorf("failed to initialize X11 screen capture on %s", displayName)
	}

	return &Capturer{c: c, fps: fps}, nil
}

func (c *Capturer) Width() int  { return int(c.c.width) }
func (c *Capturer) Height() int { return int(c.c.height) }

func (c *Capturer) Grab() (*Frame, error) {
	if C.capturer_grab(c.c) != 0 {
		return nil, fmt.Errorf("XShmGetImage failed")
	}
	C.capturer_composite_cursor(c.c)

	// Zero-copy: return pointer to SHM buffer directly.
	// Caller must finish using the frame before the next Grab() call.
	return &Frame{
		Ptr:    unsafe.Pointer(c.c.image.data),
		Width:  int(c.c.width),
		Height: int(c.c.height),
		Stride: int(c.c.image.bytes_per_line),
	}, nil
}

func (c *Capturer) Run(frames chan<- *Frame, stop <-chan struct{}) {
	interval := time.Duration(float64(time.Second) / float64(c.fps))
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			frame, err := c.Grab()
			if err != nil {
				continue
			}
			// Copy for channel-based path since SHM buffer is reused
			size := int(c.c.image.bytes_per_line) * int(c.c.height)
			data := C.GoBytes(unsafe.Pointer(c.c.image.data), C.int(size))
			frame.Data = data
			frame.Ptr = nil
			select {
			case frames <- frame:
			default:
			}
		}
	}
}

func (c *Capturer) Close() {
	C.capturer_destroy(c.c)
}
