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
	"github.com/pion/webrtc/v4/pkg/media"
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

	mu sync.Mutex

	// Shared tracks (owned by server, broadcast to all PCs)
	videoTrack *webrtc.TrackLocalStaticSample
	audioTrack *webrtc.TrackLocalStaticSample

	// Pipeline resources
	capturer types.MediaCapturer
	encoder  types.VideoEncoder
	audio    types.AudioCapturer
	pipeStop chan struct{}   // closed to stop pipeline goroutine
	pipeWg   sync.WaitGroup // waited before starting a new pipeline

	// Sessions
	ctrl    *session.Session            // at most one controller
	viewers map[string]*session.Session // zero or more viewers
}

func New(cfg Config) *Server {
	return &Server{
		cfg:     cfg,
		viewers: make(map[string]*session.Session),
	}
}

func (s *Server) ListenAndServe() error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /mode", s.handleMode)

	// Controller endpoints
	mux.HandleFunc("POST /whep", s.handleWHEPOffer)
	mux.HandleFunc("PATCH /whep/{id}", s.handleWHEPPatch)
	mux.HandleFunc("DELETE /whep/{id}", s.handleWHEPDelete)
	mux.HandleFunc("OPTIONS /whep", s.handleWHEPOptions)
	mux.HandleFunc("OPTIONS /whep/{id}", s.handleWHEPOptions)

	// Viewer endpoints
	mux.HandleFunc("POST /whep/view", s.handleViewerOffer)
	mux.HandleFunc("PATCH /whep/view/{id}", s.handleViewerPatch)
	mux.HandleFunc("DELETE /whep/view/{id}", s.handleViewerDelete)
	mux.HandleFunc("OPTIONS /whep/view", s.handleWHEPOptions)
	mux.HandleFunc("OPTIONS /whep/view/{id}", s.handleWHEPOptions)

	mux.HandleFunc("GET /debug/frame", s.handleDebugFrame)

	log.Printf("starting bunghole on %s (display %s, %d fps, %d kbps, codec %s)",
		s.cfg.Addr, s.cfg.Display, s.cfg.FPS, s.cfg.Bitrate, s.cfg.Codec)

	return http.ListenAndServe(s.cfg.Addr, mux)
}

