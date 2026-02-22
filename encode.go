package main

/*
#cgo pkg-config: libavcodec libavutil libswscale
#include <libavcodec/avcodec.h>
#include <libavutil/imgutils.h>
#include <libavutil/opt.h>
#include <libswscale/swscale.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	AVCodecContext *ctx;
	AVFrame *frame;
	AVPacket *pkt;
	struct SwsContext *sws;
	int width;
	int height;
	int64_t pts;
} Encoder;

static Encoder* encoder_init(int width, int height, int fps, int bitrate_kbps, int keyint, int gpu_index, const char *codec_name) {
	Encoder *e = (Encoder*)calloc(1, sizeof(Encoder));
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

	// Set up swscale for BGRA -> NV12/YUV420P conversion
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

// Returns: 0 = success, -1 = error. out_size=0 means no output yet.
static int encoder_encode(Encoder *e, const uint8_t *bgra, int stride,
                          uint8_t **out_buf, int *out_size, int *is_key) {
	*out_size = 0;

	// Convert BGRA to encoder pixel format
	const uint8_t *src_data[1] = { bgra };
	int src_linesize[1] = { stride };

	av_frame_make_writable(e->frame);
	sws_scale(e->sws, src_data, src_linesize, 0, e->height,
	          e->frame->data, e->frame->linesize);

	e->frame->pts = e->pts++;

	int ret = avcodec_send_frame(e->ctx, e->frame);
	if (ret < 0) return -1;

	ret = avcodec_receive_packet(e->ctx, e->pkt);
	if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
		return 0;
	}
	if (ret < 0) return -1;

	*out_buf = e->pkt->data;
	*out_size = e->pkt->size;
	*is_key = (e->pkt->flags & AV_PKT_FLAG_KEY) ? 1 : 0;
	return 0;
}

static void encoder_unref_packet(Encoder *e) {
	av_packet_unref(e->pkt);
}

static const char* encoder_name(Encoder *e) {
	return e->ctx->codec->name;
}

static void encoder_destroy(Encoder *e) {
	if (!e) return;
	if (e->sws) sws_freeContext(e->sws);
	if (e->pkt) av_packet_free(&e->pkt);
	if (e->frame) av_frame_free(&e->frame);
	if (e->ctx) avcodec_free_context(&e->ctx);
	free(e);
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type Encoder struct {
	e *C.Encoder
}

func NewEncoder(width, height, fps, bitrateKbps, gpu int, codec string, gop int) (*Encoder, error) {
	keyint := gop
	if keyint <= 0 {
		keyint = fps * 2 // default: keyframe every 2 seconds
	}
	cCodec := C.CString(codec)
	defer C.free(unsafe.Pointer(cCodec))
	e := C.encoder_init(C.int(width), C.int(height), C.int(fps), C.int(bitrateKbps), C.int(keyint), C.int(gpu), cCodec)
	if e == nil {
		if codec == "h265" {
			return nil, fmt.Errorf("failed to initialize video encoder (tried hevc_nvenc then libx265)")
		}
		return nil, fmt.Errorf("failed to initialize video encoder (tried h264_nvenc then libx264)")
	}
	name := C.GoString(C.encoder_name(e))
	fmt.Printf("video encoder: %s (%dx%d @ %d kbps)\n", name, width, height, bitrateKbps)
	return &Encoder{e: e}, nil
}

type EncodedFrame struct {
	Data  []byte
	IsKey bool
}

func (enc *Encoder) Encode(frame *Frame) (*EncodedFrame, error) {
	var outBuf *C.uint8_t
	var outSize C.int
	var isKey C.int

	// Use zero-copy pointer if available, otherwise fall back to Go slice
	var srcPtr unsafe.Pointer
	if frame.Ptr != nil {
		srcPtr = frame.Ptr
	} else {
		srcPtr = unsafe.Pointer(&frame.Data[0])
	}

	ret := C.encoder_encode(enc.e,
		(*C.uint8_t)(srcPtr),
		C.int(frame.Stride),
		&outBuf, &outSize, &isKey)

	if ret != 0 {
		return nil, fmt.Errorf("encode failed")
	}
	if outSize == 0 {
		return nil, nil
	}

	data := C.GoBytes(unsafe.Pointer(outBuf), outSize)
	C.encoder_unref_packet(enc.e)

	return &EncodedFrame{
		Data:  data,
		IsKey: isKey != 0,
	}, nil
}

func (enc *Encoder) Close() {
	C.encoder_destroy(enc.e)
}
