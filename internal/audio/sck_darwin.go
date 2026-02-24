//go:build darwin

package audio

/*
#cgo CFLAGS: -mmacosx-version-min=14.0 -fobjc-arc
#cgo LDFLAGS: -framework ScreenCaptureKit -framework CoreMedia -framework CoreAudio -framework Cocoa

#include <stdint.h>

typedef struct {
	void *stream;
	void *delegate;
	void *filter;
	void *buffer;
} SCKAudioCaptureHandle;

int  sck_audio_start_display(SCKAudioCaptureHandle *out);
int  sck_audio_start_window(uint32_t window_id, SCKAudioCaptureHandle *out);
int  sck_audio_read_frame(SCKAudioCaptureHandle *h, int16_t *dst, int samples_per_channel);
void sck_audio_stop(SCKAudioCaptureHandle *h);
*/
import "C"
import (
	"fmt"
	"log"
	"time"
	"unsafe"

	"bunghole/internal/types"
	"bunghole/internal/vm"

	"github.com/hraban/opus"
)

const (
	sampleRate    = 48000
	channels      = 2
	frameDuration = 20                                // ms
	frameSize     = sampleRate * frameDuration / 1000 // 960 samples/channel
)

type AudioCapture struct {
	handle        C.SCKAudioCaptureHandle
	encoder       *opus.Encoder
	source        string
	fallbackTried bool
}

func NewAudioCapture() (types.AudioCapturer, error) {
	enc, err := opus.NewEncoder(sampleRate, channels, opus.AppAudio)
	if err != nil {
		return nil, fmt.Errorf("opus encoder: %w", err)
	}

	ac := &AudioCapture{encoder: enc}
	var vmErr error

	if g := vm.GetGlobal(); g != nil && g.WindowID != 0 {
		if ret := C.sck_audio_start_window(C.uint32_t(g.WindowID), &ac.handle); ret == 0 {
			ac.source = "vm-window"
			log.Printf("audio: macOS ScreenCaptureKit source=vm-window")
			return ac, nil
		}
		vmErr = fmt.Errorf("vm window stream init failed")
	}

	if ret := C.sck_audio_start_display(&ac.handle); ret != 0 {
		if vmErr != nil {
			return nil, fmt.Errorf("macOS audio init failed (%v, display stream init failed)", vmErr)
		}
		return nil, fmt.Errorf("macOS audio init failed (display stream init failed)")
	}

	ac.source = "display"
	log.Printf("audio: macOS ScreenCaptureKit source=display")
	return ac, nil
}

func (ac *AudioCapture) Run(packets chan<- *types.OpusPacket, stop <-chan struct{}) {
	opusBuf := make([]byte, 4000)
	pcmBuf := make([]int16, frameSize*channels)
	ticker := time.NewTicker(time.Duration(frameDuration) * time.Millisecond)
	defer ticker.Stop()

	emptyReads := 0
	silentFrames := 0
	seenFrame := false
	seenAudible := false

	fallbackToDisplay := func(reason string) {
		log.Printf("audio: %s; falling back to display audio", reason)
		C.sck_audio_stop(&ac.handle)
		ac.fallbackTried = true
		if rc := C.sck_audio_start_display(&ac.handle); rc == 0 {
			ac.source = "display"
			emptyReads = 0
			silentFrames = 0
			seenFrame = false
			seenAudible = false
			log.Printf("audio: fallback source=display")
		} else {
			log.Printf("audio: display fallback init failed")
		}
	}

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			ret := C.sck_audio_read_frame(
				&ac.handle,
				(*C.int16_t)(unsafe.Pointer(&pcmBuf[0])),
				C.int(frameSize),
			)
			if ret != 0 {
				emptyReads++
				// Window-audio streams can come up "alive" but deliver no samples.
				if ac.source == "vm-window" && !ac.fallbackTried && emptyReads >= 300 {
					fallbackToDisplay("vm-window yielded no frames for ~6s")
				}
				continue
			}

			emptyReads = 0
			if !seenFrame {
				seenFrame = true
				log.Printf("audio: first frame source=%s", ac.source)
			}

			peak := int32(0)
			for _, s := range pcmBuf {
				v := int32(s)
				if v < 0 {
					v = -v
				}
				if v > peak {
					peak = v
				}
			}
			if peak < 16 {
				silentFrames++
			} else {
				silentFrames = 0
				if !seenAudible {
					seenAudible = true
					log.Printf("audio: audible frame source=%s peak=%d", ac.source, peak)
				}
			}

			if ac.source == "vm-window" && !ac.fallbackTried && !seenAudible && silentFrames >= 200 {
				fallbackToDisplay("vm-window produced only silence for ~4s")
				continue
			}

			encoded, err := ac.encoder.Encode(pcmBuf, opusBuf)
			if err != nil {
				log.Printf("opus encode: %v", err)
				continue
			}

			pkt := &types.OpusPacket{
				Data:     make([]byte, encoded),
				Duration: time.Duration(frameDuration) * time.Millisecond,
			}
			copy(pkt.Data, opusBuf[:encoded])

			select {
			case packets <- pkt:
			default:
			}
		}
	}
}

func (ac *AudioCapture) Close() {
	C.sck_audio_stop(&ac.handle)
}
