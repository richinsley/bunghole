package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"bunghole/internal/platform"
	"bunghole/internal/server"
)

var (
	flagDisplay = flag.String("display", "", "X11 display to capture (auto-detected or started if empty)")
	flagAddr    = flag.String("addr", ":8080", "HTTP listen address")
	flagToken   = flag.String("token", "", "Bearer token for authentication (required)")
	flagFPS     = flag.Int("fps", 30, "Capture frame rate")
	flagBitrate = flag.Int("bitrate", 4000, "Video bitrate in kbps")
	flagGPU     = flag.Int("gpu", 0, "GPU index for Xorg (0=first, 1=second)")
	flagCodec   = flag.String("codec", "h264", "Video codec (h264 or h265)")
	flagGOP     = flag.Int("gop", 0, "Keyframe interval in frames (0 = 2x FPS)")
	flagStats   = flag.Bool("stats", false, "Log pipeline stats every 5 seconds")
)

func main() {
	registerPlatformFlags()
	flag.Parse()

	cfg := &platform.Config{
		Display:    *flagDisplay,
		GPU:        *flagGPU,
	}
	fillPlatformConfig(cfg)

	// Subcommand: bunghole setup
	if flag.NArg() > 0 && flag.Arg(0) == "setup" {
		runtime.LockOSThread()
		go func() {
			platform.RunSetup(cfg)
			platform.VMNSAppStop()
		}()
		platform.VMNSAppRun()
		return
	}

	if platform.IsVMMode() {
		runtime.LockOSThread()
		go runServer(cfg)
		platform.VMNSAppRun()
	} else {
		runServer(cfg)
	}
}

func runServer(cfg *platform.Config) {
	if *flagToken == "" {
		log.Fatal("--token is required")
	}

	platform.SaveTermState()

	cleanup, err := platform.Init(cfg)
	if err != nil {
		log.Fatal(err)
	}

	// Xorg with -keeptty modifies terminal settings (clears ONLCR, etc).
	// Restore them now so our log output renders correctly.
	platform.RestoreTermState()

	if cfg.Display == "" {
		log.Fatal("no display available — use --display, set DISPLAY env, or use --start-x")
	}

	codec := *flagCodec
	if codec != "h264" && codec != "h265" {
		log.Fatalf("--codec must be h264 or h265, got %q", codec)
	}

	srv := server.New(server.Config{
		Display: cfg.Display,
		Token:   *flagToken,
		FPS:     *flagFPS,
		Bitrate: *flagBitrate,
		GPU:     *flagGPU,
		Codec:   codec,
		GOP:     *flagGOP,
		Addr:    *flagAddr,
		Stats:   *flagStats,

		NewCapturer:  newCapturer,
		NewEncoder:   newEncoder,
		InputFactory: newInputHandler,
		ClipFactory:  newClipboardHandler,
	})

	// Handle graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received %s, shutting down...", sig)
		srv.Teardown()
		cleanup()
		platform.RestoreTermState()
		if platform.IsVMMode() {
			platform.VMNSAppStop()
		}
		os.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
