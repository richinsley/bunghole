//go:build !darwin

package vm

import "unsafe"

var globalVM *VMManager

type VMManager struct {
	Width, Height int
}

func SetGlobal(vm *VMManager) { globalVM = vm }
func GetGlobal() *VMManager   { return globalVM }

func (vm *VMManager) Window() unsafe.Pointer { return nil }
func (vm *VMManager) View() unsafe.Pointer   { return nil }

func BundlePath() string             { return "" }
func BundleExists(path string) bool  { return false }
func RunSetup(diskGB int)            {}
func AutoProvision(path string) error { return nil }
func NSAppRun()                      {}
func NSAppStop()                     {}
