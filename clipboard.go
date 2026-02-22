package main

/*
#cgo pkg-config: x11
#include <X11/Xlib.h>
#include <X11/Xatom.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

static Display *clip_display = NULL;
static Window clip_window;
static Atom CLIPBOARD;
static Atom UTF8_STRING;
static Atom TARGETS;
static Atom BUNGHOLE_SEL;
static char *owned_text = NULL;
static int own_len = 0;

static int clip_init(const char *display_name) {
	clip_display = XOpenDisplay(display_name);
	if (!clip_display) return -1;

	CLIPBOARD = XInternAtom(clip_display, "CLIPBOARD", False);
	UTF8_STRING = XInternAtom(clip_display, "UTF8_STRING", False);
	TARGETS = XInternAtom(clip_display, "TARGETS", False);
	BUNGHOLE_SEL = XInternAtom(clip_display, "BUNGHOLE_SEL", False);

	clip_window = XCreateSimpleWindow(clip_display,
		DefaultRootWindow(clip_display),
		0, 0, 1, 1, 0, 0, 0);

	return 0;
}

// Set clipboard content (take ownership)
static void clip_set(const char *text, int len) {
	if (!clip_display) return;

	if (owned_text) free(owned_text);
	owned_text = (char*)malloc(len + 1);
	memcpy(owned_text, text, len);
	owned_text[len] = 0;
	own_len = len;

	XSetSelectionOwner(clip_display, CLIPBOARD, clip_window, CurrentTime);
	XFlush(clip_display);
}

// Request clipboard content from current owner
static void clip_request() {
	if (!clip_display) return;
	XConvertSelection(clip_display, CLIPBOARD, UTF8_STRING, BUNGHOLE_SEL,
		clip_window, CurrentTime);
	XFlush(clip_display);
}

// Process one X event, returns:
//   1 = got clipboard text (stored in out_text/out_len)
//   2 = selection request handled (we served our text to another app)
//   0 = other event
static int clip_process_event(char **out_text, int *out_len) {
	XEvent ev;
	if (!XPending(clip_display)) return 0;

	XNextEvent(clip_display, &ev);

	// We received clipboard data we requested
	if (ev.type == SelectionNotify) {
		if (ev.xselection.property == None) return 0;

		Atom type;
		int format;
		unsigned long nitems, bytes_after;
		unsigned char *data = NULL;

		XGetWindowProperty(clip_display, clip_window, BUNGHOLE_SEL,
			0, 1024*1024, True, AnyPropertyType,
			&type, &format, &nitems, &bytes_after, &data);

		if (data && nitems > 0) {
			*out_text = (char*)malloc(nitems + 1);
			memcpy(*out_text, data, nitems);
			(*out_text)[nitems] = 0;
			*out_len = (int)nitems;
			XFree(data);
			return 1;
		}
		if (data) XFree(data);
		return 0;
	}

	// Another app is requesting our clipboard content
	if (ev.type == SelectionRequest) {
		XSelectionRequestEvent *req = &ev.xselectionrequest;
		XSelectionEvent resp;
		memset(&resp, 0, sizeof(resp));
		resp.type = SelectionNotify;
		resp.requestor = req->requestor;
		resp.selection = req->selection;
		resp.target = req->target;
		resp.time = req->time;
		resp.property = None;

		if (req->target == TARGETS) {
			Atom targets[] = { TARGETS, UTF8_STRING, XA_STRING };
			XChangeProperty(clip_display, req->requestor, req->property,
				XA_ATOM, 32, PropModeReplace,
				(unsigned char*)targets, 3);
			resp.property = req->property;
		} else if ((req->target == UTF8_STRING || req->target == XA_STRING) && owned_text) {
			XChangeProperty(clip_display, req->requestor, req->property,
				req->target, 8, PropModeReplace,
				(unsigned char*)owned_text, own_len);
			resp.property = req->property;
		}

		XSendEvent(clip_display, req->requestor, False, 0, (XEvent*)&resp);
		XFlush(clip_display);
		return 2;
	}

	// Clipboard owner changed (someone else copied something)
	if (ev.type == SelectionClear) {
		// We lost ownership — someone else set the clipboard
		if (owned_text) {
			free(owned_text);
			owned_text = NULL;
			own_len = 0;
		}
	}

	return 0;
}

static void clip_watch_owner_change() {
	if (!clip_display) return;
	// We'll poll for owner changes via XGetSelectionOwner
}

static int clip_we_own() {
	if (!clip_display) return 0;
	return XGetSelectionOwner(clip_display, CLIPBOARD) == clip_window ? 1 : 0;
}

static void clip_destroy() {
	if (!clip_display) return;
	if (owned_text) free(owned_text);
	XDestroyWindow(clip_display, clip_window);
	XCloseDisplay(clip_display);
	clip_display = NULL;
}
*/
import "C"
import (
	"fmt"
	"log"
	"time"
	"unsafe"
)

type ClipboardHandler struct {
	lastContent string
	sendFn      func(string) // callback to send clipboard to client
}

func NewClipboardHandler(displayName string, sendFn func(string)) (*ClipboardHandler, error) {
	cDisplay := C.CString(displayName)
	defer C.free(unsafe.Pointer(cDisplay))

	if C.clip_init(cDisplay) != 0 {
		return nil, fmt.Errorf("failed to open display for clipboard: %s", displayName)
	}

	return &ClipboardHandler{sendFn: sendFn}, nil
}

// SetFromClient sets the X11 clipboard with content received from the browser
func (ch *ClipboardHandler) SetFromClient(text string) {
	ch.lastContent = text
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))
	C.clip_set(cText, C.int(len(text)))
}

// Run monitors the clipboard for changes and processes X events
func (ch *ClipboardHandler) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			// Process any pending X events
			for {
				var outText *C.char
				var outLen C.int
				result := C.clip_process_event(&outText, &outLen)
				if result == 0 {
					break
				}
				if result == 1 && outText != nil {
					text := C.GoStringN(outText, outLen)
					C.free(unsafe.Pointer(outText))
					if text != ch.lastContent {
						ch.lastContent = text
						ch.sendFn(text)
					}
				}
			}

			// If we don't own the clipboard, request its content
			if C.clip_we_own() == 0 {
				C.clip_request()
			}
		}
	}
}

func (ch *ClipboardHandler) Close() {
	C.clip_destroy()
	log.Println("clipboard handler closed")
}
