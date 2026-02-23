package server

import (
	"fmt"
	"image/png"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unsafe"

	"bunghole/internal/audio"
	"bunghole/internal/session"
	"bunghole/internal/types"
	"bunghole/web"

	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
)

// CapturerFactory creates a screen capturer for the given display.
type CapturerFactory func(display string, fps, gpu int) (types.MediaCapturer, error)

// EncoderFactory creates a video encoder.
type EncoderFactory func(width, height, fps, bitrateKbps, gpu int, codec string, gop int, cudaCtx, cuMemcpy2D unsafe.Pointer) (types.VideoEncoder, error)

// Config holds all server configuration.
type Config struct {
	Display string
	Token   string
	FPS     int
	Bitrate int
	GPU     int
	Codec   string
	GOP     int
	Addr    string
	Stats   bool

	NewCapturer  CapturerFactory
	NewEncoder   EncoderFactory
	InputFactory session.InputHandlerFactory
	ClipFactory  session.ClipboardHandlerFactory
}

type Server struct {
	cfg Config

	mu       sync.Mutex
	sess     *session.Session
	capturer types.MediaCapturer
	encoder  types.VideoEncoder
	audio    types.AudioCapturer
}

func New(cfg Config) *Server {
	return &Server{cfg: cfg}
}

func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /mode", s.handleMode)
	mux.HandleFunc("POST /whep", s.handleWHEPOffer)
	mux.HandleFunc("PATCH /whep/{id}", s.handleWHEPPatch)
	mux.HandleFunc("DELETE /whep/{id}", s.handleWHEPDelete)
	mux.HandleFunc("OPTIONS /whep", s.handleWHEPOptions)
	mux.HandleFunc("OPTIONS /whep/{id}", s.handleWHEPOptions)
	mux.HandleFunc("GET /debug/frame", s.handleDebugFrame)

	log.Printf("starting bunghole on %s (display %s, %d fps, %d kbps, codec %s)",
		s.cfg.Addr, s.cfg.Display, s.cfg.FPS, s.cfg.Bitrate, s.cfg.Codec)

	return http.ListenAndServe(s.cfg.Addr, mux)
}

// Teardown shuts down the active session and releases resources.
// It acquires the lock internally.
func (s *Server) Teardown() {
	s.mu.Lock()
	s.teardownLocked()
	s.mu.Unlock()
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/" {
		data, err := web.Content.ReadFile("index.html")
		if err != nil {
			http.Error(w, "internal error", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
		return
	}
	// Serve embedded static files (logo, etc.)
	http.FileServer(http.FS(web.Content)).ServeHTTP(w, r)
}

func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	mode := "desktop"
	if s.cfg.Display == "vm" {
		mode = "vm"
	}
	fmt.Fprintf(w, `{"mode":%q}`, mode)
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
	if s.sess != nil {
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
	sess, err := session.NewSession(sessionID, s.cfg.Display, s.cfg.Codec,
		s.cfg.InputFactory, s.cfg.ClipFactory)
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
	s.sess = sess
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
	sess := s.sess
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

	if s.sess == nil || s.sess.ID != id {
		http.Error(w, "not found", 404)
		return
	}

	s.teardownLocked()
	w.WriteHeader(200)
}

func (s *Server) checkAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	return auth == "Bearer "+s.cfg.Token
}

func (s *Server) startPipeline(sess *session.Session) {
	cap, err := s.cfg.NewCapturer(s.cfg.Display, s.cfg.FPS, s.cfg.GPU)
	if err != nil {
		log.Printf("capturer init error: %v", err)
		return
	}

	// If the capturer provides a CUDA context, pass it to the encoder
	// for zero-copy NVENC encoding from GPU memory.
	var cudaCtx, cuMemcpy2D unsafe.Pointer
	if cp, ok := cap.(types.CUDAProvider); ok {
		cudaCtx = cp.CUDAContext()
		cuMemcpy2D = cp.CuMemcpy2D()
	}

	enc, err := s.cfg.NewEncoder(cap.Width(), cap.Height(), s.cfg.FPS, s.cfg.Bitrate,
		s.cfg.GPU, s.cfg.Codec, s.cfg.GOP, cudaCtx, cuMemcpy2D)
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
	ac, err := audio.NewAudioCapture()
	if err != nil {
		log.Printf("audio capture init failed (continuing without audio): %v", err)
	} else {
		s.mu.Lock()
		s.audio = ac
		s.mu.Unlock()

		audioPkts := make(chan *types.OpusPacket, 10)
		go ac.Run(audioPkts, sess.Stop)
		go func() {
			for {
				select {
				case <-sess.Stop:
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
	frameDur := time.Duration(float64(time.Second) / float64(s.cfg.FPS))
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()

	var loopCount, grabFails, encodeFails, encodeNils, sendFails int
	lastStats := time.Now()

	for {
		select {
		case <-sess.Stop:
			return
		case <-ticker.C:
			loopCount++
			t0 := time.Now()

			frame, err := cap.Grab()
			if err != nil {
				grabFails++
				continue
			}
			tGrab := time.Since(t0)

			t1 := time.Now()
			encoded, err := enc.Encode(frame)
			if err != nil {
				encodeFails++
				if encodeFails <= 5 {
					log.Printf("encode error: %v", err)
				}
				continue
			}
			tEncode := time.Since(t1)

			if encoded == nil {
				encodeNils++
				continue
			}

			t2 := time.Now()
			if err := sess.WriteVideoSample(encoded.Data, frameDur); err != nil {
				sendFails++
				return
			}
			tSend := time.Since(t2)

			// Report pipeline stats every 5 seconds (opt-in)
			if s.cfg.Stats && time.Since(lastStats) >= 5*time.Second {
				log.Printf("pipeline: loops=%d grabFail=%d encFail=%d encNil=%d sendFail=%d | last: grab=%v enc=%v send=%v",
					loopCount, grabFails, encodeFails, encodeNils, sendFails,
					tGrab.Round(time.Microsecond), tEncode.Round(time.Microsecond), tSend.Round(time.Microsecond))
				loopCount = 0
				grabFails = 0
				encodeFails = 0
				encodeNils = 0
				sendFails = 0
				lastStats = time.Now()
			}
		}
	}
}

func (s *Server) handleDebugFrame(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(r) {
		http.Error(w, "unauthorized", 401)
		return
	}

	s.mu.Lock()
	cap := s.capturer
	s.mu.Unlock()

	var tempCap types.MediaCapturer
	if cap == nil {
		var err error
		tempCap, err = s.cfg.NewCapturer(s.cfg.Display, 1, s.cfg.GPU)
		if err != nil {
			http.Error(w, fmt.Sprintf("capturer init: %v", err), 500)
			return
		}
		defer tempCap.Close()
		cap = tempCap
	}

	grabber, ok := cap.(types.DebugGrabber)
	if !ok {
		http.Error(w, "capturer does not support debug grab", 500)
		return
	}

	img, err := grabber.GrabImage()
	if err != nil {
		http.Error(w, fmt.Sprintf("grab failed: %v", err), 500)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	png.Encode(w, img)
}

func (s *Server) teardownLocked() {
	if s.sess != nil {
		s.sess.Close()
		s.sess = nil
	}
	if s.audio != nil {
		s.audio.Close()
		s.audio = nil
	}
	// Close encoder before capturer: encoder uses the CUDA context
	// owned by the capturer, so it must be freed first.
	if s.encoder != nil {
		s.encoder.Close()
		s.encoder = nil
	}
	if s.capturer != nil {
		s.capturer.Close()
		s.capturer = nil
	}
}
