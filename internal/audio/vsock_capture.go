//go:build darwin

package audio

import (
	"log"
	"net"
	"time"

	"bunghole/internal/types"
)

const vsockOpusFrameDuration = 20 * time.Millisecond

type VsockAudioCapture struct {
	connCh <-chan net.Conn
}

func NewVsockAudioCapture(connCh <-chan net.Conn) *VsockAudioCapture {
	return &VsockAudioCapture{connCh: connCh}
}

func (ac *VsockAudioCapture) Run(packets chan<- *types.OpusPacket, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		case conn, ok := <-ac.connCh:
			if !ok {
				return
			}
			log.Printf("audio: vsock guest connected")
			ac.readLoop(conn, packets, stop)
			log.Printf("audio: vsock guest disconnected, waiting for reconnect")
		}
	}
}

func (ac *VsockAudioCapture) readLoop(conn net.Conn, packets chan<- *types.OpusPacket, stop <-chan struct{}) {
	defer conn.Close()

	seenFirst := false
	for {
		select {
		case <-stop:
			return
		default:
		}

		data, err := ReadFrame(conn)
		if err != nil {
			return
		}

		if !seenFirst {
			seenFirst = true
			log.Printf("audio: first vsock packet (%d bytes)", len(data))
		}

		pkt := &types.OpusPacket{
			Data:     data,
			Duration: vsockOpusFrameDuration,
		}

		select {
		case packets <- pkt:
		default:
		}
	}
}

func (ac *VsockAudioCapture) Close() {}
