//go:build linux

package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

var (
	flagStartX     = flag.Bool("start-x", false, "Start a new Xorg server with nvidia driver")
	flagResolution = flag.String("resolution", "1920x1080", "Screen resolution when starting X server")
)

// platformInit handles Linux-specific startup (headless X server).
// Returns a cleanup function and may modify the display string.
func platformInit(display *string, gpu int) (func(), error) {
	if *flagStartX || *display == "" {
		if *display == "" {
			*display = os.Getenv("DISPLAY")
		}

		if *display == "" || *flagStartX {
			xserver, err := StartXServer(*flagResolution, gpu)
			if err != nil {
				return nil, fmt.Errorf("failed to start X server: %v", err)
			}
			*display = xserver.Display
			os.Setenv("DISPLAY", *display)
			os.Setenv("XAUTHORITY", xserver.Xauthority)

			if err := xserver.StartDesktopSession(*flagResolution); err != nil {
				log.Printf("warning: failed to start desktop session: %v", err)
				log.Printf("X server is running on %s but no desktop — you may want to start one manually", *display)
			}

			if xserver.PulseServer != "" {
				os.Setenv("PULSE_SERVER", xserver.PulseServer)
				log.Printf("audio: using %s", xserver.PulseServer)
			}

			return func() { xserver.Stop() }, nil
		}
	}
	return func() {}, nil
}
