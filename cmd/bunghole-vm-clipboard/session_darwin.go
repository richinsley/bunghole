//go:build darwin

package main

import (
	"io"
	"log"
	"sync"

	"bunghole/internal/clipboard"
)

func runSession(conn io.ReadWriteCloser, stop <-chan struct{}) {
	defer conn.Close()

	// Guest pasteboard handler — sendFn writes frames to host over vsock
	var writeMu sync.Mutex
	sendFn := func(text string) {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := clipboard.WriteClipFrame(conn, text); err != nil {
			log.Printf("clipboard: write to host failed: %v", err)
		}
	}

	handler, err := clipboard.NewClipboardHandler("main", sendFn)
	if err != nil {
		log.Printf("clipboard handler init failed: %v", err)
		return
	}
	defer handler.Close()

	// Run pasteboard poller in background (guest copy → host)
	pollStop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		handler.Run(pollStop)
	}()

	// Read loop: host → guest pasteboard
	for {
		select {
		case <-stop:
			close(pollStop)
			wg.Wait()
			return
		default:
		}

		text, err := clipboard.ReadClipFrame(conn)
		if err != nil {
			close(pollStop)
			wg.Wait()
			return
		}

		handler.SetFromClient(text)
	}
}
