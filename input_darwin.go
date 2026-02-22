//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>

static int _buttons_down = 0;  // bitmask of held buttons

static void input_mouse_move_abs(int x, int y) {
	CGEventType evtype;
	CGMouseButton button;

	// Use drag event type when a button is held
	if (_buttons_down & 1) {
		evtype = kCGEventLeftMouseDragged;
		button = kCGMouseButtonLeft;
	} else if (_buttons_down & 4) {
		evtype = kCGEventRightMouseDragged;
		button = kCGMouseButtonRight;
	} else if (_buttons_down & 2) {
		evtype = kCGEventOtherMouseDragged;
		button = kCGMouseButtonCenter;
	} else {
		evtype = kCGEventMouseMoved;
		button = kCGMouseButtonLeft;
	}

	CGEventRef ev = CGEventCreateMouseEvent(NULL, evtype,
		CGPointMake(x, y), button);
	CGEventPost(kCGHIDEventTap, ev);
	CFRelease(ev);
}

static void input_mouse_move_rel(int dx, int dy) {
	CGEventRef pos = CGEventCreate(NULL);
	CGPoint cur = CGEventGetLocation(pos);
	CFRelease(pos);
	input_mouse_move_abs((int)(cur.x + dx), (int)(cur.y + dy));
}

static void input_mouse_button(int button, int press, int x, int y) {
	CGEventType evtype;
	CGMouseButton cgbutton;
	int mask;

	if (button == 0) {
		cgbutton = kCGMouseButtonLeft;
		evtype = press ? kCGEventLeftMouseDown : kCGEventLeftMouseUp;
		mask = 1;
	} else if (button == 2) {
		cgbutton = kCGMouseButtonRight;
		evtype = press ? kCGEventRightMouseDown : kCGEventRightMouseUp;
		mask = 4;
	} else {
		cgbutton = kCGMouseButtonCenter;
		evtype = press ? kCGEventOtherMouseDown : kCGEventOtherMouseUp;
		mask = 2;
	}

	if (press) {
		_buttons_down |= mask;
	} else {
		_buttons_down &= ~mask;
	}

	CGEventRef ev = CGEventCreateMouseEvent(NULL, evtype,
		CGPointMake(x, y), cgbutton);
	CGEventPost(kCGHIDEventTap, ev);
	CFRelease(ev);
}

static void input_mouse_scroll(int dx, int dy) {
	// Web deltaY positive = scroll down; macOS expects negated value
	CGEventRef ev = CGEventCreateScrollWheelEvent(NULL,
		kCGScrollEventUnitPixel, 2, -dy, -dx);
	CGEventPost(kCGHIDEventTap, ev);
	CFRelease(ev);
}

static void input_key(int keycode, int press) {
	CGEventRef ev = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)keycode, press);
	CGEventPost(kCGHIDEventTap, ev);
	CFRelease(ev);
}
*/
import "C"
import "log"

type InputHandler struct{}

func NewInputHandler(displayName string) (EventInjector, error) {
	return &InputHandler{}, nil
}

func (ih *InputHandler) Inject(event InputEvent) {
	switch event.Type {
	case "mousemove":
		if event.Relative {
			C.input_mouse_move_rel(C.int(event.DX), C.int(event.DY))
		} else {
			C.input_mouse_move_abs(C.int(event.X), C.int(event.Y))
		}
	case "mousedown":
		C.input_mouse_button(C.int(event.Button), C.int(1), C.int(event.X), C.int(event.Y))
	case "mouseup":
		C.input_mouse_button(C.int(event.Button), C.int(0), C.int(event.X), C.int(event.Y))
	case "wheel":
		C.input_mouse_scroll(C.int(event.DX), C.int(event.DY))
	case "keydown":
		if kc, ok := codeMap[event.Code]; ok {
			C.input_key(C.int(kc), C.int(1))
		} else {
			log.Printf("input: unmapped key code=%s key=%s", event.Code, event.Key)
		}
	case "keyup":
		if kc, ok := codeMap[event.Code]; ok {
			C.input_key(C.int(kc), C.int(0))
		}
	}
}

