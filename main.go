package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
)

//go:embed web/index.html
var webContent embed.FS

var (
	flagDisplay    = flag.String("display", "", "X11 display to capture (auto-detected or started if empty)")
	flagAddr       = flag.String("addr", ":8080", "HTTP listen address")
	flagToken      = flag.String("token", "", "Bearer token for authentication (required)")
	flagFPS        = flag.Int("fps", 30, "Capture frame rate")
	flagBitrate    = flag.Int("bitrate", 4000, "Video bitrate in kbps")
	flagStartX     = flag.Bool("start-x", false, "Start a new Xorg server with nvidia driver")
	flagResolution = flag.String("resolution", "1920x1080", "Screen resolution when starting X server")
	flagGPU        = flag.Int("gpu", 0, "GPU index for Xorg (0=first, 1=second)")
	flagCodec      = flag.String("codec", "h264", "Video codec (h264 or h265)")
	flagGOP        = flag.Int("gop", 0, "Keyframe interval in frames (0 = 2x FPS)")
)

type Server struct {
	display string
	token   string
	fps     int
	bitrate int
	gpu     int
	codec   string
	gop     int

	mu       sync.Mutex
	session  *Session
	capturer *Capturer
	encoder  *Encoder
	audio    *AudioCapture
}

func main() {
	flag.Parse()

	if *flagToken == "" {
		log.Fatal("--token is required")
	}

	display := *flagDisplay
	var xserver *XServer

	if *flagStartX || display == "" {
		// Try to auto-detect an existing display, or start one
		if display == "" {
			display = os.Getenv("DISPLAY")
		}

		if display == "" || *flagStartX {
			var err error
			xserver, err = StartXServer(*flagResolution, *flagGPU)
			if err != nil {
				log.Fatalf("failed to start X server: %v", err)
			}
			display = xserver.Display
			os.Setenv("DISPLAY", display)
			os.Setenv("XAUTHORITY", xserver.Xauthority)

			if err := xserver.StartDesktopSession(*flagResolution); err != nil {
				log.Printf("warning: failed to start desktop session: %v", err)
				log.Printf("X server is running on %s but no desktop — you may want to start one manually", display)
			}

			// Point audio capture at the PipeWire instance inside the desktop session
			if xserver.PulseServer != "" {
				os.Setenv("PULSE_SERVER", xserver.PulseServer)
				log.Printf("audio: using %s", xserver.PulseServer)
			}
		}
	}

	if display == "" {
		log.Fatal("no display available — use --display, set DISPLAY env, or use --start-x")
	}

	codec := *flagCodec
	if codec != "h264" && codec != "h265" {
		log.Fatalf("--codec must be h264 or h265, got %q", codec)
	}

	srv := &Server{
		display: display,
		token:   *flagToken,
		fps:     *flagFPS,
		bitrate: *flagBitrate,
		gpu:     *flagGPU,
		codec:   codec,
		gop:     *flagGOP,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", srv.handleIndex)
	mux.HandleFunc("POST /whep", srv.handleWHEPOffer)
	mux.HandleFunc("PATCH /whep/{id}", srv.handleWHEPPatch)
	mux.HandleFunc("DELETE /whep/{id}", srv.handleWHEPDelete)
	mux.HandleFunc("OPTIONS /whep", srv.handleWHEPOptions)
	mux.HandleFunc("OPTIONS /whep/{id}", srv.handleWHEPOptions)

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received %s, shutting down...", sig)
		srv.mu.Lock()
		srv.teardownLocked()
		srv.mu.Unlock()
		if xserver != nil {
			xserver.Stop()
		}
		os.Exit(0)
	}()

	log.Printf("starting bunghole on %s (display %s, %d fps, %d kbps, codec %s)",
		*flagAddr, display, *flagFPS, *flagBitrate, codec)

	if err := http.ListenAndServe(*flagAddr, mux); err != nil {
		log.Fatal(err)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webContent.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

func (s *Server) handleWHEPOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, PATCH, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Expose-Headers", "Location")
	w.WriteHeader(204)
}

func (s *Server) handleWHEPOffer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Location")

	if !s.checkAuth(r) {
		http.Error(w, "unauthorized", 401)
		return
	}

	// Single session: tear down existing
	s.mu.Lock()
	if s.session != nil {
		s.teardownLocked()
	}
	s.mu.Unlock()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(body),
	}

	sessionID := uuid.New().String()
	sess, err := NewSession(sessionID, s.display, s.codec)
	if err != nil {
		log.Printf("session create error: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	if err := sess.PC.SetRemoteDescription(offer); err != nil {
		sess.Close()
		log.Printf("set remote desc error: %v", err)
		http.Error(w, "bad SDP offer", 400)
		return
	}

	answer, err := sess.PC.CreateAnswer(nil)
	if err != nil {
		sess.Close()
		log.Printf("create answer error: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	if err := sess.PC.SetLocalDescription(answer); err != nil {
		sess.Close()
		log.Printf("set local desc error: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	// Wait for ICE gathering to complete
	gatherComplete := webrtc.GatheringCompletePromise(sess.PC)
	<-gatherComplete

	s.mu.Lock()
	s.session = sess
	s.mu.Unlock()

	// Start capture pipeline
	go s.startPipeline(sess)

	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", fmt.Sprintf("/whep/%s", sessionID))
	w.WriteHeader(201)
	w.Write([]byte(sess.PC.LocalDescription().SDP))
}

func (s *Server) handleWHEPPatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if !s.checkAuth(r) {
		http.Error(w, "unauthorized", 401)
		return
	}

	id := r.PathValue("id")
	s.mu.Lock()
	sess := s.session
	s.mu.Unlock()

	if sess == nil || sess.ID != id {
		http.Error(w, "not found", 404)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}

	candidate := string(body)
	if strings.TrimSpace(candidate) == "" {
		w.WriteHeader(204)
		return
	}

	// Parse ICE candidate from SDP fragment
	// The candidate line is in the body
	lines := strings.Split(candidate, "\r\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "a=candidate:") {
			c := strings.TrimPrefix(line, "a=")
			if err := sess.PC.AddICECandidate(webrtc.ICECandidateInit{
				Candidate: c,
			}); err != nil {
				log.Printf("add ice candidate error: %v", err)
			}
		}
	}

	w.WriteHeader(204)
}

func (s *Server) handleWHEPDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if !s.checkAuth(r) {
		http.Error(w, "unauthorized", 401)
		return
	}

	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.session == nil || s.session.ID != id {
		http.Error(w, "not found", 404)
		return
	}

	s.teardownLocked()
	w.WriteHeader(200)
}

