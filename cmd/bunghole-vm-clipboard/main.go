//go:build darwin

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var (
	flagVsockPort = flag.Uint("vsock-port", 5002, "Vsock port to connect to")
)

func main() {
	flag.Parse()

	port := uint32(*flagVsockPort)
	stop := make(chan struct{})
	var stopOnce sync.Once

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("received %s, shutting down", sig)
		stopOnce.Do(func() { close(stop) })
	}()

	for {
		select {
		case <-stop:
			log.Printf("stopped")
			return
		default:
		}

		log.Printf("connecting to host vsock port %d...", port)
		conn, err := dialVsock(port, 5*time.Second)
		if err != nil {
			log.Printf("vsock connect failed: %v, retrying in 1s", err)
			select {
			case <-stop:
				return
			case <-time.After(1 * time.Second):
			}
			continue
		}
		log.Printf("connected to host vsock port %d", port)

		runSession(conn, stop)
		log.Printf("disconnected, reconnecting...")
	}
}

// dialVsock connects to the host via AF_VSOCK on the given port.
// CID 2 is the well-known host address (VMADDR_CID_HOST).
// Returns an *os.File rather than net.Conn because Go's net package
// does not support AF_VSOCK sockets.
func dialVsock(port uint32, timeout time.Duration) (*os.File, error) {
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

	if timeout > 0 {
		tv := unix.NsecToTimeval(0)
		_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv)
	}

	return os.NewFile(uintptr(fd), "vsock"), nil
}
