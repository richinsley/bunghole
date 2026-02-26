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
	vsockPorts = make(map[uint32]chan net.Conn)
	vsockMu    sync.Mutex
)

// StartVsockListener starts listening for vsock connections on the given port.
// Returns a channel that delivers accepted connections.
func StartVsockListener(vmPtr unsafe.Pointer, port uint32) (<-chan net.Conn, error) {
	vsockMu.Lock()
	defer vsockMu.Unlock()

	if _, exists := vsockPorts[port]; exists {
		return nil, fmt.Errorf("vsock port %d already in use", port)
	}

	ch := make(chan net.Conn, 4)
	vsockPorts[port] = ch

	ret := C.vm_vsock_listen(vmPtr, C.uint32_t(port))
	if ret != 0 {
		delete(vsockPorts, port)
		return nil, fmt.Errorf("vm_vsock_listen failed for port %d", port)
	}
	return ch, nil
}

// StopVsockListener removes the vsock listener for a specific port.
func StopVsockListener(vmPtr unsafe.Pointer, port uint32) {
	vsockMu.Lock()
	defer vsockMu.Unlock()

	C.vm_vsock_stop(vmPtr, C.uint32_t(port))
	if ch, ok := vsockPorts[port]; ok {
		close(ch)
		delete(vsockPorts, port)
	}
}

//export vsock_go_accepted
func vsock_go_accepted(fd C.int, port C.uint32_t) {
	vsockMu.Lock()
	ch := vsockPorts[uint32(port)]
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
