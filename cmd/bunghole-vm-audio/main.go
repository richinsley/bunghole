//go:build darwin

package main

import (
	"flag"
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
	flagUDP             = flag.String("udp", "", "Optional host:port to send raw Opus packet datagrams")
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

	var udpConn *net.UDPConn
	if *flagUDP != "" {
		addr, err := net.ResolveUDPAddr("udp", *flagUDP)
		if err != nil {
			log.Fatalf("resolve --udp %q: %v", *flagUDP, err)
		}
		udpConn, err = net.DialUDP("udp", nil, addr)
		if err != nil {
			log.Fatalf("dial --udp %q: %v", *flagUDP, err)
		}
		defer udpConn.Close()
		log.Printf("sending Opus datagrams to %s", addr.String())
	} else {
		log.Printf("no --udp destination set; capturing only")
	}

	packets := make(chan *types.OpusPacket, 256)
	stop := make(chan struct{})
	go ac.Run(packets, stop)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	log.Printf("capture started")

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

			if udpConn != nil {
				if _, err := udpConn.Write(pkt.Data); err != nil {
					log.Printf("udp write failed: %v", err)
					continue
				}
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
	log.Printf("stopped")
}

func tickerCh(t *time.Ticker) <-chan time.Time {
	if t == nil {
		return nil
	}
	return t.C
}
