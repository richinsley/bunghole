//go:build !darwin

package audio

import (
	"net"

	"bunghole/internal/types"
)

type VsockAudioCapture struct{}

func NewVsockAudioCapture(_ <-chan net.Conn) *VsockAudioCapture {
	return &VsockAudioCapture{}
}

func (ac *VsockAudioCapture) Run(_ chan<- *types.OpusPacket, stop <-chan struct{}) {
	<-stop
}

func (ac *VsockAudioCapture) Close() {}
