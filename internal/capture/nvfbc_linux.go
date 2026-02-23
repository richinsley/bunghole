//go:build linux

package capture

/*
#cgo CFLAGS: -I${SRCDIR}/../../cvendor
#include <stdlib.h>
#include <string.h>
#include <dlfcn.h>
#include <stdio.h>
#include <time.h>
#include "cuda_defs.h"
#include "nvfbc.h"

// ---------------------------------------------------------------------------
// NvFBC TOCUDA capturer
// ---------------------------------------------------------------------------

// Dynamically loaded CUDA driver API function pointers
static PFN_cuInit         fn_cuInit = NULL;
static PFN_cuDeviceGet    fn_cuDeviceGet = NULL;
static PFN_cuDeviceGetName fn_cuDeviceGetName = NULL;
static PFN_cuDeviceGetByPCIBusId fn_cuDeviceGetByPCIBusId = NULL;
static PFN_cuCtxCreate    fn_cuCtxCreate = NULL;
static PFN_cuCtxDestroy   fn_cuCtxDestroy = NULL;
static PFN_cuCtxSetCurrent fn_cuCtxSetCurrent = NULL;
static PFN_cuCtxGetCurrent fn_cuCtxGetCurrent = NULL;
static PFN_cuMemcpyDtoH fn_cuMemcpyDtoH = NULL;
static void *fn_cuMemcpy2D_ptr = NULL;

typedef struct {
	void *cuda_lib;                    // dlopen handle for libcuda.so.1
	void *nvfbc_lib;                   // dlopen handle for libnvidia-fbc.so.1
	NVFBC_API_FUNCTION_LIST fn;        // NvFBC function pointers
	NVFBC_SESSION_HANDLE session;
	CUcontext cuda_ctx;
	CUdeviceptr frame_ptr;             // last captured frame CUDA device pointer
	CUdeviceptr grab_ptr;              // target for grab (separate to preserve frame_ptr on failure)
	NVFBC_FRAME_GRAB_INFO grab_info;   // last grab info
	int width;
	int height;
	int stride;
} NvFBCCapturer;

// Load CUDA driver API dynamically
static int load_cuda(NvFBCCapturer *c) {
	c->cuda_lib = dlopen("libcuda.so.1", RTLD_LAZY);
	if (!c->cuda_lib) {
		c->cuda_lib = dlopen("libcuda.so", RTLD_LAZY);
	}
	if (!c->cuda_lib) {
		fprintf(stderr, "nvfbc: failed to load libcuda.so: %s\n", dlerror());
		return -1;
	}

	fn_cuInit = (PFN_cuInit)dlsym(c->cuda_lib, "cuInit");
	fn_cuDeviceGet = (PFN_cuDeviceGet)dlsym(c->cuda_lib, "cuDeviceGet");
	fn_cuDeviceGetName = (PFN_cuDeviceGetName)dlsym(c->cuda_lib, "cuDeviceGetName");
	fn_cuDeviceGetByPCIBusId = (PFN_cuDeviceGetByPCIBusId)dlsym(c->cuda_lib, "cuDeviceGetByPCIBusId");
	fn_cuCtxCreate = (PFN_cuCtxCreate)dlsym(c->cuda_lib, "cuCtxCreate_v2");
	if (!fn_cuCtxCreate)
		fn_cuCtxCreate = (PFN_cuCtxCreate)dlsym(c->cuda_lib, "cuCtxCreate");
	fn_cuCtxDestroy = (PFN_cuCtxDestroy)dlsym(c->cuda_lib, "cuCtxDestroy_v2");
	if (!fn_cuCtxDestroy)
		fn_cuCtxDestroy = (PFN_cuCtxDestroy)dlsym(c->cuda_lib, "cuCtxDestroy");
	fn_cuCtxSetCurrent = (PFN_cuCtxSetCurrent)dlsym(c->cuda_lib, "cuCtxSetCurrent");
	fn_cuCtxGetCurrent = (PFN_cuCtxGetCurrent)dlsym(c->cuda_lib, "cuCtxGetCurrent");
	fn_cuMemcpyDtoH = (PFN_cuMemcpyDtoH)dlsym(c->cuda_lib, "cuMemcpyDtoH_v2");
	if (!fn_cuMemcpyDtoH)
		fn_cuMemcpyDtoH = (PFN_cuMemcpyDtoH)dlsym(c->cuda_lib, "cuMemcpyDtoH");

	fn_cuMemcpy2D_ptr = dlsym(c->cuda_lib, "cuMemcpy2D_v2");
	if (!fn_cuMemcpy2D_ptr)
		fn_cuMemcpy2D_ptr = dlsym(c->cuda_lib, "cuMemcpy2D");

	if (!fn_cuInit || !fn_cuDeviceGet || !fn_cuCtxCreate ||
	    !fn_cuCtxDestroy || !fn_cuCtxSetCurrent) {
		fprintf(stderr, "nvfbc: failed to resolve CUDA symbols\n");
		dlclose(c->cuda_lib);
		c->cuda_lib = NULL;
		return -1;
	}

	return 0;
}

// Helper to log NvFBC's last error string
static void nvfbc_log_error(NvFBCCapturer *c, const char *context) {
	if (c->fn.nvFBCGetLastErrorStr) {
		const char *errStr = c->fn.nvFBCGetLastErrorStr(c->session);
		if (errStr && errStr[0]) {
			fprintf(stderr, "nvfbc: %s: %s\n", context, errStr);
			return;
		}
	}
	fprintf(stderr, "nvfbc: %s (no error string available)\n", context);
}

// Cleanup helper to avoid repeating teardown code
static void nvfbc_cleanup(NvFBCCapturer *c, int has_session, int has_handle) {
	if (has_session && c->fn.nvFBCDestroyCaptureSession) {
		NVFBC_DESTROY_CAPTURE_SESSION_PARAMS dcsParams;
		memset(&dcsParams, 0, sizeof(dcsParams));
		dcsParams.dwVersion = NVFBC_DESTROY_CAPTURE_SESSION_PARAMS_VER;
		c->fn.nvFBCDestroyCaptureSession(c->session, &dcsParams);
	}
	if (has_handle && c->fn.nvFBCDestroyHandle) {
		NVFBC_DESTROY_HANDLE_PARAMS dp;
		memset(&dp, 0, sizeof(dp));
		dp.dwVersion = NVFBC_DESTROY_HANDLE_PARAMS_VER;
		c->fn.nvFBCDestroyHandle(c->session, &dp);
	}
	if (c->cuda_ctx && fn_cuCtxDestroy) fn_cuCtxDestroy(c->cuda_ctx);
	if (c->nvfbc_lib) dlclose(c->nvfbc_lib);
	if (c->cuda_lib) dlclose(c->cuda_lib);
	free(c);
}

static NvFBCCapturer* nvfbc_init(const char *display_name, int fps, const char *pci_bus_id) {
	NvFBCCapturer *c = (NvFBCCapturer*)calloc(1, sizeof(NvFBCCapturer));
	if (!c) return NULL;

	// Step 1: Load CUDA
	if (load_cuda(c) != 0) {
		free(c);
		return NULL;
	}

	// Step 2: Initialize CUDA driver API
	CUresult cr = fn_cuInit(0);
	if (cr != CUDA_SUCCESS) {
		fprintf(stderr, "nvfbc: cuInit failed: %d\n", cr);
		dlclose(c->cuda_lib);
		free(c);
		return NULL;
	}

	// Step 2b: Find CUDA device by PCI Bus ID (nvidia-smi and CUDA use
	// different ordinals, so we must match by bus ID, not index)
	CUdevice device;
	if (fn_cuDeviceGetByPCIBusId) {
		cr = fn_cuDeviceGetByPCIBusId(&device, pci_bus_id);
		if (cr != CUDA_SUCCESS) {
			fprintf(stderr, "nvfbc: cuDeviceGetByPCIBusId(%s) failed: %d\n", pci_bus_id, cr);
			dlclose(c->cuda_lib);
			free(c);
			return NULL;
		}
	} else {
		fprintf(stderr, "nvfbc: cuDeviceGetByPCIBusId not available, falling back to device 0\n");
		cr = fn_cuDeviceGet(&device, 0);
		if (cr != CUDA_SUCCESS) {
			fprintf(stderr, "nvfbc: cuDeviceGet(0) failed: %d\n", cr);
			dlclose(c->cuda_lib);
			free(c);
			return NULL;
		}
	}

	if (fn_cuDeviceGetName) {
		char devName[256] = {0};
		fn_cuDeviceGetName(devName, sizeof(devName), device);
		fprintf(stderr, "nvfbc: CUDA device [%s]: %s\n", pci_bus_id, devName);
	}

	cr = fn_cuCtxCreate(&c->cuda_ctx, 0, device);
	if (cr != CUDA_SUCCESS) {
		fprintf(stderr, "nvfbc: cuCtxCreate failed: %d\n", cr);
		dlclose(c->cuda_lib);
		free(c);
		return NULL;
	}
	fprintf(stderr, "nvfbc: CUDA context created on %s\n", pci_bus_id);

	// Step 3: Load NvFBC
	c->nvfbc_lib = dlopen("libnvidia-fbc.so.1", RTLD_LAZY);
	if (!c->nvfbc_lib) {
		fprintf(stderr, "nvfbc: failed to load libnvidia-fbc.so.1: %s\n", dlerror());
		nvfbc_cleanup(c, 0, 0);
		return NULL;
	}

	PFN_NvFBCCreateInstance createInstance =
		(PFN_NvFBCCreateInstance)dlsym(c->nvfbc_lib, "NvFBCCreateInstance");
	if (!createInstance) {
		fprintf(stderr, "nvfbc: NvFBCCreateInstance not found\n");
		nvfbc_cleanup(c, 0, 0);
		return NULL;
	}

	memset(&c->fn, 0, sizeof(c->fn));
	c->fn.dwVersion = NVFBC_VERSION;

	NVFBCSTATUS status = createInstance(&c->fn);
	if (status != NVFBC_SUCCESS) {
		fprintf(stderr, "nvfbc: NvFBCCreateInstance failed: %d\n", status);
		nvfbc_cleanup(c, 0, 0);
		return NULL;
	}

	// Step 4: Create NvFBC handle
	NVFBC_CREATE_HANDLE_PARAMS handleParams;
	memset(&handleParams, 0, sizeof(handleParams));
	handleParams.dwVersion = NVFBC_CREATE_HANDLE_PARAMS_VER;

	status = c->fn.nvFBCCreateHandle(&c->session, &handleParams);
	if (status != NVFBC_SUCCESS) {
		fprintf(stderr, "nvfbc: NvFBCCreateHandle failed: %d\n", status);
		nvfbc_log_error(c, "NvFBCCreateHandle");
		nvfbc_cleanup(c, 0, 0);
		return NULL;
	}

	// Step 5: Get status (screen dimensions)
	NVFBC_GET_STATUS_PARAMS statusParams;
	memset(&statusParams, 0, sizeof(statusParams));
	statusParams.dwVersion = NVFBC_GET_STATUS_PARAMS_VER;

	status = c->fn.nvFBCGetStatus(c->session, &statusParams);
	if (status != NVFBC_SUCCESS) {
		fprintf(stderr, "nvfbc: NvFBCGetStatus failed: %d\n", status);
		nvfbc_log_error(c, "NvFBCGetStatus");
		nvfbc_cleanup(c, 0, 1);
		return NULL;
	}

	if (!statusParams.bIsCapturePossible) {
		fprintf(stderr, "nvfbc: capture not possible on this GPU\n");
		nvfbc_cleanup(c, 0, 1);
		return NULL;
	}

	c->width = statusParams.screenSize.w;
	c->height = statusParams.screenSize.h;

	// Step 6: Create capture session
	NVFBC_CREATE_CAPTURE_SESSION_PARAMS captureParams;
	memset(&captureParams, 0, sizeof(captureParams));
	captureParams.dwVersion = NVFBC_CREATE_CAPTURE_SESSION_PARAMS_VER;
	captureParams.eCaptureType = NVFBC_CAPTURE_SHARED_CUDA;
	captureParams.eTrackingType = NVFBC_TRACKING_DEFAULT;
	captureParams.bWithCursor = NVFBC_TRUE;
	captureParams.dwSamplingRateMs = fps > 0 ? 1000 / fps : 33;
	captureParams.bPushModel = NVFBC_FALSE;

	status = c->fn.nvFBCCreateCaptureSession(c->session, &captureParams);
	if (status != NVFBC_SUCCESS) {
		fprintf(stderr, "nvfbc: NvFBCCreateCaptureSession failed: %d\n", status);
		nvfbc_log_error(c, "NvFBCCreateCaptureSession");
		nvfbc_cleanup(c, 0, 1);
		return NULL;
	}

	// Step 7: Set up TOCUDA with NV12 output
	NVFBC_TOCUDA_SETUP_PARAMS setupParams;
	memset(&setupParams, 0, sizeof(setupParams));
	setupParams.dwVersion = NVFBC_TOCUDA_SETUP_PARAMS_VER;
	setupParams.eBufferFormat = NVFBC_BUFFER_FORMAT_NV12;

	status = c->fn.nvFBCToCudaSetUp(c->session, &setupParams);
	if (status != NVFBC_SUCCESS) {
		fprintf(stderr, "nvfbc: NvFBCToCudaSetUp failed: %d\n", status);
		nvfbc_log_error(c, "NvFBCToCudaSetUp");
		nvfbc_cleanup(c, 1, 1);
		return NULL;
	}

	// NV12 stride is typically width aligned to 256 bytes for NVENC
	c->stride = (c->width + 255) & ~255;

	fprintf(stderr, "nvfbc: initialized %dx%d capture (TOCUDA, poll+force_refresh)\n",
		c->width, c->height);
	return c;
}

// Returns: 0=success (new frame), 1=reused last frame, -1=error
static int nvfbc_grab(NvFBCCapturer *c) {
	struct timespec t0, t1;
	clock_gettime(CLOCK_MONOTONIC, &t0);

	// Use a separate grab target so NvFBC can't clear our saved frame_ptr.
	// NvFBC may write NULL to pCUDADeviceBuffer on failed grabs.
	c->grab_ptr = 0;

	NVFBC_TOCUDA_GRAB_FRAME_PARAMS grabParams;
	memset(&grabParams, 0, sizeof(grabParams));
	grabParams.dwVersion = NVFBC_TOCUDA_GRAB_FRAME_PARAMS_VER;
	grabParams.dwFlags = NVFBC_TOCUDA_GRAB_FLAGS_FORCE_REFRESH
	                   | NVFBC_TOCUDA_GRAB_FLAGS_NOWAIT;
	grabParams.pCUDADeviceBuffer = (void*)&c->grab_ptr;
	grabParams.pFrameGrabInfo = &c->grab_info;
	grabParams.dwTimeoutMs = 0;

	NVFBCSTATUS status = c->fn.nvFBCToCudaGrabFrame(c->session, &grabParams);

	// NvFBC with bExternallyManagedContext=FALSE manages its own CUDA context
	// internally. After the grab, restore our context for the encoder.
	if (fn_cuCtxSetCurrent) fn_cuCtxSetCurrent(c->cuda_ctx);

	clock_gettime(CLOCK_MONOTONIC, &t1);
	double grab_ms = (t1.tv_sec - t0.tv_sec) * 1000.0 +
	                 (t1.tv_nsec - t0.tv_nsec) / 1e6;

	static int grab_count = 0;
	static int new_count = 0;
	static int reuse_count = 0;
	static int fail_count = 0;
	static double grab_ms_total = 0;
	static struct timespec last_report = {0};

	grab_count++;
	grab_ms_total += grab_ms;

	if (status != NVFBC_SUCCESS) {
		// Grab failed — reuse last good frame if we have one
		if (c->frame_ptr) {
			reuse_count++;

			// Report stats every 5 seconds
			if (last_report.tv_sec == 0) last_report = t1;
			double elapsed = (t1.tv_sec - last_report.tv_sec) +
			                 (t1.tv_nsec - last_report.tv_nsec) / 1e9;
			if (elapsed >= 5.0) {
				fprintf(stderr, "nvfbc: grabs=%d new=%d reuse=%d fail=%d avg=%.2fms status=%d\n",
					grab_count, new_count, reuse_count, fail_count,
					grab_ms_total / grab_count, status);
				grab_count = new_count = reuse_count = fail_count = 0;
				grab_ms_total = 0;
				last_report = t1;
			}

			return 1;
		}
		fail_count++;
		return -1;
	}

	// Success — update frame_ptr from grab target
	c->frame_ptr = c->grab_ptr;
	new_count++;

	// Update dimensions from grab info (may differ on resolution change)
	c->width = c->grab_info.dwWidth;
	c->height = c->grab_info.dwHeight;

	// Calculate stride from dwByteSize: NV12 total = stride * height * 3/2
	if (c->grab_info.dwByteSize > 0 && c->height > 0) {
		c->stride = c->grab_info.dwByteSize / (c->height * 3 / 2);
	} else {
		c->stride = (c->width + 255) & ~255;
	}

	static int first_grab = 1;
	if (first_grab) {
		fprintf(stderr, "nvfbc: first grab: %dx%d stride=%d\n",
			c->width, c->height, c->stride);
		first_grab = 0;
	}

	// Report stats every 5 seconds
	if (last_report.tv_sec == 0) last_report = t1;
	double elapsed = (t1.tv_sec - last_report.tv_sec) +
	                 (t1.tv_nsec - last_report.tv_nsec) / 1e9;
	if (elapsed >= 5.0) {
		fprintf(stderr, "nvfbc: grabs=%d new=%d reuse=%d fail=%d avg=%.2fms\n",
			grab_count, new_count, reuse_count, fail_count,
			grab_ms_total / grab_count);
		grab_count = new_count = reuse_count = fail_count = 0;
		grab_ms_total = 0;
		last_report = t1;
	}

	return 0;
}

// Return the last captured frame's CUDA device pointer as a void* for Go.
static void* nvfbc_frame_ptr(NvFBCCapturer *c) {
	return (void*)(uintptr_t)c->frame_ptr;
}

// Download the NV12 CUDA frame to CPU memory. Caller must free the returned buffer.
// Returns NULL on failure. *out_size receives the total byte size.
static uint8_t* nvfbc_download_frame(NvFBCCapturer *c, int *out_size) {
	if (!fn_cuMemcpyDtoH || !c->frame_ptr) return NULL;
	int total = c->stride * c->height * 3 / 2; // NV12
	uint8_t *buf = (uint8_t*)malloc(total);
	if (!buf) return NULL;
	CUresult r = fn_cuMemcpyDtoH(buf, c->frame_ptr, total);
	if (r != CUDA_SUCCESS) {
		fprintf(stderr, "nvfbc: cuMemcpyDtoH failed: %d\n", r);
		free(buf);
		return NULL;
	}
	*out_size = total;
	return buf;
}

static void nvfbc_destroy(NvFBCCapturer *c) {
	if (!c) return;

	if (c->fn.nvFBCDestroyCaptureSession) {
		NVFBC_DESTROY_CAPTURE_SESSION_PARAMS dcsParams;
		memset(&dcsParams, 0, sizeof(dcsParams));
		dcsParams.dwVersion = NVFBC_DESTROY_CAPTURE_SESSION_PARAMS_VER;
		c->fn.nvFBCDestroyCaptureSession(c->session, &dcsParams);
	}

	if (c->fn.nvFBCDestroyHandle) {
		NVFBC_DESTROY_HANDLE_PARAMS destroyParams;
		memset(&destroyParams, 0, sizeof(destroyParams));
		destroyParams.dwVersion = NVFBC_DESTROY_HANDLE_PARAMS_VER;
		c->fn.nvFBCDestroyHandle(c->session, &destroyParams);
	}

	if (c->cuda_ctx && fn_cuCtxDestroy) {
		fn_cuCtxDestroy(c->cuda_ctx);
	}

	// Do NOT dlclose cuda_lib or nvfbc_lib — the static function pointers
	// (fn_cuInit, fn_cuCtxCreate, etc.) are shared across all capturers.
	// Closing the library would make them dangling pointers.
	free(c);
}

// Return the cuMemcpy2D function pointer for the encoder to use.
static void* get_cuMemcpy2D_ptr(void) {
	return fn_cuMemcpy2D_ptr;
}
*/
import "C"
import (
	"fmt"
	"image"
	"image/color"
	"log"
	"unsafe"

	"bunghole/internal/types"
)

