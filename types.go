package main

import (
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
}

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
