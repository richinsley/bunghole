//go:build darwin

package main

import "fmt"

func NewAudioCapture() (AudioCapturer, error) {
	return nil, fmt.Errorf("audio capture not supported on macOS")
}
