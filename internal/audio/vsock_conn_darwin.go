//go:build darwin

package audio

import (
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// DialVsock connects to the host via AF_VSOCK on the given port.
// CID 2 is the well-known host address (VMADDR_CID_HOST).
func DialVsock(port uint32, timeout time.Duration) (net.Conn, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("vsock socket: %w", err)
	}

	if timeout > 0 {
		tv := unix.NsecToTimeval(int64(timeout))
		_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv)
	}

	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_HOST,
		Port: port,
	}
	if err := unix.Connect(fd, sa); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("vsock connect CID=%d port=%d: %w", unix.VMADDR_CID_HOST, port, err)
	}

	// Clear the send timeout after successful connect
	if timeout > 0 {
		tv := unix.NsecToTimeval(0)
		_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv)
	}

	f := os.NewFile(uintptr(fd), "vsock")
	conn, err := net.FileConn(f)
	f.Close() // FileConn dups the fd
	if err != nil {
		return nil, fmt.Errorf("vsock fileconn: %w", err)
	}
	return conn, nil
}