func (ih *InputHandler) Close() {}

// macOS virtual keycodes (from HIToolbox/Events.h)
const (
	kVK_ANSI_A     = 0x00
	kVK_ANSI_S     = 0x01
	kVK_ANSI_D     = 0x02
	kVK_ANSI_F     = 0x03
	kVK_ANSI_H     = 0x04
	kVK_ANSI_G     = 0x05
	kVK_ANSI_Z     = 0x06
	kVK_ANSI_X     = 0x07
	kVK_ANSI_C     = 0x08
	kVK_ANSI_V     = 0x09
	kVK_ANSI_B     = 0x0B
	kVK_ANSI_Q     = 0x0C
	kVK_ANSI_W     = 0x0D
	kVK_ANSI_E     = 0x0E
	kVK_ANSI_R     = 0x0F
	kVK_ANSI_Y     = 0x10
	kVK_ANSI_T     = 0x11
	kVK_ANSI_1     = 0x12
	kVK_ANSI_2     = 0x13
	kVK_ANSI_3     = 0x14
	kVK_ANSI_4     = 0x15
	kVK_ANSI_6     = 0x16
	kVK_ANSI_5     = 0x17
	kVK_ANSI_Equal = 0x18
	kVK_ANSI_9     = 0x19
	kVK_ANSI_7     = 0x1A
	kVK_ANSI_Minus = 0x1B
	kVK_ANSI_8     = 0x1C
	kVK_ANSI_0     = 0x1D
	kVK_ANSI_RightBracket = 0x1E
	kVK_ANSI_O     = 0x1F
	kVK_ANSI_U     = 0x20
	kVK_ANSI_LeftBracket  = 0x21
	kVK_ANSI_I     = 0x22
	kVK_ANSI_P     = 0x23
	kVK_Return     = 0x24
	kVK_ANSI_L     = 0x25
	kVK_ANSI_J     = 0x26
	kVK_ANSI_Quote = 0x27
	kVK_ANSI_K     = 0x28
	kVK_ANSI_Semicolon    = 0x29
	kVK_ANSI_Backslash    = 0x2A
	kVK_ANSI_Comma = 0x2B
	kVK_ANSI_Slash = 0x2C
	kVK_ANSI_N     = 0x2D
	kVK_ANSI_M     = 0x2E
	kVK_ANSI_Period = 0x2F
	kVK_Tab        = 0x30
	kVK_Space      = 0x31
	kVK_ANSI_Grave = 0x32
	kVK_Delete     = 0x33 // backspace
	kVK_Escape     = 0x35
	kVK_Command    = 0x37
	kVK_Shift      = 0x38
	kVK_CapsLock   = 0x39
	kVK_Option     = 0x3A
	kVK_Control    = 0x3B
	kVK_RightShift   = 0x3C
	kVK_RightOption  = 0x3D
	kVK_RightControl = 0x3E
	kVK_F17        = 0x40
	kVK_VolumeUp   = 0x48
	kVK_VolumeDown = 0x49
	kVK_Mute       = 0x4A
	kVK_F5         = 0x60
	kVK_F6         = 0x61
	kVK_F7         = 0x62
	kVK_F3         = 0x63
	kVK_F8         = 0x64
	kVK_F9         = 0x65
	kVK_F11        = 0x67
	kVK_F13        = 0x69
	kVK_F14        = 0x6B
	kVK_F10        = 0x6D
	kVK_F12        = 0x6F
	kVK_F15        = 0x71
	kVK_Home       = 0x73
	kVK_PageUp     = 0x74
	kVK_ForwardDelete = 0x75
	kVK_F4         = 0x76
	kVK_End        = 0x77
	kVK_F2         = 0x78
	kVK_PageDown   = 0x79
	kVK_F1         = 0x7A
	kVK_LeftArrow  = 0x7B
	kVK_RightArrow = 0x7C
	kVK_DownArrow  = 0x7D
	kVK_UpArrow    = 0x7E
)

