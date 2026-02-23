//go:build darwin

package audio

import (
	"fmt"

	"bunghole/internal/types"
)

func NewAudioCapture() (types.AudioCapturer, error) {
	return nil, fmt.Errorf("audio capture not supported on macOS")
}
