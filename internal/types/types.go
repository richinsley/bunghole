package types

import (
	"image"
	"time"
	"unsafe"
)

// Frame is a captured screen frame. Either Ptr (zero-copy) or Data is populated.
type Frame struct {
	Data   []byte
	Ptr    unsafe.Pointer
	Width  int
	Height int
	Stride int
	IsCUDA bool // true = Ptr is a CUDA device pointer (NV12 format)
	PixFmt int  // 0 = BGRA (default), 1 = NV12
}

const (
	PixFmtBGRA = 0
	PixFmtNV12 = 1
)

type EncodedFrame struct {
	Data  []byte
	IsKey bool
}

type InputEvent struct {
	Type     string  `json:"type"`
	X        float64 `json:"x,omitempty"`
	Y        float64 `json:"y,omitempty"`
	DX       float64 `json:"dx,omitempty"`
	DY       float64 `json:"dy,omitempty"`
	Button   int     `json:"button,omitempty"`
	Key      string  `json:"key,omitempty"`
	Code     string  `json:"code,omitempty"`
	Relative bool    `json:"relative,omitempty"`
}

type OpusPacket struct {
	Data     []byte
	Duration time.Duration
}

type MediaCapturer interface {
	Width() int
	Height() int
	Grab() (*Frame, error)
	Close()
}

// CUDAProvider is optionally implemented by a MediaCapturer that captures
// directly to CUDA device memory (e.g. NvFBC). The encoder uses this to
// set up a CUDA hw_frames_ctx for zero-copy NVENC encoding.
type CUDAProvider interface {
	CUDAContext() unsafe.Pointer
	CuMemcpy2D() unsafe.Pointer
}

// DebugGrabber is optionally implemented by a MediaCapturer to provide
// a debug image for the /debug/frame endpoint.
type DebugGrabber interface {
	GrabImage() (image.Image, error)
}

type VideoEncoder interface {
	Encode(frame *Frame) (*EncodedFrame, error)
	Close()
}

type EventInjector interface {
	Inject(event InputEvent)
	Close()
}

type ClipboardSync interface {
	SetFromClient(text string)
	Run(stop <-chan struct{})
	Close()
}

type AudioCapturer interface {
	Run(packets chan<- *OpusPacket, stop <-chan struct{})
	Close()
}