var codeMap = map[string]uint16{
	// Letter keys
	"KeyA": kVK_ANSI_A, "KeyB": kVK_ANSI_B, "KeyC": kVK_ANSI_C,
	"KeyD": kVK_ANSI_D, "KeyE": kVK_ANSI_E, "KeyF": kVK_ANSI_F,
	"KeyG": kVK_ANSI_G, "KeyH": kVK_ANSI_H, "KeyI": kVK_ANSI_I,
	"KeyJ": kVK_ANSI_J, "KeyK": kVK_ANSI_K, "KeyL": kVK_ANSI_L,
	"KeyM": kVK_ANSI_M, "KeyN": kVK_ANSI_N, "KeyO": kVK_ANSI_O,
	"KeyP": kVK_ANSI_P, "KeyQ": kVK_ANSI_Q, "KeyR": kVK_ANSI_R,
	"KeyS": kVK_ANSI_S, "KeyT": kVK_ANSI_T, "KeyU": kVK_ANSI_U,
	"KeyV": kVK_ANSI_V, "KeyW": kVK_ANSI_W, "KeyX": kVK_ANSI_X,
	"KeyY": kVK_ANSI_Y, "KeyZ": kVK_ANSI_Z,
	// Digit keys
	"Digit0": kVK_ANSI_0, "Digit1": kVK_ANSI_1, "Digit2": kVK_ANSI_2,
	"Digit3": kVK_ANSI_3, "Digit4": kVK_ANSI_4, "Digit5": kVK_ANSI_5,
	"Digit6": kVK_ANSI_6, "Digit7": kVK_ANSI_7, "Digit8": kVK_ANSI_8,
	"Digit9": kVK_ANSI_9,
	// Function keys
	"F1": kVK_F1, "F2": kVK_F2, "F3": kVK_F3, "F4": kVK_F4,
	"F5": kVK_F5, "F6": kVK_F6, "F7": kVK_F7, "F8": kVK_F8,
	"F9": kVK_F9, "F10": kVK_F10, "F11": kVK_F11, "F12": kVK_F12,
	// Navigation
	"ArrowLeft":  kVK_LeftArrow,
	"ArrowRight": kVK_RightArrow,
	"ArrowUp":    kVK_UpArrow,
	"ArrowDown":  kVK_DownArrow,
	"Home":       kVK_Home,
	"End":        kVK_End,
	"PageUp":     kVK_PageUp,
	"PageDown":   kVK_PageDown,
	// Modifiers
	"ShiftLeft":    kVK_Shift,
	"ShiftRight":   kVK_RightShift,
	"ControlLeft":  kVK_Control,
	"ControlRight": kVK_RightControl,
	"AltLeft":      kVK_Option,
	"AltRight":     kVK_RightOption,
	"MetaLeft":     kVK_Command,
	"MetaRight":    kVK_Command,
	"CapsLock":     kVK_CapsLock,
	// Special keys
	"Backspace":   kVK_Delete,
	"Tab":         kVK_Tab,
	"Enter":       kVK_Return,
	"NumpadEnter": kVK_Return,
	"Escape":      kVK_Escape,
	"Space":       kVK_Space,
	"Delete":      kVK_ForwardDelete,
	"Insert":      kVK_Home, // macOS has no Insert key; map to Home
	// Punctuation
	"Minus":        kVK_ANSI_Minus,
	"Equal":        kVK_ANSI_Equal,
	"BracketLeft":  kVK_ANSI_LeftBracket,
	"BracketRight": kVK_ANSI_RightBracket,
	"Backslash":    kVK_ANSI_Backslash,
	"Semicolon":    kVK_ANSI_Semicolon,
	"Quote":        kVK_ANSI_Quote,
	"Backquote":    kVK_ANSI_Grave,
	"Comma":        kVK_ANSI_Comma,
	"Period":       kVK_ANSI_Period,
	"Slash":        kVK_ANSI_Slash,
}
