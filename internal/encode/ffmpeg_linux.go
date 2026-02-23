//go:build linux

package encode

/*
#cgo pkg-config: libavcodec libavutil libswscale
#cgo CFLAGS: -I${SRCDIR}/../../cvendor
#include <libavcodec/avcodec.h>
#include <libavutil/imgutils.h>
#include <libavutil/opt.h>
#include <libavutil/hwcontext.h>
#include <libavutil/hwcontext_cuda.h>
#include <libswscale/swscale.h>
#include <stdlib.h>
#include <string.h>
#include "cuda_defs.h"

// ---------------------------------------------------------------------------
// CPU encoder — sws_scale BGRA→NV12/YUV420P, then avcodec_send_frame.
// Used when XShm fallback is active (no CUDA context).
// ---------------------------------------------------------------------------

typedef struct {
	AVCodecContext *ctx;
	AVFrame *frame;
	AVPacket *pkt;
	struct SwsContext *sws;
	int width;
	int height;
	int64_t pts;
} CPUEncoder;

static CPUEncoder* cpu_encoder_init(int width, int height, int fps,
                                     int bitrate_kbps, int keyint,
                                     int gpu_index, const char *codec_name) {
	CPUEncoder *e = (CPUEncoder*)calloc(1, sizeof(CPUEncoder));
	if (!e) return NULL;

	e->width = width;
	e->height = height;
	e->pts = 0;

	const AVCodec *codec = NULL;
	int is_hevc = (strcmp(codec_name, "h265") == 0);

	if (is_hevc) {
		codec = avcodec_find_encoder_by_name("hevc_nvenc");
		if (!codec) codec = avcodec_find_encoder_by_name("libx265");
	} else {
		codec = avcodec_find_encoder_by_name("h264_nvenc");
		if (!codec) codec = avcodec_find_encoder_by_name("libx264");
	}
	if (!codec) return NULL;

	e->ctx = avcodec_alloc_context3(codec);
	if (!e->ctx) { free(e); return NULL; }

	e->ctx->width = width;
	e->ctx->height = height;
	e->ctx->time_base = (AVRational){1, fps};
	e->ctx->framerate = (AVRational){fps, 1};
	e->ctx->pix_fmt = AV_PIX_FMT_NV12;
	e->ctx->bit_rate = (int64_t)bitrate_kbps * 1000;
	e->ctx->gop_size = keyint;
	e->ctx->max_b_frames = 0;

	if (strcmp(codec->name, "h264_nvenc") == 0) {
		av_opt_set(e->ctx->priv_data, "preset", "p1", 0);
		av_opt_set(e->ctx->priv_data, "tune", "ull", 0);
		av_opt_set(e->ctx->priv_data, "profile", "baseline", 0);
		av_opt_set(e->ctx->priv_data, "rc", "cbr", 0);
		av_opt_set(e->ctx->priv_data, "zerolatency", "1", 0);
		av_opt_set_int(e->ctx->priv_data, "gpu", gpu_index, 0);
	} else if (strcmp(codec->name, "hevc_nvenc") == 0) {
		av_opt_set(e->ctx->priv_data, "preset", "p1", 0);
		av_opt_set(e->ctx->priv_data, "tune", "ull", 0);
		av_opt_set(e->ctx->priv_data, "profile", "main", 0);
		av_opt_set(e->ctx->priv_data, "rc", "cbr", 0);
		av_opt_set(e->ctx->priv_data, "zerolatency", "1", 0);
		av_opt_set_int(e->ctx->priv_data, "gpu", gpu_index, 0);
	} else if (strcmp(codec->name, "libx265") == 0) {
		av_opt_set(e->ctx->priv_data, "preset", "ultrafast", 0);
		av_opt_set(e->ctx->priv_data, "tune", "zerolatency", 0);
		e->ctx->pix_fmt = AV_PIX_FMT_YUV420P;
	} else {
		// libx264 fallback
		av_opt_set(e->ctx->priv_data, "preset", "ultrafast", 0);
		av_opt_set(e->ctx->priv_data, "tune", "zerolatency", 0);
		av_opt_set(e->ctx->priv_data, "profile", "baseline", 0);
		e->ctx->pix_fmt = AV_PIX_FMT_YUV420P;
	}

	e->ctx->flags |= AV_CODEC_FLAG_LOW_DELAY;

	if (avcodec_open2(e->ctx, codec, NULL) < 0) {
		avcodec_free_context(&e->ctx);
		free(e);
		return NULL;
	}

	e->frame = av_frame_alloc();
	e->frame->format = e->ctx->pix_fmt;
	e->frame->width = width;
	e->frame->height = height;
	av_frame_get_buffer(e->frame, 0);

	e->pkt = av_packet_alloc();

	e->sws = sws_getContext(
		width, height, AV_PIX_FMT_BGRA,
		width, height, e->ctx->pix_fmt,
		SWS_FAST_BILINEAR, NULL, NULL, NULL);

	if (!e->sws) {
		av_packet_free(&e->pkt);
		av_frame_free(&e->frame);
		avcodec_free_context(&e->ctx);
		free(e);
		return NULL;
	}

	return e;
}

static int cpu_encoder_encode(CPUEncoder *e, const uint8_t *bgra, int stride,
                               uint8_t **out_buf, int *out_size, int *is_key) {
	*out_size = 0;

	const uint8_t *src_data[1] = { bgra };
	int src_linesize[1] = { stride };

	av_frame_make_writable(e->frame);
	sws_scale(e->sws, src_data, src_linesize, 0, e->height,
	          e->frame->data, e->frame->linesize);

	e->frame->pts = e->pts++;

	int ret = avcodec_send_frame(e->ctx, e->frame);
	if (ret < 0) return -1;

	ret = avcodec_receive_packet(e->ctx, e->pkt);
	if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) return 0;
	if (ret < 0) return -1;

	*out_buf = e->pkt->data;
	*out_size = e->pkt->size;
	*is_key = (e->pkt->flags & AV_PKT_FLAG_KEY) ? 1 : 0;
	return 0;
}

static void cpu_encoder_unref(CPUEncoder *e) { av_packet_unref(e->pkt); }

static const char* cpu_encoder_name(CPUEncoder *e) { return e->ctx->codec->name; }

static void cpu_encoder_destroy(CPUEncoder *e) {
	if (!e) return;
	if (e->sws) sws_freeContext(e->sws);
	if (e->pkt) av_packet_free(&e->pkt);
	if (e->frame) av_frame_free(&e->frame);
	if (e->ctx) avcodec_free_context(&e->ctx);
	free(e);
}

// ---------------------------------------------------------------------------
// CUDA encoder — receives NV12 CUDA device pointer from NvFBC,
// wraps it in an AVFrame with AV_PIX_FMT_CUDA, encodes via NVENC.
// Zero CPU involvement in the video path.
// ---------------------------------------------------------------------------

typedef struct {
	AVCodecContext *ctx;
	AVBufferRef *hw_device_ctx;
	AVBufferRef *hw_frames_ctx;
	AVFrame *frame;
	AVPacket *pkt;
	int width;
	int height;
	int64_t pts;
	void *cuMemcpy2D_fn; // cuMemcpy2D function pointer (passed from capturer via Go)
} CUDAEncoder;

static CUDAEncoder* cuda_encoder_init(int width, int height, int fps,
                                       int bitrate_kbps, int keyint,
                                       int gpu_index, const char *codec_name,
                                       void *cuda_ctx_ptr, void *cuMemcpy2D_fn) {
	CUcontext cuda_ctx = (CUcontext)cuda_ctx_ptr;
	CUDAEncoder *e = (CUDAEncoder*)calloc(1, sizeof(CUDAEncoder));
	if (!e) return NULL;

	e->width = width;
	e->height = height;
	e->pts = 0;
	e->cuMemcpy2D_fn = cuMemcpy2D_fn;

	// Create hw device context from existing CUDA context
	e->hw_device_ctx = av_hwdevice_ctx_alloc(AV_HWDEVICE_TYPE_CUDA);
	if (!e->hw_device_ctx) { free(e); return NULL; }

	AVHWDeviceContext *device_ctx = (AVHWDeviceContext*)e->hw_device_ctx->data;
	AVCUDADeviceContext *cuda_device_ctx = (AVCUDADeviceContext*)device_ctx->hwctx;
	cuda_device_ctx->cuda_ctx = cuda_ctx;
	// Let FFmpeg manage the internal CUDA state
	cuda_device_ctx->internal = NULL;

	int ret = av_hwdevice_ctx_init(e->hw_device_ctx);
	if (ret < 0) {
		av_buffer_unref(&e->hw_device_ctx);
		free(e);
		return NULL;
	}

	// Create hw frames context
	e->hw_frames_ctx = av_hwframe_ctx_alloc(e->hw_device_ctx);
	if (!e->hw_frames_ctx) {
		av_buffer_unref(&e->hw_device_ctx);
		free(e);
		return NULL;
	}

	AVHWFramesContext *frames_ctx = (AVHWFramesContext*)e->hw_frames_ctx->data;
	frames_ctx->format = AV_PIX_FMT_CUDA;
	frames_ctx->sw_format = AV_PIX_FMT_NV12;
	frames_ctx->width = width;
	frames_ctx->height = height;
	frames_ctx->initial_pool_size = 1;

	ret = av_hwframe_ctx_init(e->hw_frames_ctx);
	if (ret < 0) {
		av_buffer_unref(&e->hw_frames_ctx);
		av_buffer_unref(&e->hw_device_ctx);
		free(e);
		return NULL;
	}

	// Find NVENC codec
	const AVCodec *codec = NULL;
	int is_hevc = (strcmp(codec_name, "h265") == 0);

	if (is_hevc) {
		codec = avcodec_find_encoder_by_name("hevc_nvenc");
	} else {
		codec = avcodec_find_encoder_by_name("h264_nvenc");
	}
	if (!codec) {
		av_buffer_unref(&e->hw_frames_ctx);
		av_buffer_unref(&e->hw_device_ctx);
		free(e);
		return NULL;
	}

	e->ctx = avcodec_alloc_context3(codec);
	if (!e->ctx) {
		av_buffer_unref(&e->hw_frames_ctx);
		av_buffer_unref(&e->hw_device_ctx);
		free(e);
		return NULL;
	}

	e->ctx->width = width;
	e->ctx->height = height;
	e->ctx->time_base = (AVRational){1, fps};
	e->ctx->framerate = (AVRational){fps, 1};
	e->ctx->pix_fmt = AV_PIX_FMT_CUDA;
	e->ctx->sw_pix_fmt = AV_PIX_FMT_NV12;
	e->ctx->bit_rate = (int64_t)bitrate_kbps * 1000;
	e->ctx->gop_size = keyint;
	e->ctx->max_b_frames = 0;
	e->ctx->hw_frames_ctx = av_buffer_ref(e->hw_frames_ctx);

	if (strcmp(codec->name, "h264_nvenc") == 0) {
		av_opt_set(e->ctx->priv_data, "preset", "p1", 0);
		av_opt_set(e->ctx->priv_data, "tune", "ull", 0);
		av_opt_set(e->ctx->priv_data, "profile", "baseline", 0);
		av_opt_set(e->ctx->priv_data, "rc", "cbr", 0);
		av_opt_set(e->ctx->priv_data, "zerolatency", "1", 0);
		av_opt_set_int(e->ctx->priv_data, "gpu", gpu_index, 0);
	} else {
		av_opt_set(e->ctx->priv_data, "preset", "p1", 0);
		av_opt_set(e->ctx->priv_data, "tune", "ull", 0);
		av_opt_set(e->ctx->priv_data, "profile", "main", 0);
		av_opt_set(e->ctx->priv_data, "rc", "cbr", 0);
		av_opt_set(e->ctx->priv_data, "zerolatency", "1", 0);
		av_opt_set_int(e->ctx->priv_data, "gpu", gpu_index, 0);
	}

	e->ctx->flags |= AV_CODEC_FLAG_LOW_DELAY;

	ret = avcodec_open2(e->ctx, codec, NULL);
	if (ret < 0) {
		avcodec_free_context(&e->ctx);
		av_buffer_unref(&e->hw_frames_ctx);
		av_buffer_unref(&e->hw_device_ctx);
		free(e);
		return NULL;
	}

	// Allocate a CUDA AVFrame from the hw_frames_ctx
	e->frame = av_frame_alloc();
	if (!e->frame) {
		avcodec_free_context(&e->ctx);
		av_buffer_unref(&e->hw_frames_ctx);
		av_buffer_unref(&e->hw_device_ctx);
		free(e);
		return NULL;
	}

	e->pkt = av_packet_alloc();

	return e;
}

// Encode an NV12 frame from a CUDA device pointer.
// cuda_ptr is the device pointer to the NV12 frame, stride is the row pitch.
static int cuda_encoder_encode(CUDAEncoder *e, unsigned long long cuda_ptr,
                                int stride,
                                uint8_t **out_buf, int *out_size, int *is_key) {
	*out_size = 0;

	// Get a fresh frame from the hw_frames_ctx
	av_frame_unref(e->frame);
	int ret = av_hwframe_get_buffer(e->hw_frames_ctx, e->frame, 0);
	if (ret < 0) return -1;

	// Copy NvFBC's CUDA buffer into the AVFrame's CUDA buffer.
	// Both are on the same GPU so this is a fast device-to-device copy.
	// NV12 layout: Y plane = stride * height, UV plane = stride * height/2

	size_t y_size = (size_t)stride * e->height;

	CUdeviceptr src_y = (CUdeviceptr)cuda_ptr;
	CUdeviceptr src_uv = src_y + y_size;

	CUdeviceptr dst_y = (CUdeviceptr)e->frame->data[0];
	CUdeviceptr dst_uv = (CUdeviceptr)e->frame->data[1];
	int dst_stride_y = e->frame->linesize[0];
	int dst_stride_uv = e->frame->linesize[1];

	if (!e->cuMemcpy2D_fn) {
		fprintf(stderr, "cuda_enc: cuMemcpy2D_fn not set\n");
		return -1;
	}

	typedef struct {
		size_t srcXInBytes, srcY;
		int srcMemoryType; // CU_MEMORYTYPE_DEVICE = 2
		const void *srcHost;
		CUdeviceptr srcDevice;
		void *srcArray;
		size_t srcPitch;
		size_t dstXInBytes, dstY;
		int dstMemoryType;
		void *dstHost;
		CUdeviceptr dstDevice;
		void *dstArray;
		size_t dstPitch;
		size_t WidthInBytes, Height;
	} MY_CUDA_MEMCPY2D;

	typedef CUresult (*PFN_cuMemcpy2D)(const MY_CUDA_MEMCPY2D *);
	PFN_cuMemcpy2D fn_memcpy2d = (PFN_cuMemcpy2D)e->cuMemcpy2D_fn;

	// Copy Y plane
	MY_CUDA_MEMCPY2D cp_y = {0};
	cp_y.srcMemoryType = 2;
	cp_y.srcDevice = src_y;
	cp_y.srcPitch = stride;
	cp_y.dstMemoryType = 2;
	cp_y.dstDevice = dst_y;
	cp_y.dstPitch = dst_stride_y;
	cp_y.WidthInBytes = e->width;
	cp_y.Height = e->height;
	CUresult r = fn_memcpy2d(&cp_y);
	if (r != CUDA_SUCCESS) {
		fprintf(stderr, "cuda_enc: Y plane copy failed: %d\n", r);
		return -1;
	}

	// Copy UV plane
	MY_CUDA_MEMCPY2D cp_uv = {0};
	cp_uv.srcMemoryType = 2;
	cp_uv.srcDevice = src_uv;
	cp_uv.srcPitch = stride;
	cp_uv.dstMemoryType = 2;
	cp_uv.dstDevice = dst_uv;
	cp_uv.dstPitch = dst_stride_uv;
	cp_uv.WidthInBytes = e->width;
	cp_uv.Height = e->height / 2;
	r = fn_memcpy2d(&cp_uv);
	if (r != CUDA_SUCCESS) {
		fprintf(stderr, "cuda_enc: UV plane copy failed: %d\n", r);
		return -1;
	}

	e->frame->pts = e->pts++;

	ret = avcodec_send_frame(e->ctx, e->frame);
	if (ret < 0) {
		fprintf(stderr, "cuda_enc: avcodec_send_frame failed: %d\n", ret);
		return -1;
	}

	ret = avcodec_receive_packet(e->ctx, e->pkt);
	if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) return 0;
	if (ret < 0) {
		fprintf(stderr, "cuda_enc: avcodec_receive_packet failed: %d\n", ret);
		return -1;
	}

	*out_buf = e->pkt->data;
	*out_size = e->pkt->size;
	*is_key = (e->pkt->flags & AV_PKT_FLAG_KEY) ? 1 : 0;
	return 0;
}

static void cuda_encoder_unref(CUDAEncoder *e) { av_packet_unref(e->pkt); }

static const char* cuda_encoder_name(CUDAEncoder *e) { return e->ctx->codec->name; }

static void cuda_encoder_destroy(CUDAEncoder *e) {
	if (!e) return;
	if (e->pkt) av_packet_free(&e->pkt);
	if (e->frame) av_frame_free(&e->frame);
	if (e->ctx) avcodec_free_context(&e->ctx);
	if (e->hw_frames_ctx) av_buffer_unref(&e->hw_frames_ctx);
	if (e->hw_device_ctx) av_buffer_unref(&e->hw_device_ctx);
	free(e);
}
*/
import "C"
import (
	"fmt"
	"unsafe"

	"bunghole/internal/types"
)

