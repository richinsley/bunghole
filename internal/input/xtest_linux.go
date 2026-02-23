//go:build linux

package input

/*
#cgo pkg-config: x11 xtst
#include <X11/Xlib.h>
#include <X11/keysym.h>
#include <X11/extensions/XTest.h>
#include <X11/XKBlib.h>
#include <stdlib.h>
#include <string.h>

static Display* input_display = NULL;

static int input_init(const char *display_name) {
	input_display = XOpenDisplay(display_name);
	if (!input_display) return -1;
	return 0;
}

static void input_mouse_move_abs(int x, int y) {
	if (!input_display) return;
	XTestFakeMotionEvent(input_display, DefaultScreen(input_display), x, y, 0);
	XFlush(input_display);
}

static void input_mouse_move_rel(int dx, int dy) {
	if (!input_display) return;
	XWarpPointer(input_display, None, None, 0, 0, 0, 0, dx, dy);
	XFlush(input_display);
}

static void input_mouse_button(int button, int press) {
	if (!input_display) return;
	XTestFakeButtonEvent(input_display, button, press, 0);
	XFlush(input_display);
}

// Accumulate sub-step scroll deltas
static double scroll_accum_x = 0, scroll_accum_y = 0;

static void input_mouse_scroll(double dx, double dy) {
	if (!input_display) return;

	scroll_accum_y += dy;
	scroll_accum_x += dx;

	// Fire scroll events for each 40px of accumulated delta
	while (scroll_accum_y <= -40) {
		XTestFakeButtonEvent(input_display, 4, True, 0);
		XTestFakeButtonEvent(input_display, 4, False, 0);
		scroll_accum_y += 40;
	}
	while (scroll_accum_y >= 40) {
		XTestFakeButtonEvent(input_display, 5, True, 0);
		XTestFakeButtonEvent(input_display, 5, False, 0);
		scroll_accum_y -= 40;
	}
	while (scroll_accum_x <= -40) {
		XTestFakeButtonEvent(input_display, 6, True, 0);
		XTestFakeButtonEvent(input_display, 6, False, 0);
		scroll_accum_x += 40;
	}
	while (scroll_accum_x >= 40) {
		XTestFakeButtonEvent(input_display, 7, True, 0);
		XTestFakeButtonEvent(input_display, 7, False, 0);
		scroll_accum_x -= 40;
	}
	XFlush(input_display);
}

static void input_key(unsigned int keysym, int press) {
	if (!input_display) return;
	KeyCode kc = XKeysymToKeycode(input_display, keysym);
	if (kc == 0) return;
	XTestFakeKeyEvent(input_display, kc, press, 0);
	XFlush(input_display);
}

static void input_destroy() {
	if (input_display) {
		XCloseDisplay(input_display);
		input_display = NULL;
	}
}
*/
import "C"
import (
	"fmt"
	"log"
	"strings"
	"unsafe"

	"bunghole/internal/types"
)

type InputHandler struct{}

func NewInputHandler(displayName string) (types.EventInjector, error) {
	cDisplay := C.CString(displayName)
	defer C.free(unsafe.Pointer(cDisplay))

	if C.input_init(cDisplay) != 0 {
		return nil, fmt.Errorf("failed to open display for input: %s", displayName)
	}
	return &InputHandler{}, nil
}

func (ih *InputHandler) Inject(event types.InputEvent) {
	switch event.Type {
	case "mousemove":
		if event.Relative {
			C.input_mouse_move_rel(C.int(event.X), C.int(event.Y))
		} else {
			C.input_mouse_move_abs(C.int(event.X), C.int(event.Y))
		}
	case "mousedown":
		C.input_mouse_button(C.int(jsButtonToX11(event.Button)), C.int(1))
	case "mouseup":
		C.input_mouse_button(C.int(jsButtonToX11(event.Button)), C.int(0))
	case "wheel":
		C.input_mouse_scroll(C.double(event.DX), C.double(event.DY))
	case "keydown":
		keysym := codeToKeysym(event.Code, event.Key)
		if keysym != 0 {
			C.input_key(C.uint(keysym), C.int(1))
		}
	case "keyup":
		keysym := codeToKeysym(event.Code, event.Key)
		if keysym != 0 {
			C.input_key(C.uint(keysym), C.int(0))
		}
	}
}

func (ih *InputHandler) Close() {
	C.input_destroy()
}

func jsButtonToX11(button int) int {
	switch button {
	case 0:
		return 1 // Left
	case 1:
		return 2 // Middle
	case 2:
		return 3 // Right
	default:
		return 1
	}
}

func codeToKeysym(code, key string) uint {
	// First try the code-based mapping (physical key position)
	if ks, ok := codeMap[code]; ok {
		return ks
	}

	// For single printable characters, use the character directly
	if len(key) == 1 {
		r := rune(key[0])
		if r >= 0x20 && r <= 0x7E {
			return uint(r)
		}
	}

	// Try key name mapping
	if ks, ok := keyMap[strings.ToLower(key)]; ok {
		return ks
	}

	log.Printf("input: unmapped key code=%s key=%s", code, key)
	return 0
}

