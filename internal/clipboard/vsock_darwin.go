//go:build darwin

package clipboard

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	"bunghole/internal/types"
)

const maxClipFrameSize = 1 << 20 // 1 MB

// WriteClipFrame writes a clipboard frame: [4-byte BE length][UTF-8 payload].
func WriteClipFrame(w io.Writer, text string) error {
	if len(text) > maxClipFrameSize {
		return fmt.Errorf("clipboard frame too large: %d > %d", len(text), maxClipFrameSize)
	}
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(text)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := io.WriteString(w, text)
	return err
}

// ReadClipFrame reads a clipboard frame from a stream.
func ReadClipFrame(r io.Reader) (string, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > maxClipFrameSize {
		return "", fmt.Errorf("invalid clipboard frame length: %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// VsockClipboardSync implements types.ClipboardSync over a vsock connection
// to a guest clipboard agent.
type VsockClipboardSync struct {
	connCh <-chan net.Conn
	sendFn func(string)

	connMu sync.Mutex
	conn   net.Conn

	lastMu   sync.Mutex
	lastText string
}

var _ types.ClipboardSync = (*VsockClipboardSync)(nil)

func NewVsockClipboardSync(connCh <-chan net.Conn, sendFn func(string)) *VsockClipboardSync {
	return &VsockClipboardSync{
		connCh: connCh,
		sendFn: sendFn,
	}
}

// SetFromClient sends browser clipboard text to the guest.
func (v *VsockClipboardSync) SetFromClient(text string) {
	v.lastMu.Lock()
	v.lastText = text
	v.lastMu.Unlock()

	v.connMu.Lock()
	c := v.conn
	v.connMu.Unlock()

	if c == nil {
		return
	}
	if err := WriteClipFrame(c, text); err != nil {
		log.Printf("clipboard: vsock write failed: %v", err)
	}
}

// Run waits for guest connections and reads clipboard updates from the guest.
func (v *VsockClipboardSync) Run(stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case conn, ok := <-v.connCh:
			if !ok {
				return
			}
			log.Printf("clipboard: vsock guest connected")
			v.connMu.Lock()
			v.conn = conn
			v.connMu.Unlock()

			v.readLoop(conn, stop)

			v.connMu.Lock()
			v.conn = nil
			v.connMu.Unlock()
			log.Printf("clipboard: vsock guest disconnected, waiting for reconnect")
		}
	}
}

func (v *VsockClipboardSync) readLoop(conn net.Conn, stop <-chan struct{}) {
	defer conn.Close()

	for {
		select {
		case <-stop:
			return
		default:
		}

		text, err := ReadClipFrame(conn)
		if err != nil {
			return
		}

		v.lastMu.Lock()
		dup := text == v.lastText
		if !dup {
			v.lastText = text
		}
		v.lastMu.Unlock()

		if !dup {
			v.sendFn(text)
		}
	}
}

func (v *VsockClipboardSync) Close() {
	v.connMu.Lock()
	defer v.connMu.Unlock()
	if v.conn != nil {
		v.conn.Close()
		v.conn = nil
	}
}
