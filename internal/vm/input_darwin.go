//go:build darwin

package vm

/*
#cgo LDFLAGS: -framework Cocoa -framework Virtualization

void vm_input_key(void *view, int keycode, int press);
void vm_input_mouse_move(void *view, double x, double y);
void vm_input_mouse_button(void *view, int button, int press, double x, double y);
void vm_input_scroll(void *view, double dx, double dy, double x, double y);
*/
import "C"
import (
	"log"
	"unsafe"

	"bunghole/internal/input"
	"bunghole/internal/types"
)

type VMInputHandler struct {
	view         unsafe.Pointer
	lastX, lastY float64
}

func NewVMInputHandler(view unsafe.Pointer) types.EventInjector {
	return &VMInputHandler{view: view}
}

func (h *VMInputHandler) Inject(event types.InputEvent) {
	switch event.Type {
	case "mousemove":
		h.lastX = event.X
		h.lastY = event.Y
		C.vm_input_mouse_move(h.view, C.double(event.X), C.double(event.Y))
	case "mousedown":
		h.lastX = event.X
		h.lastY = event.Y
		C.vm_input_mouse_button(h.view, C.int(event.Button), C.int(1),
			C.double(event.X), C.double(event.Y))
	case "mouseup":
		h.lastX = event.X
		h.lastY = event.Y
		C.vm_input_mouse_button(h.view, C.int(event.Button), C.int(0),
			C.double(event.X), C.double(event.Y))
	case "wheel":
		C.vm_input_scroll(h.view, C.double(event.DX), C.double(event.DY),
			C.double(h.lastX), C.double(h.lastY))
	case "keydown":
		if kc, ok := input.CodeMap[event.Code]; ok {
			C.vm_input_key(h.view, C.int(kc), C.int(1))
		} else {
			log.Printf("vm input: unmapped key code=%s key=%s", event.Code, event.Key)
		}
	case "keyup":
		if kc, ok := input.CodeMap[event.Code]; ok {
			C.vm_input_key(h.view, C.int(kc), C.int(0))
		}
	}
}

func (h *VMInputHandler) Close() {}
