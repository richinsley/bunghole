//go:build linux

package audio

import (
	"encoding/binary"
	"fmt"
	"log"
	"sync"
	"time"

	"bunghole/internal/types"

	"github.com/hraban/opus"
	"github.com/jfreymuth/pulse"
	"github.com/jfreymuth/pulse/proto"
)

const (
	sampleRate    = 48000
	channels      = 2
	frameDuration = 20 // ms
	frameSize     = sampleRate * frameDuration / 1000 // 960 samples per channel
)

type AudioCapture struct {
	client  *pulse.Client
	stream  *pulse.RecordStream
	encoder *opus.Encoder
}

// pcmCollector implements pulse.Writer — receives raw PCM from PulseAudio
type pcmCollector struct {
	mu     sync.Mutex
	buf    []int16
	format byte
}

func (p *pcmCollector) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Convert bytes to int16 samples (S16LE)
	n := len(data) / 2
	for i := 0; i < n; i++ {
		sample := int16(binary.LittleEndian.Uint16(data[i*2 : i*2+2]))
		p.buf = append(p.buf, sample)
	}
	return len(data), nil
}

func (p *pcmCollector) Format() byte {
	return p.format
}

// drain returns up to `count` int16 samples, removing them from the buffer
func (p *pcmCollector) drain(count int) []int16 {
	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.buf) < count {
		return nil
	}
	out := make([]int16, count)
	copy(out, p.buf[:count])
	p.buf = p.buf[count:]
	return out
}

func NewAudioCapture() (types.AudioCapturer, error) {
	client, err := pulse.NewClient(
		pulse.ClientApplicationName("bunghole"),
	)
	if err != nil {
		return nil, fmt.Errorf("pulse connect: %w", err)
	}

	enc, err := opus.NewEncoder(sampleRate, channels, opus.AppAudio)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("opus encoder: %w", err)
	}

	ac := &AudioCapture{
		client:  client,
		encoder: enc,
	}

	return ac, nil
}

func (ac *AudioCapture) Run(packets chan<- *types.OpusPacket, stop <-chan struct{}) {
	collector := &pcmCollector{
		format: proto.FormatInt16LE,
	}

	// Get default sink for monitor recording
	sink, err := ac.client.DefaultSink()
	if err != nil {
		log.Printf("audio: failed to get default sink: %v", err)
		return
	}

	stream, err := ac.client.NewRecord(
		collector,
		pulse.RecordMonitor(sink),
		pulse.RecordStereo,
		pulse.RecordSampleRate(sampleRate),
		pulse.RecordBufferFragmentSize(uint32(frameSize*channels*2)),
	)
	if err != nil {
		log.Printf("audio: failed to create record stream: %v", err)
		return
	}
	ac.stream = stream
	stream.Start()

	opusBuf := make([]byte, 4000)
	samplesPerFrame := frameSize * channels // 960 * 2 = 1920 int16 samples per 20ms stereo frame

	ticker := time.NewTicker(time.Duration(frameDuration) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			pcm := collector.drain(samplesPerFrame)
			if pcm == nil {
				continue
			}

			encoded, err := ac.encoder.Encode(pcm, opusBuf)
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
	if ac.stream != nil {
		ac.stream.Stop()
	}
	ac.client.Close()
}