// cpuEncoder wraps the CPU-based encoder (sws_scale BGRA→NV12 + NVENC/libx264).
type cpuEncoder struct {
	e *C.CPUEncoder
}

// cudaEncoder wraps the CUDA-based encoder (NV12 CUDA ptr → NVENC).
type cudaEncoder struct {
	e *C.CUDAEncoder
}

func NewEncoder(width, height, fps, bitrateKbps, gpu int, codec string, gop int, cudaCtx, cuMemcpy2D unsafe.Pointer) (types.VideoEncoder, error) {
	keyint := gop
	if keyint <= 0 {
		keyint = fps * 2
	}

	cCodec := C.CString(codec)
	defer C.free(unsafe.Pointer(cCodec))

	if cudaCtx != nil {
		// CUDA path: zero-copy from NvFBC CUDA buffer to NVENC
		e := C.cuda_encoder_init(
			C.int(width), C.int(height), C.int(fps),
			C.int(bitrateKbps), C.int(keyint), C.int(gpu),
			cCodec, cudaCtx, cuMemcpy2D)
		if e != nil {
			name := C.GoString(C.cuda_encoder_name(e))
			fmt.Printf("video encoder: %s CUDA (%dx%d @ %d kbps)\n", name, width, height, bitrateKbps)
			return &cudaEncoder{e: e}, nil
		}
		fmt.Println("CUDA encoder init failed, falling back to CPU encoder")
	}

	// CPU fallback path
	e := C.cpu_encoder_init(
		C.int(width), C.int(height), C.int(fps),
		C.int(bitrateKbps), C.int(keyint), C.int(gpu), cCodec)
	if e == nil {
		if codec == "h265" {
			return nil, fmt.Errorf("failed to initialize video encoder (tried hardware h265 then libx265)")
		}
		return nil, fmt.Errorf("failed to initialize video encoder (tried hardware h264 then libx264)")
	}
	name := C.GoString(C.cpu_encoder_name(e))
	fmt.Printf("video encoder: %s (%dx%d @ %d kbps)\n", name, width, height, bitrateKbps)
	return &cpuEncoder{e: e}, nil
}