// NvfbcCapturer captures frames via NvFBC TOCUDA (zero-copy GPU capture).
type NvfbcCapturer struct {
	c   *C.NvFBCCapturer
	fps int
}

// NewNvFBCCapturer creates an NvFBC TOCUDA capturer for the given PCI bus ID.
func NewNvFBCCapturer(displayName string, fps int, pciBusID string) (types.MediaCapturer, error) {
	cDisplay := C.CString(displayName)
	defer C.free(unsafe.Pointer(cDisplay))
	cBusID := C.CString(pciBusID)
	defer C.free(unsafe.Pointer(cBusID))

	c := C.nvfbc_init(cDisplay, C.int(fps), cBusID)
	if c == nil {
		return nil, fmt.Errorf("failed to initialize NvFBC capture")
	}
	log.Printf("capture: NvFBC (%dx%d)", int(c.width), int(c.height))
	return &NvfbcCapturer{c: c, fps: fps}, nil
}

func (c *NvfbcCapturer) Width() int  { return int(c.c.width) }
func (c *NvfbcCapturer) Height() int { return int(c.c.height) }

func (c *NvfbcCapturer) Grab() (*types.Frame, error) {
	ret := C.nvfbc_grab(c.c)
	if ret < 0 {
		return nil, fmt.Errorf("NvFBC grab failed")
	}

	return &types.Frame{
		Ptr:    unsafe.Pointer(C.nvfbc_frame_ptr(c.c)),
		Width:  int(c.c.width),
		Height: int(c.c.height),
		Stride: int(c.c.stride),
		IsCUDA: true,
		PixFmt: types.PixFmtNV12,
	}, nil
}