// Teardown shuts down all sessions and releases resources.
func (s *Server) Teardown() {
	s.mu.Lock()
	s.teardownLocked()
	s.mu.Unlock()
	s.pipeWg.Wait()
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

// --- Controller (interactive) endpoints ---

func (s *Server) handleWHEPOffer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Location")

	if !s.checkAuth(r) {
		http.Error(w, "unauthorized", 401)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(body),
	}

	s.mu.Lock()
	// Close old controller if present (pipeline keeps running for viewers)
	if s.ctrl != nil {
		s.ctrl.Close()
		s.ctrl = nil
	}

	// Ensure pipeline is running and shared tracks exist
	if err := s.ensurePipelineLocked(); err != nil {
		s.mu.Unlock()
		log.Printf("pipeline start error: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	videoTrack := s.videoTrack
	audioTrack := s.audioTrack
	s.mu.Unlock()

	sessionID := uuid.New().String()
	sess, err := session.NewSession(sessionID, s.cfg.Display, s.cfg.Codec,
		videoTrack, audioTrack,
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

	gatherComplete := webrtc.GatheringCompletePromise(sess.PC)
	<-gatherComplete

	s.mu.Lock()
	s.ctrl = sess
	s.mu.Unlock()

	// Watch for controller disconnect
	go s.watchSession(sess, true)

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
	sess := s.ctrl
	s.mu.Unlock()

	if sess == nil || sess.ID != id {
		http.Error(w, "not found", 404)
		return
	}

	s.addICECandidates(sess, w, r)
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

	if s.ctrl == nil || s.ctrl.ID != id {
		http.Error(w, "not found", 404)
		return
	}

	s.ctrl.Close()
	s.ctrl = nil
	s.maybeStopPipelineLocked()
	w.WriteHeader(200)
}

// --- Viewer (view-only) endpoints ---

func (s *Server) handleViewerOffer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Expose-Headers", "Location")

	if !s.checkAuth(r) {
		http.Error(w, "unauthorized", 401)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", 400)
		return
	}

	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(body),
	}

	s.mu.Lock()
	if err := s.ensurePipelineLocked(); err != nil {
		s.mu.Unlock()
		log.Printf("pipeline start error: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	videoTrack := s.videoTrack
	audioTrack := s.audioTrack
	s.mu.Unlock()

	sessionID := uuid.New().String()
	sess, err := session.NewViewerSession(sessionID, s.cfg.Codec, videoTrack, audioTrack)
	if err != nil {
		log.Printf("viewer session create error: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	if err := sess.PC.SetRemoteDescription(offer); err != nil {
		sess.Close()
		log.Printf("viewer set remote desc error: %v", err)
		http.Error(w, "bad SDP offer", 400)
		return
	}

	answer, err := sess.PC.CreateAnswer(nil)
	if err != nil {
		sess.Close()
		log.Printf("viewer create answer error: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	if err := sess.PC.SetLocalDescription(answer); err != nil {
		sess.Close()
		log.Printf("viewer set local desc error: %v", err)
		http.Error(w, "internal error", 500)
		return
	}

	gatherComplete := webrtc.GatheringCompletePromise(sess.PC)
	<-gatherComplete

	s.mu.Lock()
	s.viewers[sessionID] = sess
	s.mu.Unlock()

	go s.watchSession(sess, false)

	w.Header().Set("Content-Type", "application/sdp")
	w.Header().Set("Location", fmt.Sprintf("/whep/view/%s", sessionID))
	w.WriteHeader(201)
	w.Write([]byte(sess.PC.LocalDescription().SDP))
}

func (s *Server) handleViewerPatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if !s.checkAuth(r) {
		http.Error(w, "unauthorized", 401)
		return
	}

	id := r.PathValue("id")
	s.mu.Lock()
	sess := s.viewers[id]
	s.mu.Unlock()

	if sess == nil {
		http.Error(w, "not found", 404)
		return
	}

	s.addICECandidates(sess, w, r)
}

func (s *Server) handleViewerDelete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if !s.checkAuth(r) {
		http.Error(w, "unauthorized", 401)
		return
	}

	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()

	sess := s.viewers[id]
	if sess == nil {
		http.Error(w, "not found", 404)
		return
	}

	sess.Close()
	delete(s.viewers, id)
	s.maybeStopPipelineLocked()
	w.WriteHeader(200)
}

// --- Shared helpers ---

func (s *Server) addICECandidates(sess *session.Session, w http.ResponseWriter, r *http.Request) {
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

func (s *Server) checkAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	return auth == "Bearer "+s.cfg.Token
}

// watchSession monitors a session's Stop channel and cleans up when it closes.
func (s *Server) watchSession(sess *session.Session, isController bool) {
	<-sess.Stop

	s.mu.Lock()
	defer s.mu.Unlock()

	if isController {
		if s.ctrl == sess {
			s.ctrl = nil
			log.Printf("controller %s disconnected", sess.ID)
		}
	} else {
		if _, ok := s.viewers[sess.ID]; ok {
			delete(s.viewers, sess.ID)
			log.Printf("viewer %s disconnected", sess.ID)
		}
	}

	s.maybeStopPipelineLocked()
}

// --- Pipeline lifecycle ---

// ensurePipelineLocked starts the capture/encode pipeline if not already running.
// Must be called with s.mu held.
func (s *Server) ensurePipelineLocked() error {
	if s.pipeStop != nil {
		return nil // already running
	}

	// Wait for any previous pipeline goroutine to finish cleanup
	s.mu.Unlock()
	s.pipeWg.Wait()
	s.mu.Lock()

	// Re-check after re-acquiring lock
	if s.pipeStop != nil {
		return nil
	}

	cap, err := s.cfg.NewCapturer(s.cfg.Display, s.cfg.FPS, s.cfg.GPU)
	if err != nil {
		return fmt.Errorf("capturer init: %w", err)
	}

	var cudaCtx, cuMemcpy2D unsafe.Pointer
	if cp, ok := cap.(types.CUDAProvider); ok {
		cudaCtx = cp.CUDAContext()
		cuMemcpy2D = cp.CuMemcpy2D()
	}

	enc, err := s.cfg.NewEncoder(cap.Width(), cap.Height(), s.cfg.FPS, s.cfg.Bitrate,
		s.cfg.GPU, s.cfg.Codec, s.cfg.GOP, cudaCtx, cuMemcpy2D)
	if err != nil {
		cap.Close()
		return fmt.Errorf("encoder init: %w", err)
	}

	// Create shared tracks
	var videoMimeType, videoFmtp string
	if s.cfg.Codec == "h265" {
		videoMimeType = webrtc.MimeTypeH265
		videoFmtp = "profile-id=1"
	} else {
		videoMimeType = webrtc.MimeTypeH264
		videoFmtp = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"
	}

	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:    videoMimeType,
			ClockRate:   90000,
			SDPFmtpLine: videoFmtp,
		},
		"video", "bunghole",
	)
	if err != nil {
		enc.Close()
		cap.Close()
		return fmt.Errorf("create video track: %w", err)
	}

	audioTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		},
		"audio", "bunghole",
	)
	if err != nil {
		enc.Close()
		cap.Close()
		return fmt.Errorf("create audio track: %w", err)
	}

	s.capturer = cap
	s.encoder = enc
	s.videoTrack = videoTrack
	s.audioTrack = audioTrack
	s.pipeStop = make(chan struct{})

	s.pipeWg.Add(1)
	go s.runPipeline(cap, enc, videoTrack, audioTrack, s.pipeStop)

	log.Printf("pipeline started (%dx%d, %s)", cap.Width(), cap.Height(), s.cfg.Codec)
	return nil
}

