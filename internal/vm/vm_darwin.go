//go:build darwin

package vm

/*
#cgo CFLAGS: -mmacosx-version-min=14.0 -fobjc-arc
#cgo LDFLAGS: -framework Virtualization -framework Cocoa

#include <stdlib.h>
#include <stdint.h>

typedef struct {
	void *vm;
	void *view;
	void *window;
	void *delegate;
	int width;
	int height;
	uint32_t windowID;
} VMHandle;

void vm_nsapp_run(void);
void vm_nsapp_stop(void);
int  vm_create(const char *bundle_path, const char *shared_dir,
               int width, int height, VMHandle *out);
int  vm_start(VMHandle *h);
void vm_stop(VMHandle *h);
void vm_destroy(VMHandle *h);
void* vm_get_view(VMHandle *h);
uint32_t vm_get_window_id(VMHandle *h);

int vm_fetch_restore_url(char **out_url, uint64_t *out_size);
int vm_download_ipsw(const char *url, const char *dest,
                     void (*progress)(uint64_t done, uint64_t total));
int vm_create_bundle(const char *ipsw, const char *bundle, uint64_t disk_gb);
int vm_install(const char *bundle, const char *ipsw,
               void (*progress)(double fraction));
*/
import "C"
import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"unsafe"
)

var globalVM *VMManager

type VMManager struct {
	handle     C.VMHandle
	bundlePath string
	view       unsafe.Pointer
	Width      int
	Height     int
	WindowID   uint32
}

func SetGlobal(vm *VMManager) { globalVM = vm }
func GetGlobal() *VMManager   { return globalVM }

func NewVMManager(bundlePath, sharedDir string, w, h int) (*VMManager, error) {
	cBundle := C.CString(bundlePath)
	defer C.free(unsafe.Pointer(cBundle))

	var cShare *C.char
	if sharedDir != "" {
		cShare = C.CString(sharedDir)
		defer C.free(unsafe.Pointer(cShare))
	}

	var handle C.VMHandle
	if ret := C.vm_create(cBundle, cShare, C.int(w), C.int(h), &handle); ret != 0 {
		return nil, fmt.Errorf("vm_create failed")
	}

	return &VMManager{
		handle:     handle,
		bundlePath: bundlePath,
		view:       unsafe.Pointer(C.vm_get_view(&handle)),
		Width:      w,
		Height:     h,
		WindowID:   uint32(C.vm_get_window_id(&handle)),
	}, nil
}

func (vm *VMManager) Start() error {
	if ret := C.vm_start(&vm.handle); ret != 0 {
		return fmt.Errorf("vm_start failed")
	}
	log.Printf("VM started (bundle: %s)", vm.bundlePath)
	return nil
}

func (vm *VMManager) Stop() {
	C.vm_stop(&vm.handle)
	C.vm_destroy(&vm.handle)
}

func (vm *VMManager) View() unsafe.Pointer { return vm.view }

func BundlePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "bunghole", "vm")
}

func BundleExists(path string) bool {
	hwPath := filepath.Join(path, "hardware.json")
	diskPath := filepath.Join(path, "disk.img")
	_, err1 := os.Stat(hwPath)
	_, err2 := os.Stat(diskPath)
	return err1 == nil && err2 == nil
}

func RunSetup(diskGB int) {
	bundlePath := BundlePath()

	if BundleExists(bundlePath) {
		log.Printf("VM bundle already exists at %s", bundlePath)
		log.Printf("Delete it first to re-setup: rm -rf '%s'", bundlePath)
		os.Exit(1)
	}

	log.Printf("fetching latest macOS restore image URL...")
	var cURL *C.char
	var imageSize C.uint64_t
	if ret := C.vm_fetch_restore_url(&cURL, &imageSize); ret != 0 {
		log.Fatal("failed to fetch restore image URL")
	}
	restoreURL := C.GoString(cURL)
	C.free(unsafe.Pointer(cURL))

	log.Printf("restore URL: %s", restoreURL)

	ipswDir := filepath.Join(filepath.Dir(bundlePath), "cache")
	os.MkdirAll(ipswDir, 0755)
	ipswPath := filepath.Join(ipswDir, "restore.ipsw")

	if _, err := os.Stat(ipswPath); os.IsNotExist(err) {
		log.Printf("downloading macOS restore image...")
		cIPSWURL := C.CString(restoreURL)
		cIPSWDest := C.CString(ipswPath)
		defer C.free(unsafe.Pointer(cIPSWURL))
		defer C.free(unsafe.Pointer(cIPSWDest))

		if ret := C.vm_download_ipsw(cIPSWURL, cIPSWDest, nil); ret != 0 {
			log.Fatal("failed to download IPSW")
		}
		log.Printf("IPSW downloaded to %s", ipswPath)
	} else {
		log.Printf("using cached IPSW at %s", ipswPath)
	}

	log.Printf("creating VM bundle (disk: %d GB)...", diskGB)
	cIPSW := C.CString(ipswPath)
	cBundle := C.CString(bundlePath)
	defer C.free(unsafe.Pointer(cIPSW))
	defer C.free(unsafe.Pointer(cBundle))

	if ret := C.vm_create_bundle(cIPSW, cBundle, C.uint64_t(diskGB)); ret != 0 {
		log.Fatal("failed to create VM bundle")
	}

	log.Printf("installing macOS (this may take a while)...")
	if ret := C.vm_install(cBundle, cIPSW, nil); ret != 0 {
		log.Fatal("macOS installation failed")
	}

	log.Printf("macOS installed successfully!")
	log.Printf("VM bundle: %s", bundlePath)
	log.Printf("Start with: bunghole --vm --token <secret>")
}

func AutoProvision(bundlePath string) error {
	log.Printf("auto-provisioning VM (this will take a while)...")

	var cURL *C.char
	var imageSize C.uint64_t
	if ret := C.vm_fetch_restore_url(&cURL, &imageSize); ret != 0 {
		return fmt.Errorf("failed to fetch restore image URL")
	}
	restoreURL := C.GoString(cURL)
	C.free(unsafe.Pointer(cURL))

	ipswDir := filepath.Join(filepath.Dir(bundlePath), "cache")
	os.MkdirAll(ipswDir, 0755)
	ipswPath := filepath.Join(ipswDir, "restore.ipsw")

	if _, err := os.Stat(ipswPath); os.IsNotExist(err) {
		log.Printf("downloading macOS restore image...")
		cIPSWURL := C.CString(restoreURL)
		cIPSWDest := C.CString(ipswPath)
		defer C.free(unsafe.Pointer(cIPSWURL))
		defer C.free(unsafe.Pointer(cIPSWDest))

		if ret := C.vm_download_ipsw(cIPSWURL, cIPSWDest, nil); ret != 0 {
			return fmt.Errorf("IPSW download failed")
		}
	}

	cIPSW := C.CString(ipswPath)
	cBundle := C.CString(bundlePath)
	defer C.free(unsafe.Pointer(cIPSW))
	defer C.free(unsafe.Pointer(cBundle))

	if ret := C.vm_create_bundle(cIPSW, cBundle, C.uint64_t(64)); ret != 0 {
		return fmt.Errorf("bundle creation failed")
	}

	if ret := C.vm_install(cBundle, cIPSW, nil); ret != 0 {
		return fmt.Errorf("macOS installation failed")
	}

	log.Printf("auto-provision complete")
	return nil
}

func NSAppRun() {
	C.vm_nsapp_run()
}

func NSAppStop() {
	C.vm_nsapp_stop()
}
