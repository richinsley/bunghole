package session

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"bunghole/internal/types"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// InputHandlerFactory creates an EventInjector for a given display.
type InputHandlerFactory func(displayName string) (types.EventInjector, error)

// ClipboardHandlerFactory creates a ClipboardSync for a given display
// with a callback for sending clipboard changes to the client.
type ClipboardHandlerFactory func(displayName string, sendFn func(string)) (types.ClipboardSync, error)

type Session struct {
	ID             string
	PC             *webrtc.PeerConnection
	VideoTrack     *webrtc.TrackLocalStaticSample
	AudioTrack     *webrtc.TrackLocalStaticSample
	InputHandler     types.EventInjector
	ClipboardHandler types.ClipboardSync
	Stop           chan struct{}
	closed         bool
	mu             sync.Mutex
}

func NewSession(id, displayName, codec string, inputFactory InputHandlerFactory, clipboardFactory ClipboardHandlerFactory) (*Session, error) {
	me := &webrtc.MediaEngine{}

	// Register video codec
	var videoMimeType string
	var videoFmtp string
	var videoPayloadType webrtc.PayloadType

	if codec == "h265" {
		videoMimeType = webrtc.MimeTypeH265
		videoFmtp = "profile-id=1"
		videoPayloadType = 97
	} else {
		videoMimeType = webrtc.MimeTypeH264
		videoFmtp = "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f"
		videoPayloadType = 96
	}

	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:    videoMimeType,
			ClockRate:   90000,
			SDPFmtpLine: videoFmtp,
		},
		PayloadType: videoPayloadType,
	}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, fmt.Errorf("register video codec: %w", err)
	}

	// Register Opus codec
	if err := me.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		},
		PayloadType: 111,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, fmt.Errorf("register Opus: %w", err)
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(me))

	config := webrtc.Configuration{
		// LAN only — no STUN/TURN
	}

	pc, err := api.NewPeerConnection(config)
	if err != nil {
		return nil, fmt.Errorf("create peer connection: %w", err)
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
		pc.Close()
		return nil, fmt.Errorf("create video track: %w", err)
	}

	if _, err = pc.AddTrack(videoTrack); err != nil {
		pc.Close()
		return nil, fmt.Errorf("add video track: %w", err)
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
		pc.Close()
		return nil, fmt.Errorf("create audio track: %w", err)
	}

	if _, err = pc.AddTrack(audioTrack); err != nil {
		pc.Close()
		return nil, fmt.Errorf("add audio track: %w", err)
	}

	sess := &Session{
		ID:         id,
		PC:         pc,
		VideoTrack: videoTrack,
		AudioTrack: audioTrack,
		Stop:       make(chan struct{}),
	}

	// Set up input handler via factory
	if inputFactory != nil {
		ih, err := inputFactory(displayName)
		if err != nil {
			log.Printf("warning: input handler init failed: %v", err)
		} else {
			sess.InputHandler = ih
		}
	}

	// Data channels are created by the client; we handle them via OnDataChannel
	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		switch dc.Label() {
		case "input":
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				if sess.InputHandler != nil {
					var event types.InputEvent
					if err := json.Unmarshal(msg.Data, &event); err != nil {
						return
					}
					sess.InputHandler.Inject(event)
				}
			})
		case "clipboard":
			if clipboardFactory == nil {
				break
			}
			// Set up clipboard handler when the channel opens
			dc.OnOpen(func() {
				ch, err := clipboardFactory(displayName, func(text string) {
					if dc.ReadyState() == webrtc.DataChannelStateOpen {
						dc.SendText(text)
					}
				})
				if err != nil {
					log.Printf("clipboard handler init failed: %v", err)
					return
				}
				sess.mu.Lock()
				sess.ClipboardHandler = ch
				sess.mu.Unlock()
				go ch.Run(sess.Stop)
			})
			dc.OnMessage(func(msg webrtc.DataChannelMessage) {
				sess.mu.Lock()
				ch := sess.ClipboardHandler
				sess.mu.Unlock()
				if ch != nil {
					ch.SetFromClient(string(msg.Data))
				}
			})
		}
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("peer connection state: %s", state.String())
		if state == webrtc.PeerConnectionStateFailed ||
			state == webrtc.PeerConnectionStateDisconnected ||
			state == webrtc.PeerConnectionStateClosed {
			sess.Close()
		}
	})

	return sess, nil
}

func (s *Session) WriteVideoSample(data []byte, dur time.Duration) error {
	return s.VideoTrack.WriteSample(media.Sample{
		Data:     data,
		Duration: dur,
	})
}

func (s *Session) WriteAudioSample(data []byte, dur time.Duration) error {
	return s.AudioTrack.WriteSample(media.Sample{
		Data:     data,
		Duration: dur,
	})
}

func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.Stop)

	if s.InputHandler != nil {
		s.InputHandler.Close()
	}
	if s.ClipboardHandler != nil {
		s.ClipboardHandler.Close()
	}
	s.PC.Close()
	log.Printf("session %s closed", s.ID)
}

func (s *Session) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