// maybeStopPipelineLocked stops the pipeline if no sessions remain.
// Must be called with s.mu held.
func (s *Server) maybeStopPipelineLocked() {
	if s.ctrl != nil || len(s.viewers) > 0 {
		return
	}
	s.stopPipelineLocked()
}

// stopPipelineLocked signals the pipeline to stop.
// Must be called with s.mu held.
func (s *Server) stopPipelineLocked() {
	if s.pipeStop == nil {
		return
	}
	close(s.pipeStop)
	s.pipeStop = nil
	// Cleanup happens in runPipeline's defer
}

// runPipeline is the capture/encode loop. It writes to shared tracks and
// stops when pipeStop is closed. Cleanup of cap/enc/audio is done in defer.
func (s *Server) runPipeline(cap types.MediaCapturer, enc types.VideoEncoder, videoTrack, audioTrack *webrtc.TrackLocalStaticSample, stop chan struct{}) {
	defer s.pipeWg.Done()
	defer func() {
		s.mu.Lock()
		// Only nil out if these are still our resources
		if s.capturer == cap {
			s.capturer = nil
		}
		if s.encoder == enc {
			s.encoder = nil
		}
		if s.audio != nil {
			s.audio.Close()
			s.audio = nil
		}
		if s.videoTrack == videoTrack {
			s.videoTrack = nil
		}
		if s.audioTrack == audioTrack {
			s.audioTrack = nil
		}
		s.mu.Unlock()

		// Close encoder before capturer (encoder uses CUDA context owned by capturer)
		enc.Close()
		cap.Close()
		log.Printf("pipeline stopped")
	}()

	// Start audio capture (non-fatal if it fails)
	ac, err := audio.NewAudioCapture()
	if err != nil {
		log.Printf("audio capture init failed (continuing without audio): %v", err)
	} else {
		s.mu.Lock()
		s.audio = ac
		s.mu.Unlock()

		audioPkts := make(chan *types.OpusPacket, 10)
		go ac.Run(audioPkts, stop)
		go func() {
			for {
				select {
				case <-stop:
					return
				case pkt := <-audioPkts:
					audioTrack.WriteSample(media.Sample{
						Data:     pkt.Data,
						Duration: pkt.Duration,
					})
				}
			}
		}()
	}

	frameDur := time.Duration(float64(time.Second) / float64(s.cfg.FPS))
	ticker := time.NewTicker(frameDur)
	defer ticker.Stop()

	var loopCount, grabFails, encodeFails, encodeNils int
	lastStats := time.Now()

	for {
		select {
		case <-stop:
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
			// WriteSample broadcasts to all bound PeerConnections.
			// Ignore errors — they occur when no PCs are bound yet.
			videoTrack.WriteSample(media.Sample{
				Data:     encoded.Data,
				Duration: frameDur,
			})
			tSend := time.Since(t2)

			if s.cfg.Stats && time.Since(lastStats) >= 5*time.Second {
				log.Printf("pipeline: loops=%d grabFail=%d encFail=%d encNil=%d | last: grab=%v enc=%v send=%v",
					loopCount, grabFails, encodeFails, encodeNils,
					tGrab.Round(time.Microsecond), tEncode.Round(time.Microsecond), tSend.Round(time.Microsecond))
				loopCount = 0
				grabFails = 0
				encodeFails = 0
				encodeNils = 0
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
	if s.ctrl != nil {
		s.ctrl.Close()
		s.ctrl = nil
	}
	for id, v := range s.viewers {
		v.Close()
		delete(s.viewers, id)
	}
	s.stopPipelineLocked()
}