// CUDAContext returns the CUDA context for the encoder to share.
func (c *NvfbcCapturer) CUDAContext() unsafe.Pointer {
	return unsafe.Pointer(c.c.cuda_ctx)
}

// CuMemcpy2D returns the cuMemcpy2D function pointer for the encoder.
func (c *NvfbcCapturer) CuMemcpy2D() unsafe.Pointer {
	return unsafe.Pointer(C.get_cuMemcpy2D_ptr())
}

// GrabImage grabs a frame and returns it as a Go image (for debug endpoint).
func (c *NvfbcCapturer) GrabImage() (image.Image, error) {
	if C.nvfbc_grab(c.c) != 0 {
		return nil, fmt.Errorf("NvFBC grab failed")
	}
	w := int(c.c.width)
	h := int(c.c.height)
	stride := int(c.c.stride)

	var outSize C.int
	buf := C.nvfbc_download_frame(c.c, &outSize)
	if buf == nil {
		return nil, fmt.Errorf("failed to download CUDA frame")
	}
	defer C.free(unsafe.Pointer(buf))

	nv12 := C.GoBytes(unsafe.Pointer(buf), outSize)
	return nv12ToImage(nv12, w, h, stride), nil
}

func (c *NvfbcCapturer) Close() {
	C.nvfbc_destroy(c.c)
}

// nv12ToImage converts NV12 pixel data to an RGBA image.
func nv12ToImage(nv12 []byte, w, h, stride int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	yOff := 0
	uvOff := stride * h
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			yVal := int(nv12[yOff+y*stride+x])
			uvIdx := uvOff + (y/2)*stride + (x&^1)
			uVal := int(nv12[uvIdx]) - 128
			vVal := int(nv12[uvIdx+1]) - 128
			r := yVal + (91881*vVal+32768)>>16
			g := yVal - (22554*uVal+46802*vVal+32768)>>16
			b := yVal + (116130*uVal+32768)>>16
			if r < 0 {
				r = 0
			} else if r > 255 {
				r = 255
			}
			if g < 0 {
				g = 0
			} else if g > 255 {
				g = 255
			}
			if b < 0 {
				b = 0
			} else if b > 255 {
				b = 255
			}
			img.SetRGBA(x, y, color.RGBA{uint8(r), uint8(g), uint8(b), 255})
		}
	}
	return img
}