// cpuEncoder — BGRA CPU buffer path

func (enc *cpuEncoder) Encode(frame *types.Frame) (*types.EncodedFrame, error) {
	var outBuf *C.uint8_t
	var outSize C.int
	var isKey C.int

	var srcPtr unsafe.Pointer
	if frame.Ptr != nil {
		srcPtr = frame.Ptr
	} else {
		srcPtr = unsafe.Pointer(&frame.Data[0])
	}

	ret := C.cpu_encoder_encode(enc.e,
		(*C.uint8_t)(srcPtr), C.int(frame.Stride),
		&outBuf, &outSize, &isKey)

	if ret != 0 {
		return nil, fmt.Errorf("encode failed")
	}
	if outSize == 0 {
		return nil, nil
	}

	data := C.GoBytes(unsafe.Pointer(outBuf), outSize)
	C.cpu_encoder_unref(enc.e)

	return &types.EncodedFrame{
		Data:  data,
		IsKey: isKey != 0,
	}, nil
}

func (enc *cpuEncoder) Close() {
	C.cpu_encoder_destroy(enc.e)
}

// cudaEncoder — NV12 CUDA device pointer path

func (enc *cudaEncoder) Encode(frame *types.Frame) (*types.EncodedFrame, error) {
	if !frame.IsCUDA {
		return nil, fmt.Errorf("CUDA encoder received non-CUDA frame")
	}

	var outBuf *C.uint8_t
	var outSize C.int
	var isKey C.int

	// frame.Ptr is a CUdeviceptr (uint64) stored as unsafe.Pointer
	cudaPtr := C.ulonglong(uintptr(frame.Ptr))

	ret := C.cuda_encoder_encode(enc.e, cudaPtr, C.int(frame.Stride),
		&outBuf, &outSize, &isKey)

	if ret != 0 {
		return nil, fmt.Errorf("CUDA encode failed")
	}
	if outSize == 0 {
		return nil, nil
	}

	data := C.GoBytes(unsafe.Pointer(outBuf), outSize)
	C.cuda_encoder_unref(enc.e)

	return &types.EncodedFrame{
		Data:  data,
		IsKey: isKey != 0,
	}, nil
}

func (enc *cudaEncoder) Close() {
	C.cuda_encoder_destroy(enc.e)
}