// X11 keysym constants
const (
	XK_BackSpace   = 0xFF08
	XK_Tab         = 0xFF09
	XK_Return      = 0xFF0D
	XK_Escape      = 0xFF1B
	XK_Delete      = 0xFFFF
	XK_Home        = 0xFF50
	XK_Left        = 0xFF51
	XK_Up          = 0xFF52
	XK_Right       = 0xFF53
	XK_Down        = 0xFF54
	XK_Page_Up     = 0xFF55
	XK_Page_Down   = 0xFF56
	XK_End         = 0xFF57
	XK_Insert      = 0xFF63
	XK_Shift_L     = 0xFFE1
	XK_Shift_R     = 0xFFE2
	XK_Control_L   = 0xFFE3
	XK_Control_R   = 0xFFE4
	XK_Caps_Lock   = 0xFFE5
	XK_Alt_L       = 0xFFE9
	XK_Alt_R       = 0xFFEA
	XK_Super_L     = 0xFFEB
	XK_Super_R     = 0xFFEC
	XK_F1          = 0xFFBE
	XK_F2          = 0xFFBF
	XK_F3          = 0xFFC0
	XK_F4          = 0xFFC1
	XK_F5          = 0xFFC2
	XK_F6          = 0xFFC3
	XK_F7          = 0xFFC4
	XK_F8          = 0xFFC5
	XK_F9          = 0xFFC6
	XK_F10         = 0xFFC7
	XK_F11         = 0xFFC8
	XK_F12         = 0xFFC9
	XK_space       = 0x0020
	XK_Print       = 0xFF61
	XK_Scroll_Lock = 0xFF14
	XK_Pause       = 0xFF13
	XK_Num_Lock    = 0xFF7F
	XK_Menu        = 0xFF67
)

var codeMap = map[string]uint{
	"Backspace":    XK_BackSpace,
	"Tab":          XK_Tab,
	"Enter":        XK_Return,
	"NumpadEnter":  XK_Return,
	"Escape":       XK_Escape,
	"Delete":       XK_Delete,
	"Home":         XK_Home,
	"End":          XK_End,
	"PageUp":       XK_Page_Up,
	"PageDown":     XK_Page_Down,
	"ArrowLeft":    XK_Left,
	"ArrowUp":      XK_Up,
	"ArrowRight":   XK_Right,
	"ArrowDown":    XK_Down,
	"Insert":       XK_Insert,
	"ShiftLeft":    XK_Shift_L,
	"ShiftRight":   XK_Shift_R,
	"ControlLeft":  XK_Control_L,
	"ControlRight": XK_Control_R,
	"CapsLock":     XK_Caps_Lock,
	"AltLeft":      XK_Alt_L,
	"AltRight":     XK_Alt_R,
	"MetaLeft":     XK_Super_L,
	"MetaRight":    XK_Super_R,
	"Space":        XK_space,
	"F1":           XK_F1,
	"F2":           XK_F2,
	"F3":           XK_F3,
	"F4":           XK_F4,
	"F5":           XK_F5,
	"F6":           XK_F6,
	"F7":           XK_F7,
	"F8":           XK_F8,
	"F9":           XK_F9,
	"F10":          XK_F10,
	"F11":          XK_F11,
	"F12":          XK_F12,
	"PrintScreen":  XK_Print,
	"ScrollLock":   XK_Scroll_Lock,
	"Pause":        XK_Pause,
	"NumLock":      XK_Num_Lock,
	"ContextMenu":  XK_Menu,
	// Letter keys
	"KeyA": 'a', "KeyB": 'b', "KeyC": 'c', "KeyD": 'd',
	"KeyE": 'e', "KeyF": 'f', "KeyG": 'g', "KeyH": 'h',
	"KeyI": 'i', "KeyJ": 'j', "KeyK": 'k', "KeyL": 'l',
	"KeyM": 'm', "KeyN": 'n', "KeyO": 'o', "KeyP": 'p',
	"KeyQ": 'q', "KeyR": 'r', "KeyS": 's', "KeyT": 't',
	"KeyU": 'u', "KeyV": 'v', "KeyW": 'w', "KeyX": 'x',
	"KeyY": 'y', "KeyZ": 'z',
	// Digit keys
	"Digit0": '0', "Digit1": '1', "Digit2": '2', "Digit3": '3',
	"Digit4": '4', "Digit5": '5', "Digit6": '6', "Digit7": '7',
	"Digit8": '8', "Digit9": '9',
	// Punctuation
	"Minus":        '-',
	"Equal":        '=',
	"BracketLeft":  '[',
	"BracketRight": ']',
	"Backslash":    '\\',
	"Semicolon":    ';',
	"Quote":        '\'',
	"Backquote":    '`',
	"Comma":        ',',
	"Period":       '.',
	"Slash":        '/',
}

var keyMap = map[string]uint{
	"backspace":  XK_BackSpace,
	"tab":        XK_Tab,
	"enter":      XK_Return,
	"escape":     XK_Escape,
	"delete":     XK_Delete,
	"home":       XK_Home,
	"end":        XK_End,
	"pageup":     XK_Page_Up,
	"pagedown":   XK_Page_Down,
	"arrowleft":  XK_Left,
	"arrowup":    XK_Up,
	"arrowright":  XK_Right,
	"arrowdown":  XK_Down,
	"insert":     XK_Insert,
	"shift":      XK_Shift_L,
	"control":    XK_Control_L,
	"alt":        XK_Alt_L,
	"meta":       XK_Super_L,
	" ":          XK_space,
}
