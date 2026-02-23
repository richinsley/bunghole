//go:build darwin

package clipboard

/*
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
extern void clip_init(void);
extern void clip_set(const char *text, int len);
extern int clip_check(char **out_text, int *out_len);
extern void clip_destroy(void);
*/
import "C"
import (
	"time"
	"unsafe"

	"bunghole/internal/types"
)

type ClipboardHandler struct {
	lastContent string
	sendFn      func(string)
}

func NewClipboardHandler(displayName string, sendFn func(string)) (types.ClipboardSync, error) {
	C.clip_init()
	return &ClipboardHandler{sendFn: sendFn}, nil
}

func (ch *ClipboardHandler) SetFromClient(text string) {
	ch.lastContent = text
	cText := C.CString(text)
	defer C.free(unsafe.Pointer(cText))
	C.clip_set(cText, C.int(len(text)))
}

func (ch *ClipboardHandler) Run(stop <-chan struct{}) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			var outText *C.char
			var outLen C.int
			if C.clip_check(&outText, &outLen) == 1 && outText != nil {
				text := C.GoStringN(outText, outLen)
				C.free(unsafe.Pointer(outText))
				if text != ch.lastContent {
					ch.lastContent = text
					ch.sendFn(text)
				}
			}
		}
	}
}

func (ch *ClipboardHandler) Close() {
	C.clip_destroy()
}