func (s *Server) checkAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	return auth == "Bearer "+s.token
}

func (s *Server) startPipeline(sess *Session) {
	cap, err := NewCapturer(s.display, s.fps)
	if err != nil {
		log.Printf("capturer init error: %v", err)
		return
	}

	enc, err := NewEncoder(cap.Width(), cap.Height(), s.fps, s.bitrate, s.gpu, s.codec, s.gop)
	if err != nil {
		cap.Close()
		log.Printf("encoder init error: %v", err)
		return
	}

	s.mu.Lock()
	s.capturer = cap
	s.encoder = enc
	s.mu.Unlock()

	// Start audio capture (non-fatal if it fails)
	var audio *AudioCapture
	audio, err = NewAudioCapture()
	if err != nil {
		log.Printf("audio capture init failed (continuing without audio): %v", err)
	} else {
		s.mu.Lock()
		s.audio = audio
		s.mu.Unlock()

		audioPkts := make(chan *OpusPacket, 10)
		go audio.Run(audioPkts, sess.stop)
		go func() {
			for {
				select {
				case <-sess.stop:
					return
				case pkt := <-audioPkts:
					if err := sess.WriteAudioSample(pkt.Data, pkt.Duration); err != nil {
						return
					}
				}
			}
		}()
	}

	// Inline capture + encode loop (zero-copy: no channel, no frame copy)
	// Grab() returns a pointer to the SHM buffer which is valid until the next Grab().
	// Encode() reads from it synchronously before we grab again.
	frameDur := time.Duration(float64(time.Second) / float64(s.fps))
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()

	for {
		select {
		case <-sess.stop:
			return
		case <-ticker.C:
			frame, err := cap.Grab()
			if err != nil {
				continue
			}
			encoded, err := enc.Encode(frame)
			if err != nil {
				log.Printf("encode error: %v", err)
				continue
			}
			if encoded == nil {
				continue
			}
			if err := sess.WriteVideoSample(encoded.Data, frameDur); err != nil {
				return
			}
		}
	}
}

func (s *Server) teardownLocked() {
	if s.session != nil {
		s.session.Close()
		s.session = nil
	}
	if s.capturer != nil {
		s.capturer.Close()
		s.capturer = nil
	}
	if s.encoder != nil {
		s.encoder.Close()
		s.encoder = nil
	}
	if s.audio != nil {
		s.audio.Close()
		s.audio = nil
	}
}
