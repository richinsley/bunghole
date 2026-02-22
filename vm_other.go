//go:build !darwin

package main

import "unsafe"

var globalVM *VMManager

type VMManager struct {
	width, height int
}

func (vm *VMManager) Window() unsafe.Pointer { return nil }
func (vm *VMManager) View() unsafe.Pointer   { return nil }

func isVMMode() bool    { return false }
func vmNSAppRun()       {}
func vmNSAppStop()      {}
func runSetup()         {}

func NewVMCapturer(window unsafe.Pointer, fps, w, h int) (MediaCapturer, error) {
	return nil, nil
}

type VMInputHandler struct{}

func NewVMInputHandler(view unsafe.Pointer) EventInjector {
	return nil
}
