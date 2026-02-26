//go:build darwin

package vm

/*
#cgo CFLAGS: -mmacosx-version-min=14.0 -fobjc-arc
#cgo LDFLAGS: -framework Virtualization

#include <stdint.h>

int  vm_vsock_listen(void *vm_ptr, uint32_t port);
void vm_vsock_stop(void *vm_ptr, uint32_t port);
*/
import "C"
import (
	"fmt"
	"net"
	"os"
	"sync"
	"unsafe"
)

var (
	vsockConnCh chan net.Conn
	vsockMu     sync.Mutex
)

// StartVsockListener starts listening for vsock connections on the given port.
// Returns a channel that delivers accepted connections.
func StartVsockListener(vmPtr unsafe.Pointer, port uint32) (<-chan net.Conn, error) {
	vsockMu.Lock()
	defer vsockMu.Unlock()

	ch := make(chan net.Conn, 4)
	vsockConnCh = ch

	ret := C.vm_vsock_listen(vmPtr, C.uint32_t(port))
	if ret != 0 {
		vsockConnCh = nil
		return nil, fmt.Errorf("vm_vsock_listen failed")
	}
	return ch, nil
}

// StopVsockListener removes the vsock listener.
func StopVsockListener(vmPtr unsafe.Pointer, port uint32) {
	vsockMu.Lock()
	defer vsockMu.Unlock()

	C.vm_vsock_stop(vmPtr, C.uint32_t(port))
	if vsockConnCh != nil {
		close(vsockConnCh)
		vsockConnCh = nil
	}
}

//export vsock_go_accepted
func vsock_go_accepted(fd C.int) {
	vsockMu.Lock()
	ch := vsockConnCh
	vsockMu.Unlock()

	if ch == nil {
		return
	}

	f := os.NewFile(uintptr(fd), "vsock")
	if f == nil {
		return
	}

	conn, err := net.FileConn(f)
	// FileConn dups the fd; close the original.
	f.Close()
	if err != nil {
		return
	}

	select {
	case ch <- conn:
	default:
		conn.Close()
	}
}

// VMPtr returns the raw VM pointer for use with vsock APIs.
func (vm *VMManager) VMPtr() unsafe.Pointer {
	return vm.handle.vm
}
