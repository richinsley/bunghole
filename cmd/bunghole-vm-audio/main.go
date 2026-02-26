//go:build darwin

package main

import (
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bunghole/internal/audio"
	"bunghole/internal/types"
)

var (
	flagTransport       = flag.String("transport", "auto", "Transport: auto, vsock, or udp")
	flagUDP             = flag.String("udp", "", "host:port to send raw Opus packet datagrams (UDP mode)")
	flagVsockPort       = flag.Uint("vsock-port", 5000, "Vsock port to connect to (vsock mode)")
	flagStats           = flag.Bool("stats", true, "Log packet stats")
	flagStatsInterval   = flag.Duration("stats-interval", 5*time.Second, "Stats logging interval")
	flagProbePermission = flag.Bool("probe-permission", false, "Initialize ScreenCaptureKit audio once, then exit (used by installer)")
)

func main() {
	flag.Parse()

	if *flagStatsInterval <= 0 {
		log.Fatal("--stats-interval must be > 0")
	}

	ac, err := audio.NewAudioCapture()
	if err != nil {
		log.Fatalf("audio capture init failed: %v", err)
	}
	defer ac.Close()

	if *flagProbePermission {
		log.Printf("permission probe ok")
		return
	}

	transport := *flagTransport
	if transport != "auto" && transport != "vsock" && transport != "udp" {
		log.Fatalf("--transport must be auto, vsock, or udp, got %q", transport)
	}

	var sender packetSender
	switch transport {
	case "vsock":
		sender = connectVsock(uint32(*flagVsockPort))
	case "udp":
		sender = connectUDP()
	case "auto":
		sender = connectAuto(uint32(*flagVsockPort))
	}

	packets := make(chan *types.OpusPacket, 256)
	stop := make(chan struct{})
	go ac.Run(packets, stop)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	log.Printf("capture started (transport=%s)", sender.name())

	var ticker *time.Ticker
	if *flagStats {
		ticker = time.NewTicker(*flagStatsInterval)
		defer ticker.Stop()
	}

	var (
		intervalPackets int64
		intervalBytes   int64
		totalPackets    int64
		totalBytes      int64
	)

	running := true
	for running {
		select {
		case sig := <-sigCh:
			log.Printf("received %s, shutting down", sig)
			running = false
		case pkt := <-packets:
			if pkt == nil || len(pkt.Data) == 0 {
				continue
			}

			if err := sender.send(pkt.Data); err != nil {
				log.Printf("send failed: %v", err)
				continue
			}

			intervalPackets++
			totalPackets++
			packetBytes := int64(len(pkt.Data))
			intervalBytes += packetBytes
			totalBytes += packetBytes
		case <-tickerCh(ticker):
			avg := float64(0)
			if intervalPackets > 0 {
				avg = float64(intervalBytes) / float64(intervalPackets)
			}
			log.Printf("audio stats interval=%s packets=%d bytes=%d avg_packet=%.1fB total_packets=%d total_bytes=%d",
				flagStatsInterval.String(), intervalPackets, intervalBytes, avg, totalPackets, totalBytes)
			intervalPackets = 0
			intervalBytes = 0
		}
	}

	close(stop)
	sender.close()
	log.Printf("stopped")
}

// packetSender abstracts UDP vs vsock sending.
type packetSender interface {
	send(data []byte) error
	close()
	name() string
}

type udpSender struct {
	conn *net.UDPConn
}

func (s *udpSender) send(data []byte) error {
	_, err := s.conn.Write(data)
	return err
}

func (s *udpSender) close() { s.conn.Close() }

func (s *udpSender) name() string { return "udp" }

type vsockSender struct {
	conn io.WriteCloser
}

func (s *vsockSender) send(data []byte) error {
	return audio.WriteFrame(s.conn, data)
}

func (s *vsockSender) close() { s.conn.Close() }

func (s *vsockSender) name() string { return "vsock" }

type nullSender struct{}

func (s *nullSender) send(data []byte) error { return nil }

func (s *nullSender) close()          {}

func (s *nullSender) name() string { return "none" }

func connectVsock(port uint32) packetSender {
	conn, err := audio.DialVsock(port, 5*time.Second)
	if err != nil {
		log.Fatalf("vsock connect failed: %v", err)
	}
	log.Printf("connected via vsock (port %d)", port)
	return &vsockSender{conn: conn}
}

func connectUDP() packetSender {
	if *flagUDP == "" {
		log.Printf("no --udp destination set; capturing only")
		return &nullSender{}
	}
	addr, err := net.ResolveUDPAddr("udp", *flagUDP)
	if err != nil {
		log.Fatalf("resolve --udp %q: %v", *flagUDP, err)
	}
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		log.Fatalf("dial --udp %q: %v", *flagUDP, err)
	}
	log.Printf("sending Opus datagrams to %s", addr.String())
	return &udpSender{conn: conn}
}

func connectAuto(vsockPort uint32) packetSender {
	// Try vsock first
	conn, err := audio.DialVsock(vsockPort, 2*time.Second)
	if err == nil {
		log.Printf("auto: connected via vsock (port %d)", vsockPort)
		return &vsockSender{conn: conn}
	}
	log.Printf("auto: vsock failed (%v), falling back to UDP", err)
	return connectUDP()
}

func tickerCh(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}
