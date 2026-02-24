package audio

import (
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bunghole/internal/types"
)

const udpOpusFrameDuration = 20 * time.Millisecond

type UDPAudioCapture struct {
	conn net.PacketConn
	once sync.Once
}

func NewUDPAudioCapture(listenAddr string) (types.AudioCapturer, error) {
	if listenAddr == "" {
		return nil, fmt.Errorf("udp listen address is required")
	}

	network := "udp4"
	if strings.Contains(listenAddr, "[") {
		network = "udp6"
	}
	conn, err := net.ListenPacket(network, listenAddr)
	if err != nil {
		// Fallback for odd platform/address combos.
		conn, err = net.ListenPacket("udp", listenAddr)
		if err != nil {
			return nil, fmt.Errorf("listen udp %q: %w", listenAddr, err)
		}
		network = "udp"
	}
	log.Printf("audio: listening for guest Opus on %s://%s", network, conn.LocalAddr())
	return &UDPAudioCapture{conn: conn}, nil
}

func (ac *UDPAudioCapture) Run(packets chan<- *types.OpusPacket, stop <-chan struct{}) {
	if ac == nil || ac.conn == nil {
		return
	}

	go func() {
		<-stop
		ac.Close()
	}()

	var totalPackets int64
	var totalBytes int64
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		var lastPackets int64
		var lastBytes int64
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				p := atomic.LoadInt64(&totalPackets)
				b := atomic.LoadInt64(&totalBytes)
				log.Printf("audio: guest-udp stats pps=%d bps=%d total_packets=%d total_bytes=%d",
					(p-lastPackets)/5, (b-lastBytes)/5, p, b)
				lastPackets = p
				lastBytes = b
			}
		}
	}()

	buf := make([]byte, 4096)
	seenFirst := false
	for {
		n, addr, err := ac.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("audio: udp read error: %v", err)
			continue
		}
		if n <= 0 {
			continue
		}
		if !seenFirst {
			seenFirst = true
			log.Printf("audio: first guest-udp packet from %s (%d bytes)", addr.String(), n)
		}
		atomic.AddInt64(&totalPackets, 1)
		atomic.AddInt64(&totalBytes, int64(n))

		pkt := &types.OpusPacket{
			Data:     make([]byte, n),
			Duration: udpOpusFrameDuration,
		}
		copy(pkt.Data, buf[:n])

		select {
		case packets <- pkt:
		default:
		}
	}
}

func (ac *UDPAudioCapture) Close() {
	if ac == nil {
		return
	}
	ac.once.Do(func() {
		if ac.conn != nil {
			_ = ac.conn.Close()
		}
	})
}
