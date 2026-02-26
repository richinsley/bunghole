/*
 * BungholeAudioDriver.c
 *
 * CoreAudio HAL AudioServerPlugIn that creates virtual "Bunghole Output"
 * and "Bunghole Input" devices.  Apps playing to the output device have
 * their audio captured, Opus-encoded, and sent to the host over
 * virtio-vsock.  The input device receives Opus from the host, decodes
 * it, and presents PCM to apps.
 *
 * No TCC / Screen Recording permission required — this runs inside
 * coreaudiod as a driver, not as a user-space agent.
 *
 * Build: compiled as a MODULE library → BungholeAudio.driver bundle.
 */

#include <CoreAudio/AudioServerPlugIn.h>
#include <CoreFoundation/CoreFoundation.h>
#include <mach/mach_time.h>
#include <os/log.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>
#include <unistd.h>
#include <sys/socket.h>

/* virtio-vsock address family – may not be in SDK headers */
#ifndef AF_VSOCK
#define AF_VSOCK 40
#endif

struct sockaddr_vm {
    unsigned char   svm_len;
    unsigned char   svm_family;     /* AF_VSOCK */
    unsigned short  svm_reserved1;
    unsigned int    svm_port;
    unsigned int    svm_cid;
};

/* CID 2 = host in Apple's Virtualization.framework */
#define VSOCK_HOST_CID  2
#define VSOCK_PORT_OUT  5000
#define VSOCK_PORT_IN   5001

/* Opus encoder/decoder — linked at build time.
 * Include path comes from pkg-config which points into the opus/ subdir. */
#include <opus.h>

/* ------------------------------------------------------------------ */
/*  Compile-time constants                                            */
/* ------------------------------------------------------------------ */

#define SAMPLE_RATE         48000
#define NUM_CHANNELS        2
#define BITS_PER_CHANNEL    32
#define BYTES_PER_FRAME     (NUM_CHANNELS * (BITS_PER_CHANNEL / 8))

#define RING_CAPACITY       8192    /* frames (~170ms at 48kHz) */

/* Opus: 20ms frames = 960 samples at 48kHz */
#define OPUS_FRAME_SIZE     960
#define OPUS_MAX_PACKET     1500
#define OPUS_BITRATE        128000

/* IO nominal buffer = 512 frames */
#define IO_BUFFER_FRAMES    512

/* Clock tick period in frames (10ms) */
#define CLOCK_PERIOD_FRAMES 480

/* Volume dB range */
#define VOLUME_MIN_DB       (-96.0f)
#define VOLUME_MAX_DB       (0.0f)

/* ------------------------------------------------------------------ */
/*  Object IDs                                                        */
/* ------------------------------------------------------------------ */

enum {
    kObjectID_Plugin        = 1,
    kObjectID_OutputDevice  = 2,
    kObjectID_InputDevice   = 3,
    kObjectID_OutputStream  = 4,
    kObjectID_InputStream   = 5,
    kObjectID_OutputVolume  = 6,
    kObjectID_InputVolume   = 7,
};

/* ------------------------------------------------------------------ */
/*  Lock-free SPSC ring buffer                                        */
/* ------------------------------------------------------------------ */

typedef struct {
    float samples[RING_CAPACITY * NUM_CHANNELS];
    _Atomic uint64_t head;  /* producer write position (in frames) */
    _Atomic uint64_t tail;  /* consumer read position (in frames) */
} RingBuffer;

static void ring_init(RingBuffer *rb) {
    memset(rb->samples, 0, sizeof(rb->samples));
    atomic_store(&rb->head, 0);
    atomic_store(&rb->tail, 0);
}

static uint64_t ring_available(RingBuffer *rb) {
    uint64_t h = atomic_load_explicit(&rb->head, memory_order_acquire);
    uint64_t t = atomic_load_explicit(&rb->tail, memory_order_relaxed);
    return h - t;
}

/* Write up to `count` frames. Returns frames actually written. */
static uint64_t ring_write(RingBuffer *rb, const float *src, uint64_t count) {
    uint64_t h = atomic_load_explicit(&rb->head, memory_order_relaxed);
    uint64_t t = atomic_load_explicit(&rb->tail, memory_order_acquire);
    uint64_t free = RING_CAPACITY - (h - t);
    if (count > free) count = free;
    if (count == 0) return 0;

    uint64_t idx = h % RING_CAPACITY;
    uint64_t first = RING_CAPACITY - idx;
    if (first > count) first = count;
    memcpy(&rb->samples[idx * NUM_CHANNELS], src, first * BYTES_PER_FRAME);
    if (count > first) {
        memcpy(&rb->samples[0], src + first * NUM_CHANNELS, (count - first) * BYTES_PER_FRAME);
    }
    atomic_store_explicit(&rb->head, h + count, memory_order_release);
    return count;
}

/* Read up to `count` frames. Returns frames actually read. */
static uint64_t ring_read(RingBuffer *rb, float *dst, uint64_t count) {
    uint64_t h = atomic_load_explicit(&rb->head, memory_order_acquire);
    uint64_t t = atomic_load_explicit(&rb->tail, memory_order_relaxed);
    uint64_t avail = h - t;
    if (count > avail) count = avail;
    if (count == 0) return 0;

    uint64_t idx = t % RING_CAPACITY;
    uint64_t first = RING_CAPACITY - idx;
    if (first > count) first = count;
    memcpy(dst, &rb->samples[idx * NUM_CHANNELS], first * BYTES_PER_FRAME);
    if (count > first) {
        memcpy(dst + first * NUM_CHANNELS, &rb->samples[0], (count - first) * BYTES_PER_FRAME);
    }
    atomic_store_explicit(&rb->tail, t + count, memory_order_release);
    return count;
}

/* ------------------------------------------------------------------ */
/*  Driver state                                                      */
/* ------------------------------------------------------------------ */

typedef struct {
    /* Plugin interface — must be first field (COM convention) */
    AudioServerPlugInDriverInterface *interface;
    AudioServerPlugInHostRef          host;
    UInt32                            refCount;

    /* Ring buffers */
    RingBuffer outputRing;  /* IO writes (output device) → transport reads */
    RingBuffer inputRing;   /* transport writes → IO reads (input device) */

    /* Volume controls (atomic) */
    _Atomic float   outputVolume;       /* 0.0 – 1.0 scalar */
    _Atomic int     outputMute;
    _Atomic float   inputVolume;
    _Atomic int     inputMute;

    /* IO state */
    _Atomic int     outputIORunning;
    _Atomic int     inputIORunning;
    uint64_t        outputHostTicksAtZero;
    uint64_t        inputHostTicksAtZero;
    uint64_t        outputSampleTime;
    uint64_t        inputSampleTime;

    /* Transport threads */
    pthread_t       outputThread;
    pthread_t       inputThread;
    _Atomic int     running;

    /* Opus codecs */
    OpusEncoder    *opusEncoder;
    OpusDecoder    *opusDecoder;

    /* Mach timebase for clock calculations */
    mach_timebase_info_data_t timebase;

    os_log_t        logger;
} DriverState;

static DriverState *gDriver = NULL;

/* ------------------------------------------------------------------ */
/*  Utility: vsock connect with retry                                 */
/* ------------------------------------------------------------------ */

static int vsock_connect(unsigned int port) {
    int fd = socket(AF_VSOCK, SOCK_STREAM, 0);
    if (fd < 0) return -1;

    struct sockaddr_vm addr;
    memset(&addr, 0, sizeof(addr));
    addr.svm_len    = sizeof(addr);
    addr.svm_family = AF_VSOCK;
    addr.svm_cid    = VSOCK_HOST_CID;
    addr.svm_port   = port;

    if (connect(fd, (struct sockaddr *)&addr, sizeof(addr)) < 0) {
        close(fd);
        return -1;
    }
    return fd;
}

/* ------------------------------------------------------------------ */
/*  Utility: framed write/read (2-byte BE length prefix)              */
/* ------------------------------------------------------------------ */

static int framed_write(int fd, const unsigned char *data, uint16_t len) {
    unsigned char hdr[2];
    hdr[0] = (unsigned char)(len >> 8);
    hdr[1] = (unsigned char)(len & 0xFF);

    /* Write header */
    ssize_t n = 0;
    while (n < 2) {
        ssize_t w = write(fd, hdr + n, 2 - n);
        if (w <= 0) return -1;
        n += w;
    }
    /* Write payload */
    n = 0;
    while (n < len) {
        ssize_t w = write(fd, data + n, len - n);
        if (w <= 0) return -1;
        n += w;
    }
    return 0;
}

static int framed_read(int fd, unsigned char *buf, int bufsize, uint16_t *out_len) {
    unsigned char hdr[2];
    ssize_t n = 0;
    while (n < 2) {
        ssize_t r = read(fd, hdr + n, 2 - n);
        if (r <= 0) return -1;
        n += r;
    }
    uint16_t len = ((uint16_t)hdr[0] << 8) | hdr[1];
    if (len == 0 || len > bufsize) return -1;

    n = 0;
    while (n < len) {
        ssize_t r = read(fd, buf + n, len - n);
        if (r <= 0) return -1;
        n += r;
    }
    *out_len = len;
    return 0;
}

/* ------------------------------------------------------------------ */
/*  Utility: volume scalar ↔ dB conversion                           */
/* ------------------------------------------------------------------ */

static float scalar_to_db(float scalar) {
    if (scalar <= 0.0f) return VOLUME_MIN_DB;
    float db = 20.0f * log10f(scalar);
    if (db < VOLUME_MIN_DB) db = VOLUME_MIN_DB;
    return db;
}

static float db_to_scalar(float db) {
    if (db <= VOLUME_MIN_DB) return 0.0f;
    return powf(10.0f, db / 20.0f);
}

/* ------------------------------------------------------------------ */
/*  Transport: output thread (ring → Opus → vsock)                    */
/* ------------------------------------------------------------------ */

static void *output_transport_thread(void *arg) {
    DriverState *drv = (DriverState *)arg;
    float pcm[OPUS_FRAME_SIZE * NUM_CHANNELS];
    int16_t pcm16[OPUS_FRAME_SIZE * NUM_CHANNELS];
    unsigned char opus_buf[OPUS_MAX_PACKET];
    uint64_t acc = 0; /* accumulated frames in pcm buffer */

    os_log(drv->logger, "output transport thread started");

    while (atomic_load(&drv->running)) {
        int fd = vsock_connect(VSOCK_PORT_OUT);
        if (fd < 0) {
            sleep(1);
            continue;
        }
        os_log(drv->logger, "output vsock connected to host port %d", VSOCK_PORT_OUT);

        while (atomic_load(&drv->running)) {
            /* Drain ring buffer into accumulation buffer */
            uint64_t need = OPUS_FRAME_SIZE - acc;
            uint64_t got = ring_read(&drv->outputRing, pcm + acc * NUM_CHANNELS, need);
            acc += got;

            if (acc < OPUS_FRAME_SIZE) {
                /* Not enough data yet — sleep briefly */
                usleep(2000); /* 2ms */
                continue;
            }

            /* Apply volume */
            float vol = atomic_load_explicit(&drv->outputVolume, memory_order_relaxed);
            int mute = atomic_load_explicit(&drv->outputMute, memory_order_relaxed);
            float gain = mute ? 0.0f : vol;

            /* Convert Float32 → Int16 with volume */
            for (int i = 0; i < OPUS_FRAME_SIZE * NUM_CHANNELS; i++) {
                float s = pcm[i] * gain * 32767.0f;
                if (s > 32767.0f) s = 32767.0f;
                if (s < -32768.0f) s = -32768.0f;
                pcm16[i] = (int16_t)s;
            }

            /* Encode */
            int nbytes = opus_encode(drv->opusEncoder, pcm16, OPUS_FRAME_SIZE, opus_buf, OPUS_MAX_PACKET);
            if (nbytes < 0) {
                os_log_error(drv->logger, "opus_encode error: %d", nbytes);
                acc = 0;
                continue;
            }

            /* Send framed packet */
            if (framed_write(fd, opus_buf, (uint16_t)nbytes) < 0) {
                os_log(drv->logger, "output vsock write failed, reconnecting");
                break;
            }

            acc = 0;
        }

        close(fd);
        if (atomic_load(&drv->running)) {
            sleep(1);
        }
    }

    os_log(drv->logger, "output transport thread exiting");
    return NULL;
}

/* ------------------------------------------------------------------ */
/*  Transport: input thread (vsock → Opus → ring)                     */
/* ------------------------------------------------------------------ */

static void *input_transport_thread(void *arg) {
    DriverState *drv = (DriverState *)arg;
    unsigned char opus_buf[OPUS_MAX_PACKET];
    int16_t pcm16[OPUS_FRAME_SIZE * NUM_CHANNELS];
    float pcm[OPUS_FRAME_SIZE * NUM_CHANNELS];

    os_log(drv->logger, "input transport thread started");

    while (atomic_load(&drv->running)) {
        int fd = vsock_connect(VSOCK_PORT_IN);
        if (fd < 0) {
            sleep(1);
            continue;
        }
        os_log(drv->logger, "input vsock connected to host port %d", VSOCK_PORT_IN);

        while (atomic_load(&drv->running)) {
            uint16_t pkt_len = 0;
            if (framed_read(fd, opus_buf, OPUS_MAX_PACKET, &pkt_len) < 0) {
                os_log(drv->logger, "input vsock read failed, reconnecting");
                break;
            }

            /* Decode */
            int nsamples = opus_decode(drv->opusDecoder, opus_buf, pkt_len, pcm16, OPUS_FRAME_SIZE, 0);
            if (nsamples < 0) {
                os_log_error(drv->logger, "opus_decode error: %d", nsamples);
                continue;
            }

            /* Apply volume, convert Int16 → Float32 */
            float vol = atomic_load_explicit(&drv->inputVolume, memory_order_relaxed);
            int mute = atomic_load_explicit(&drv->inputMute, memory_order_relaxed);
            float gain = mute ? 0.0f : vol;

            for (int i = 0; i < nsamples * NUM_CHANNELS; i++) {
                pcm[i] = ((float)pcm16[i] / 32768.0f) * gain;
            }

            ring_write(&drv->inputRing, pcm, (uint64_t)nsamples);
        }

        close(fd);
        if (atomic_load(&drv->running)) {
            sleep(1);
        }
    }

    os_log(drv->logger, "input transport thread exiting");
    return NULL;
}

/* ------------------------------------------------------------------ */
/*  AudioServerPlugIn interface — forward declarations                */
/* ------------------------------------------------------------------ */

static HRESULT driver_QueryInterface(void *inDriver, REFIID inUUID, LPVOID *outInterface);
static ULONG   driver_AddRef(void *inDriver);
static ULONG   driver_Release(void *inDriver);
static OSStatus driver_Initialize(AudioServerPlugInDriverRef inDriver, AudioServerPlugInHostRef inHost);
static OSStatus driver_CreateDevice(AudioServerPlugInDriverRef d, CFDictionaryRef desc, const AudioServerPlugInClientInfo *ci, AudioObjectID *out);
static OSStatus driver_DestroyDevice(AudioServerPlugInDriverRef d, AudioObjectID id);
static OSStatus driver_AddDeviceClient(AudioServerPlugInDriverRef d, AudioObjectID id, const AudioServerPlugInClientInfo *ci);
static OSStatus driver_RemoveDeviceClient(AudioServerPlugInDriverRef d, AudioObjectID id, const AudioServerPlugInClientInfo *ci);
static OSStatus driver_PerformDeviceConfigurationChange(AudioServerPlugInDriverRef d, AudioObjectID id, UInt64 action, void *data);
static OSStatus driver_AbortDeviceConfigurationChange(AudioServerPlugInDriverRef d, AudioObjectID id, UInt64 action, void *data);
static Boolean  driver_HasProperty(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr);
static OSStatus driver_IsPropertySettable(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr, Boolean *out);
static OSStatus driver_GetPropertyDataSize(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr, UInt32 qualSize, const void *qual, UInt32 *outSize);
static OSStatus driver_GetPropertyData(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr, UInt32 qualSize, const void *qual, UInt32 inSize, UInt32 *outSize, void *outData);
static OSStatus driver_SetPropertyData(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr, UInt32 qualSize, const void *qual, UInt32 inSize, const void *inData);
static OSStatus driver_StartIO(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID);
static OSStatus driver_StopIO(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID);
static OSStatus driver_GetZeroTimeStamp(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID, Float64 *outSampleTime, UInt64 *outHostTime, UInt64 *outSeed);
static OSStatus driver_WillDoIOOperation(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID, UInt32 opID, Boolean *outWill, Boolean *outIsInput);
static OSStatus driver_BeginIOOperation(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID, UInt32 opID, UInt32 ioSize, const AudioServerPlugInIOCycleInfo *ioCycle);
static OSStatus driver_DoIOOperation(AudioServerPlugInDriverRef d, AudioObjectID id, AudioObjectID streamID, UInt32 clientID, UInt32 opID, UInt32 ioSize, const AudioServerPlugInIOCycleInfo *ioCycle, void *ioMainBuffer, void *ioSecondaryBuffer);
static OSStatus driver_EndIOOperation(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID, UInt32 opID, UInt32 ioSize, const AudioServerPlugInIOCycleInfo *ioCycle);

/* ------------------------------------------------------------------ */
/*  vtable                                                            */
/* ------------------------------------------------------------------ */

static AudioServerPlugInDriverInterface gDriverInterface = {
    /* IUnknown */
    NULL, /* _reserved */
    driver_QueryInterface,
    driver_AddRef,
    driver_Release,
    /* AudioServerPlugInDriverInterface */
    driver_Initialize,
    driver_CreateDevice,
    driver_DestroyDevice,
    driver_AddDeviceClient,
    driver_RemoveDeviceClient,
    driver_PerformDeviceConfigurationChange,
    driver_AbortDeviceConfigurationChange,
    driver_HasProperty,
    driver_IsPropertySettable,
    driver_GetPropertyDataSize,
    driver_GetPropertyData,
    driver_SetPropertyData,
    driver_StartIO,
    driver_StopIO,
    driver_GetZeroTimeStamp,
    driver_WillDoIOOperation,
    driver_BeginIOOperation,
    driver_DoIOOperation,
    driver_EndIOOperation,
};

static AudioServerPlugInDriverInterface *gDriverInterfacePtr = &gDriverInterface;

/* ------------------------------------------------------------------ */
/*  Helper: write a property value into the output buffer             */
/* ------------------------------------------------------------------ */

#define WRITE_PROPERTY(type, value) do { \
    if (inSize < sizeof(type)) return kAudioHardwareBadPropertySizeError; \
    *outSize = sizeof(type); \
    *(type *)outData = (value); \
} while (0)

/* ------------------------------------------------------------------ */
/*  Helper: create a CFString (caller must CFRelease)                 */
/* ------------------------------------------------------------------ */

static CFStringRef make_cfstr(const char *s) {
    return CFStringCreateWithCString(kCFAllocatorDefault, s, kCFStringEncodingUTF8);
}

/* ------------------------------------------------------------------ */
/*  IUnknown                                                          */
/* ------------------------------------------------------------------ */

static HRESULT driver_QueryInterface(void *inDriver, REFIID inUUID, LPVOID *outInterface) {
    CFUUIDRef req = CFUUIDCreateFromUUIDBytes(kCFAllocatorDefault, inUUID);
    CFUUIDRef iunk = CFUUIDGetConstantUUIDWithBytes(kCFAllocatorDefault,
        0x00,0x00,0x00,0x00, 0x00,0x00, 0x00,0x00,
        0xC0,0x00, 0x00,0x00,0x00,0x00,0x00,0x46); /* IUnknown */
    /* AudioServerPlugInDriverInterface UUID
       macOS ≤15: EEA5773D-CC43-49F1-8E00-8F9635872532
       macOS 26+: EEA5773D-CC43-49F1-8E00-8F96E7D23B17 */
    CFUUIDRef idrvOld = CFUUIDGetConstantUUIDWithBytes(kCFAllocatorDefault,
        0xEE,0xA5,0x77,0x3D, 0xCC,0x43, 0x49,0xF1,
        0x8E,0x00, 0x8F,0x96,0x35,0x87,0x25,0x32);
    CFUUIDRef idrvNew = CFUUIDGetConstantUUIDWithBytes(kCFAllocatorDefault,
        0xEE,0xA5,0x77,0x3D, 0xCC,0x43, 0x49,0xF1,
        0x8E,0x00, 0x8F,0x96,0xE7,0xD2,0x3B,0x17);

    if (CFEqual(req, iunk) || CFEqual(req, idrvOld) || CFEqual(req, idrvNew)) {
        driver_AddRef(inDriver);
        *outInterface = &gDriver->interface;
        CFRelease(req);
        return S_OK;
    }

    *outInterface = NULL;
    CFRelease(req);
    return E_NOINTERFACE;
}

static ULONG driver_AddRef(void *inDriver) {
    (void)inDriver;
    if (gDriver) {
        return ++gDriver->refCount;
    }
    return 1;
}

static ULONG driver_Release(void *inDriver) {
    (void)inDriver;
    if (gDriver && gDriver->refCount > 0) {
        return --gDriver->refCount;
    }
    return 0;
}

/* ------------------------------------------------------------------ */
/*  Initialize                                                        */
/* ------------------------------------------------------------------ */

static OSStatus driver_Initialize(AudioServerPlugInDriverRef inDriver, AudioServerPlugInHostRef inHost) {
    (void)inDriver;
    if (!gDriver) return kAudioHardwareUnspecifiedError;

    gDriver->host = inHost;
    gDriver->logger = os_log_create("com.bunghole.audio", "driver");
    os_log(gDriver->logger, "BungholeAudio: Initialize");

    /* Init ring buffers */
    ring_init(&gDriver->outputRing);
    ring_init(&gDriver->inputRing);

    /* Init volume defaults */
    atomic_store(&gDriver->outputVolume, 1.0f);
    atomic_store(&gDriver->outputMute, 0);
    atomic_store(&gDriver->inputVolume, 1.0f);
    atomic_store(&gDriver->inputMute, 0);

    /* IO state */
    atomic_store(&gDriver->outputIORunning, 0);
    atomic_store(&gDriver->inputIORunning, 0);
    gDriver->outputHostTicksAtZero = 0;
    gDriver->inputHostTicksAtZero = 0;
    gDriver->outputSampleTime = 0;
    gDriver->inputSampleTime = 0;

    /* Mach timebase */
    mach_timebase_info(&gDriver->timebase);

    /* Create Opus encoder */
    int err;
    gDriver->opusEncoder = opus_encoder_create(SAMPLE_RATE, NUM_CHANNELS, OPUS_APPLICATION_AUDIO, &err);
    if (err != OPUS_OK) {
        os_log_error(gDriver->logger, "opus_encoder_create failed: %d", err);
        return kAudioHardwareUnspecifiedError;
    }
    opus_encoder_ctl(gDriver->opusEncoder, OPUS_SET_BITRATE(OPUS_BITRATE));

    /* Create Opus decoder */
    gDriver->opusDecoder = opus_decoder_create(SAMPLE_RATE, NUM_CHANNELS, &err);
    if (err != OPUS_OK) {
        os_log_error(gDriver->logger, "opus_decoder_create failed: %d", err);
        opus_encoder_destroy(gDriver->opusEncoder);
        gDriver->opusEncoder = NULL;
        return kAudioHardwareUnspecifiedError;
    }

    /* Start transport threads */
    atomic_store(&gDriver->running, 1);
    pthread_create(&gDriver->outputThread, NULL, output_transport_thread, gDriver);
    pthread_create(&gDriver->inputThread, NULL, input_transport_thread, gDriver);

    os_log(gDriver->logger, "BungholeAudio: initialized successfully");

    /* Announce our devices to coreaudiod so it discovers them */
    AudioObjectPropertyAddress addr = {
        kAudioObjectPropertyOwnedObjects,
        kAudioObjectPropertyScopeGlobal,
        kAudioObjectPropertyElementMain
    };
    gDriver->host->PropertiesChanged(gDriver->host, kObjectID_Plugin, 1, &addr);
    os_log(gDriver->logger, "BungholeAudio: announced devices via PropertiesChanged");

    return kAudioHardwareNoError;
}

/* ------------------------------------------------------------------ */
/*  Device lifecycle stubs                                            */
/* ------------------------------------------------------------------ */

static OSStatus driver_CreateDevice(AudioServerPlugInDriverRef d, CFDictionaryRef desc, const AudioServerPlugInClientInfo *ci, AudioObjectID *out) {
    (void)d; (void)desc; (void)ci; (void)out;
    return kAudioHardwareUnsupportedOperationError;
}

static OSStatus driver_DestroyDevice(AudioServerPlugInDriverRef d, AudioObjectID id) {
    (void)d; (void)id;
    return kAudioHardwareUnsupportedOperationError;
}

static OSStatus driver_AddDeviceClient(AudioServerPlugInDriverRef d, AudioObjectID id, const AudioServerPlugInClientInfo *ci) {
    (void)d; (void)id; (void)ci;
    return kAudioHardwareNoError;
}

static OSStatus driver_RemoveDeviceClient(AudioServerPlugInDriverRef d, AudioObjectID id, const AudioServerPlugInClientInfo *ci) {
    (void)d; (void)id; (void)ci;
    return kAudioHardwareNoError;
}

static OSStatus driver_PerformDeviceConfigurationChange(AudioServerPlugInDriverRef d, AudioObjectID id, UInt64 action, void *data) {
    (void)d; (void)id; (void)action; (void)data;
    return kAudioHardwareNoError;
}

static OSStatus driver_AbortDeviceConfigurationChange(AudioServerPlugInDriverRef d, AudioObjectID id, UInt64 action, void *data) {
    (void)d; (void)id; (void)action; (void)data;
    return kAudioHardwareNoError;
}

/* ================================================================== */
/*  Property helpers — categorize by object type                      */
/* ================================================================== */

static Boolean is_output_device(AudioObjectID id) { return id == kObjectID_OutputDevice; }
static Boolean is_input_device(AudioObjectID id)  { return id == kObjectID_InputDevice; }
static Boolean is_device(AudioObjectID id)        { return is_output_device(id) || is_input_device(id); }
static Boolean is_stream(AudioObjectID id)        { return id == kObjectID_OutputStream || id == kObjectID_InputStream; }
static Boolean is_volume(AudioObjectID id)        { return id == kObjectID_OutputVolume || id == kObjectID_InputVolume; }

/* ------------------------------------------------------------------ */
/*  HasProperty                                                       */
/* ------------------------------------------------------------------ */

static Boolean driver_HasProperty(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr) {
    (void)d; (void)pid;

    switch (addr->mSelector) {
    /* Universal properties */
    case kAudioObjectPropertyBaseClass:
    case kAudioObjectPropertyClass:
    case kAudioObjectPropertyOwner:
    case kAudioObjectPropertyOwnedObjects:
        return true;
    default:
        break;
    }

    /* Plugin */
    if (id == kObjectID_Plugin) {
        switch (addr->mSelector) {
        case kAudioPlugInPropertyDeviceList:
        case kAudioPlugInPropertyTranslateUIDToDevice:
        case kAudioPlugInPropertyResourceBundle:
        case kAudioObjectPropertyManufacturer:
            return true;
        default:
            return false;
        }
    }

    /* Devices */
    if (is_device(id)) {
        switch (addr->mSelector) {
        case kAudioObjectPropertyName:
        case kAudioDevicePropertyDeviceUID:
        case kAudioDevicePropertyModelUID:
        case kAudioDevicePropertyTransportType:
        case kAudioDevicePropertyRelatedDevices:
        case kAudioDevicePropertyClockDomain:
        case kAudioDevicePropertyDeviceIsAlive:
        case kAudioDevicePropertyDeviceIsRunning:
        case kAudioDevicePropertyDeviceCanBeDefaultDevice:
        case kAudioDevicePropertyDeviceCanBeDefaultSystemDevice:
        case kAudioDevicePropertyLatency:
        case kAudioDevicePropertyStreams:
        case kAudioObjectPropertyControlList:
        case kAudioDevicePropertyNominalSampleRate:
        case kAudioDevicePropertyAvailableNominalSampleRates:
        case kAudioDevicePropertyZeroTimeStampPeriod:
        case kAudioDevicePropertySafetyOffset:
        case kAudioDevicePropertyPreferredChannelsForStereo:
        case kAudioDevicePropertyPreferredChannelLayout:
        case kAudioDevicePropertyIsHidden:
            return true;
        default:
            return false;
        }
    }

    /* Streams */
    if (is_stream(id)) {
        switch (addr->mSelector) {
        case kAudioStreamPropertyIsActive:
        case kAudioStreamPropertyDirection:
        case kAudioStreamPropertyTerminalType:
        case kAudioStreamPropertyStartingChannel:
        case kAudioStreamPropertyLatency:
        case kAudioStreamPropertyVirtualFormat:
        case kAudioStreamPropertyPhysicalFormat:
        case kAudioStreamPropertyAvailableVirtualFormats:
        case kAudioStreamPropertyAvailablePhysicalFormats:
            return true;
        default:
            return false;
        }
    }

    /* Volume controls */
    if (is_volume(id)) {
        switch (addr->mSelector) {
        case kAudioObjectPropertyName:
        case kAudioControlPropertyScope:
        case kAudioControlPropertyElement:
        case kAudioLevelControlPropertyScalarValue:
        case kAudioLevelControlPropertyDecibelValue:
        case kAudioLevelControlPropertyDecibelRange:
        case kAudioLevelControlPropertyConvertScalarToDecibels:
        case kAudioLevelControlPropertyConvertDecibelsToScalar:
            return true;
        default:
            return false;
        }
    }

    return false;
}

/* ------------------------------------------------------------------ */
/*  IsPropertySettable                                                */
/* ------------------------------------------------------------------ */

static OSStatus driver_IsPropertySettable(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr, Boolean *out) {
    (void)d; (void)pid;
    *out = false;

    if (is_volume(id)) {
        switch (addr->mSelector) {
        case kAudioLevelControlPropertyScalarValue:
        case kAudioLevelControlPropertyDecibelValue:
            *out = true;
            break;
        default:
            break;
        }
    }

    return kAudioHardwareNoError;
}

/* ------------------------------------------------------------------ */
/*  GetPropertyDataSize                                               */
/* ------------------------------------------------------------------ */

static OSStatus driver_GetPropertyDataSize(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr, UInt32 qualSize, const void *qual, UInt32 *outSize) {
    (void)d; (void)pid; (void)qualSize; (void)qual;

    /* Plugin */
    if (id == kObjectID_Plugin) {
        switch (addr->mSelector) {
        case kAudioObjectPropertyBaseClass:
        case kAudioObjectPropertyClass:
            *outSize = sizeof(AudioClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwner:
            *outSize = sizeof(AudioObjectID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyManufacturer:
        case kAudioPlugInPropertyResourceBundle:
            *outSize = sizeof(CFStringRef);
            return kAudioHardwareNoError;
        case kAudioPlugInPropertyDeviceList:
        case kAudioObjectPropertyOwnedObjects:
            *outSize = 2 * sizeof(AudioObjectID); /* output + input device */
            return kAudioHardwareNoError;
        case kAudioPlugInPropertyTranslateUIDToDevice:
            *outSize = sizeof(AudioObjectID);
            return kAudioHardwareNoError;
        default:
            break;
        }
    }

    /* Devices */
    if (is_device(id)) {
        switch (addr->mSelector) {
        case kAudioObjectPropertyBaseClass:
        case kAudioObjectPropertyClass:
            *outSize = sizeof(AudioClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwner:
            *outSize = sizeof(AudioObjectID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyName:
        case kAudioDevicePropertyDeviceUID:
        case kAudioDevicePropertyModelUID:
            *outSize = sizeof(CFStringRef);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyTransportType:
            *outSize = sizeof(UInt32);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyRelatedDevices:
            *outSize = sizeof(AudioObjectID);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyClockDomain:
        case kAudioDevicePropertyDeviceIsAlive:
        case kAudioDevicePropertyDeviceIsRunning:
        case kAudioDevicePropertyDeviceCanBeDefaultDevice:
        case kAudioDevicePropertyDeviceCanBeDefaultSystemDevice:
        case kAudioDevicePropertyLatency:
        case kAudioDevicePropertySafetyOffset:
            *outSize = sizeof(UInt32);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyStreams:
            *outSize = sizeof(AudioObjectID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyControlList:
            *outSize = sizeof(AudioObjectID); /* 1 volume control */
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwnedObjects:
            *outSize = 2 * sizeof(AudioObjectID); /* stream + volume */
            return kAudioHardwareNoError;
        case kAudioDevicePropertyNominalSampleRate:
            *outSize = sizeof(Float64);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyAvailableNominalSampleRates:
            *outSize = sizeof(AudioValueRange);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyZeroTimeStampPeriod:
            *outSize = sizeof(UInt32);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyPreferredChannelsForStereo:
            *outSize = 2 * sizeof(UInt32);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyPreferredChannelLayout:
            *outSize = (UInt32)(offsetof(AudioChannelLayout, mChannelDescriptions) + NUM_CHANNELS * sizeof(AudioChannelDescription));
            return kAudioHardwareNoError;
        case kAudioDevicePropertyIsHidden:
            *outSize = sizeof(UInt32);
            return kAudioHardwareNoError;
        default:
            break;
        }
    }

    /* Streams */
    if (is_stream(id)) {
        switch (addr->mSelector) {
        case kAudioObjectPropertyBaseClass:
        case kAudioObjectPropertyClass:
            *outSize = sizeof(AudioClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwner:
            *outSize = sizeof(AudioObjectID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwnedObjects:
            *outSize = 0;
            return kAudioHardwareNoError;
        case kAudioStreamPropertyIsActive:
        case kAudioStreamPropertyDirection:
        case kAudioStreamPropertyTerminalType:
        case kAudioStreamPropertyStartingChannel:
        case kAudioStreamPropertyLatency:
            *outSize = sizeof(UInt32);
            return kAudioHardwareNoError;
        case kAudioStreamPropertyVirtualFormat:
        case kAudioStreamPropertyPhysicalFormat:
            *outSize = sizeof(AudioStreamBasicDescription);
            return kAudioHardwareNoError;
        case kAudioStreamPropertyAvailableVirtualFormats:
        case kAudioStreamPropertyAvailablePhysicalFormats:
            *outSize = sizeof(AudioStreamRangedDescription);
            return kAudioHardwareNoError;
        default:
            break;
        }
    }

    /* Volume controls */
    if (is_volume(id)) {
        switch (addr->mSelector) {
        case kAudioObjectPropertyBaseClass:
        case kAudioObjectPropertyClass:
            *outSize = sizeof(AudioClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwner:
            *outSize = sizeof(AudioObjectID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwnedObjects:
            *outSize = 0;
            return kAudioHardwareNoError;
        case kAudioObjectPropertyName:
            *outSize = sizeof(CFStringRef);
            return kAudioHardwareNoError;
        case kAudioControlPropertyScope:
        case kAudioControlPropertyElement:
            *outSize = sizeof(UInt32);
            return kAudioHardwareNoError;
        case kAudioLevelControlPropertyScalarValue:
        case kAudioLevelControlPropertyDecibelValue:
            *outSize = sizeof(Float32);
            return kAudioHardwareNoError;
        case kAudioLevelControlPropertyDecibelRange:
            *outSize = sizeof(AudioValueRange);
            return kAudioHardwareNoError;
        case kAudioLevelControlPropertyConvertScalarToDecibels:
        case kAudioLevelControlPropertyConvertDecibelsToScalar:
            *outSize = sizeof(Float32);
            return kAudioHardwareNoError;
        default:
            break;
        }
    }

    return kAudioHardwareUnknownPropertyError;
}

/* ------------------------------------------------------------------ */
/*  GetPropertyData                                                   */
/* ------------------------------------------------------------------ */

static AudioStreamBasicDescription make_asbd(void) {
    AudioStreamBasicDescription asbd;
    memset(&asbd, 0, sizeof(asbd));
    asbd.mSampleRate       = (Float64)SAMPLE_RATE;
    asbd.mFormatID         = kAudioFormatLinearPCM;
    asbd.mFormatFlags      = kAudioFormatFlagIsFloat | kAudioFormatFlagIsPacked;
    asbd.mBytesPerPacket   = BYTES_PER_FRAME;
    asbd.mFramesPerPacket  = 1;
    asbd.mBytesPerFrame    = BYTES_PER_FRAME;
    asbd.mChannelsPerFrame = NUM_CHANNELS;
    asbd.mBitsPerChannel   = BITS_PER_CHANNEL;
    return asbd;
}

static OSStatus driver_GetPropertyData(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr, UInt32 qualSize, const void *qual, UInt32 inSize, UInt32 *outSize, void *outData) {
    (void)d; (void)pid;

    /* ---- Plugin ---- */
    if (id == kObjectID_Plugin) {
        switch (addr->mSelector) {
        case kAudioObjectPropertyBaseClass:
            WRITE_PROPERTY(AudioClassID, kAudioObjectClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyClass:
            WRITE_PROPERTY(AudioClassID, kAudioPlugInClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwner:
            WRITE_PROPERTY(AudioObjectID, kAudioObjectUnknown);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyManufacturer: {
            CFStringRef s = make_cfstr("Bunghole");
            WRITE_PROPERTY(CFStringRef, s);
            return kAudioHardwareNoError;
        }
        case kAudioPlugInPropertyResourceBundle: {
            CFStringRef s = make_cfstr("");
            WRITE_PROPERTY(CFStringRef, s);
            return kAudioHardwareNoError;
        }
        case kAudioObjectPropertyOwnedObjects:
        case kAudioPlugInPropertyDeviceList: {
            UInt32 need = 2 * sizeof(AudioObjectID);
            if (inSize < need) return kAudioHardwareBadPropertySizeError;
            *outSize = need;
            AudioObjectID *ids = (AudioObjectID *)outData;
            ids[0] = kObjectID_OutputDevice;
            ids[1] = kObjectID_InputDevice;
            return kAudioHardwareNoError;
        }
        case kAudioPlugInPropertyTranslateUIDToDevice: {
            if (qualSize < sizeof(CFStringRef)) return kAudioHardwareBadPropertySizeError;
            CFStringRef uid = *(const CFStringRef *)qual;
            AudioObjectID devId = kAudioObjectUnknown;
            if (uid) {
                CFStringRef outUID = make_cfstr("BungholeOutput_UID");
                CFStringRef inUID  = make_cfstr("BungholeInput_UID");
                if (CFStringCompare(uid, outUID, 0) == kCFCompareEqualTo) {
                    devId = kObjectID_OutputDevice;
                } else if (CFStringCompare(uid, inUID, 0) == kCFCompareEqualTo) {
                    devId = kObjectID_InputDevice;
                }
                CFRelease(outUID);
                CFRelease(inUID);
            }
            WRITE_PROPERTY(AudioObjectID, devId);
            return kAudioHardwareNoError;
        }
        default:
            break;
        }
    }

    /* ---- Devices ---- */
    if (is_device(id)) {
        Boolean isOutput = is_output_device(id);

        switch (addr->mSelector) {
        case kAudioObjectPropertyBaseClass:
            WRITE_PROPERTY(AudioClassID, kAudioObjectClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyClass:
            WRITE_PROPERTY(AudioClassID, kAudioDeviceClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwner:
            WRITE_PROPERTY(AudioObjectID, kObjectID_Plugin);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyName: {
            CFStringRef s = make_cfstr(isOutput ? "Bunghole Output" : "Bunghole Input");
            WRITE_PROPERTY(CFStringRef, s);
            return kAudioHardwareNoError;
        }
        case kAudioDevicePropertyDeviceUID: {
            CFStringRef s = make_cfstr(isOutput ? "BungholeOutput_UID" : "BungholeInput_UID");
            WRITE_PROPERTY(CFStringRef, s);
            return kAudioHardwareNoError;
        }
        case kAudioDevicePropertyModelUID: {
            CFStringRef s = make_cfstr("BungholeAudio_ModelUID");
            WRITE_PROPERTY(CFStringRef, s);
            return kAudioHardwareNoError;
        }
        case kAudioDevicePropertyTransportType:
            WRITE_PROPERTY(UInt32, kAudioDeviceTransportTypeVirtual);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyRelatedDevices:
            WRITE_PROPERTY(AudioObjectID, id);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyClockDomain:
            WRITE_PROPERTY(UInt32, 0);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyDeviceIsAlive:
            WRITE_PROPERTY(UInt32, 1);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyDeviceIsRunning:
            WRITE_PROPERTY(UInt32, isOutput
                ? (UInt32)atomic_load(&gDriver->outputIORunning)
                : (UInt32)atomic_load(&gDriver->inputIORunning));
            return kAudioHardwareNoError;
        case kAudioDevicePropertyDeviceCanBeDefaultDevice:
            WRITE_PROPERTY(UInt32, 1);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyDeviceCanBeDefaultSystemDevice:
            WRITE_PROPERTY(UInt32, 1);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyLatency:
            WRITE_PROPERTY(UInt32, 0);
            return kAudioHardwareNoError;
        case kAudioDevicePropertySafetyOffset:
            WRITE_PROPERTY(UInt32, 0);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyStreams: {
            AudioObjectID streamId = isOutput ? kObjectID_OutputStream : kObjectID_InputStream;
            WRITE_PROPERTY(AudioObjectID, streamId);
            return kAudioHardwareNoError;
        }
        case kAudioObjectPropertyControlList: {
            AudioObjectID volId = isOutput ? kObjectID_OutputVolume : kObjectID_InputVolume;
            WRITE_PROPERTY(AudioObjectID, volId);
            return kAudioHardwareNoError;
        }
        case kAudioObjectPropertyOwnedObjects: {
            UInt32 need = 2 * sizeof(AudioObjectID);
            if (inSize < need) return kAudioHardwareBadPropertySizeError;
            *outSize = need;
            AudioObjectID *ids = (AudioObjectID *)outData;
            ids[0] = isOutput ? kObjectID_OutputStream : kObjectID_InputStream;
            ids[1] = isOutput ? kObjectID_OutputVolume : kObjectID_InputVolume;
            return kAudioHardwareNoError;
        }
        case kAudioDevicePropertyNominalSampleRate:
            WRITE_PROPERTY(Float64, (Float64)SAMPLE_RATE);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyAvailableNominalSampleRates: {
            if (inSize < sizeof(AudioValueRange)) return kAudioHardwareBadPropertySizeError;
            *outSize = sizeof(AudioValueRange);
            AudioValueRange *r = (AudioValueRange *)outData;
            r->mMinimum = (Float64)SAMPLE_RATE;
            r->mMaximum = (Float64)SAMPLE_RATE;
            return kAudioHardwareNoError;
        }
        case kAudioDevicePropertyZeroTimeStampPeriod:
            WRITE_PROPERTY(UInt32, CLOCK_PERIOD_FRAMES);
            return kAudioHardwareNoError;
        case kAudioDevicePropertyPreferredChannelsForStereo: {
            if (inSize < 2 * sizeof(UInt32)) return kAudioHardwareBadPropertySizeError;
            *outSize = 2 * sizeof(UInt32);
            UInt32 *ch = (UInt32 *)outData;
            ch[0] = 1;
            ch[1] = 2;
            return kAudioHardwareNoError;
        }
        case kAudioDevicePropertyPreferredChannelLayout: {
            UInt32 need = (UInt32)(offsetof(AudioChannelLayout, mChannelDescriptions) + NUM_CHANNELS * sizeof(AudioChannelDescription));
            if (inSize < need) return kAudioHardwareBadPropertySizeError;
            *outSize = need;
            AudioChannelLayout *layout = (AudioChannelLayout *)outData;
            layout->mChannelLayoutTag = kAudioChannelLayoutTag_UseChannelDescriptions;
            layout->mChannelBitmap = 0;
            layout->mNumberChannelDescriptions = NUM_CHANNELS;
            layout->mChannelDescriptions[0].mChannelLabel = kAudioChannelLabel_Left;
            layout->mChannelDescriptions[0].mChannelFlags = 0;
            layout->mChannelDescriptions[0].mCoordinates[0] = 0;
            layout->mChannelDescriptions[0].mCoordinates[1] = 0;
            layout->mChannelDescriptions[0].mCoordinates[2] = 0;
            layout->mChannelDescriptions[1].mChannelLabel = kAudioChannelLabel_Right;
            layout->mChannelDescriptions[1].mChannelFlags = 0;
            layout->mChannelDescriptions[1].mCoordinates[0] = 0;
            layout->mChannelDescriptions[1].mCoordinates[1] = 0;
            layout->mChannelDescriptions[1].mCoordinates[2] = 0;
            return kAudioHardwareNoError;
        }
        case kAudioDevicePropertyIsHidden:
            WRITE_PROPERTY(UInt32, 0);
            return kAudioHardwareNoError;
        default:
            break;
        }
    }

    /* ---- Streams ---- */
    if (is_stream(id)) {
        Boolean isOutputStream = (id == kObjectID_OutputStream);

        switch (addr->mSelector) {
        case kAudioObjectPropertyBaseClass:
            WRITE_PROPERTY(AudioClassID, kAudioObjectClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyClass:
            WRITE_PROPERTY(AudioClassID, kAudioStreamClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwner:
            WRITE_PROPERTY(AudioObjectID, isOutputStream ? kObjectID_OutputDevice : kObjectID_InputDevice);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwnedObjects:
            *outSize = 0;
            return kAudioHardwareNoError;
        case kAudioStreamPropertyIsActive:
            WRITE_PROPERTY(UInt32, 1);
            return kAudioHardwareNoError;
        case kAudioStreamPropertyDirection:
            /* 0 = output, 1 = input */
            WRITE_PROPERTY(UInt32, isOutputStream ? 0 : 1);
            return kAudioHardwareNoError;
        case kAudioStreamPropertyTerminalType:
            WRITE_PROPERTY(UInt32, isOutputStream
                ? kAudioStreamTerminalTypeLine
                : kAudioStreamTerminalTypeMicrophone);
            return kAudioHardwareNoError;
        case kAudioStreamPropertyStartingChannel:
            WRITE_PROPERTY(UInt32, 1);
            return kAudioHardwareNoError;
        case kAudioStreamPropertyLatency:
            WRITE_PROPERTY(UInt32, 0);
            return kAudioHardwareNoError;
        case kAudioStreamPropertyVirtualFormat:
        case kAudioStreamPropertyPhysicalFormat: {
            if (inSize < sizeof(AudioStreamBasicDescription)) return kAudioHardwareBadPropertySizeError;
            *outSize = sizeof(AudioStreamBasicDescription);
            *(AudioStreamBasicDescription *)outData = make_asbd();
            return kAudioHardwareNoError;
        }
        case kAudioStreamPropertyAvailableVirtualFormats:
        case kAudioStreamPropertyAvailablePhysicalFormats: {
            if (inSize < sizeof(AudioStreamRangedDescription)) return kAudioHardwareBadPropertySizeError;
            *outSize = sizeof(AudioStreamRangedDescription);
            AudioStreamRangedDescription *rd = (AudioStreamRangedDescription *)outData;
            rd->mFormat = make_asbd();
            rd->mSampleRateRange.mMinimum = (Float64)SAMPLE_RATE;
            rd->mSampleRateRange.mMaximum = (Float64)SAMPLE_RATE;
            return kAudioHardwareNoError;
        }
        default:
            break;
        }
    }

    /* ---- Volume Controls ---- */
    if (is_volume(id)) {
        Boolean isOutputVol = (id == kObjectID_OutputVolume);
        _Atomic float *volPtr = isOutputVol ? &gDriver->outputVolume : &gDriver->inputVolume;

        switch (addr->mSelector) {
        case kAudioObjectPropertyBaseClass:
            WRITE_PROPERTY(AudioClassID, kAudioObjectClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyClass:
            WRITE_PROPERTY(AudioClassID, kAudioLevelControlClassID);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwner:
            WRITE_PROPERTY(AudioObjectID, isOutputVol ? kObjectID_OutputDevice : kObjectID_InputDevice);
            return kAudioHardwareNoError;
        case kAudioObjectPropertyOwnedObjects:
            *outSize = 0;
            return kAudioHardwareNoError;
        case kAudioObjectPropertyName: {
            CFStringRef s = make_cfstr(isOutputVol ? "Output Volume" : "Input Volume");
            WRITE_PROPERTY(CFStringRef, s);
            return kAudioHardwareNoError;
        }
        case kAudioControlPropertyScope:
            WRITE_PROPERTY(UInt32, isOutputVol ? kAudioObjectPropertyScopeOutput : kAudioObjectPropertyScopeInput);
            return kAudioHardwareNoError;
        case kAudioControlPropertyElement:
            WRITE_PROPERTY(UInt32, kAudioObjectPropertyElementMain);
            return kAudioHardwareNoError;
        case kAudioLevelControlPropertyScalarValue: {
            float v = atomic_load_explicit(volPtr, memory_order_relaxed);
            WRITE_PROPERTY(Float32, v);
            return kAudioHardwareNoError;
        }
        case kAudioLevelControlPropertyDecibelValue: {
            float v = atomic_load_explicit(volPtr, memory_order_relaxed);
            WRITE_PROPERTY(Float32, scalar_to_db(v));
            return kAudioHardwareNoError;
        }
        case kAudioLevelControlPropertyDecibelRange: {
            if (inSize < sizeof(AudioValueRange)) return kAudioHardwareBadPropertySizeError;
            *outSize = sizeof(AudioValueRange);
            AudioValueRange *r = (AudioValueRange *)outData;
            r->mMinimum = VOLUME_MIN_DB;
            r->mMaximum = VOLUME_MAX_DB;
            return kAudioHardwareNoError;
        }
        case kAudioLevelControlPropertyConvertScalarToDecibels: {
            if (inSize < sizeof(Float32)) return kAudioHardwareBadPropertySizeError;
            *outSize = sizeof(Float32);
            Float32 scalar = *(Float32 *)outData;
            *(Float32 *)outData = scalar_to_db(scalar);
            return kAudioHardwareNoError;
        }
        case kAudioLevelControlPropertyConvertDecibelsToScalar: {
            if (inSize < sizeof(Float32)) return kAudioHardwareBadPropertySizeError;
            *outSize = sizeof(Float32);
            Float32 db = *(Float32 *)outData;
            *(Float32 *)outData = db_to_scalar(db);
            return kAudioHardwareNoError;
        }
        default:
            break;
        }
    }

    return kAudioHardwareUnknownPropertyError;
}

/* ------------------------------------------------------------------ */
/*  SetPropertyData                                                   */
/* ------------------------------------------------------------------ */

static OSStatus driver_SetPropertyData(AudioServerPlugInDriverRef d, AudioObjectID id, pid_t pid, const AudioObjectPropertyAddress *addr, UInt32 qualSize, const void *qual, UInt32 inSize, const void *inData) {
    (void)d; (void)pid; (void)qualSize; (void)qual;

    if (is_volume(id)) {
        Boolean isOutputVol = (id == kObjectID_OutputVolume);
        _Atomic float *volPtr = isOutputVol ? &gDriver->outputVolume : &gDriver->inputVolume;

        switch (addr->mSelector) {
        case kAudioLevelControlPropertyScalarValue: {
            if (inSize < sizeof(Float32)) return kAudioHardwareBadPropertySizeError;
            Float32 v = *(const Float32 *)inData;
            if (v < 0.0f) v = 0.0f;
            if (v > 1.0f) v = 1.0f;
            atomic_store_explicit(volPtr, v, memory_order_relaxed);
            return kAudioHardwareNoError;
        }
        case kAudioLevelControlPropertyDecibelValue: {
            if (inSize < sizeof(Float32)) return kAudioHardwareBadPropertySizeError;
            Float32 db = *(const Float32 *)inData;
            if (db < VOLUME_MIN_DB) db = VOLUME_MIN_DB;
            if (db > VOLUME_MAX_DB) db = VOLUME_MAX_DB;
            atomic_store_explicit(volPtr, db_to_scalar(db), memory_order_relaxed);
            return kAudioHardwareNoError;
        }
        default:
            break;
        }
    }

    return kAudioHardwareUnsupportedOperationError;
}

/* ------------------------------------------------------------------ */
/*  IO Operations                                                     */
/* ------------------------------------------------------------------ */

static OSStatus driver_StartIO(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID) {
    (void)d; (void)clientID;
    if (!gDriver) return kAudioHardwareUnspecifiedError;

    if (is_output_device(id)) {
        gDriver->outputHostTicksAtZero = mach_absolute_time();
        gDriver->outputSampleTime = 0;
        atomic_store(&gDriver->outputIORunning, 1);
        os_log(gDriver->logger, "output device StartIO");
    } else if (is_input_device(id)) {
        gDriver->inputHostTicksAtZero = mach_absolute_time();
        gDriver->inputSampleTime = 0;
        atomic_store(&gDriver->inputIORunning, 1);
        os_log(gDriver->logger, "input device StartIO");
    }

    return kAudioHardwareNoError;
}

static OSStatus driver_StopIO(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID) {
    (void)d; (void)clientID;
    if (!gDriver) return kAudioHardwareUnspecifiedError;

    if (is_output_device(id)) {
        atomic_store(&gDriver->outputIORunning, 0);
        os_log(gDriver->logger, "output device StopIO");
    } else if (is_input_device(id)) {
        atomic_store(&gDriver->inputIORunning, 0);
        os_log(gDriver->logger, "input device StopIO");
    }

    return kAudioHardwareNoError;
}

static OSStatus driver_GetZeroTimeStamp(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID, Float64 *outSampleTime, UInt64 *outHostTime, UInt64 *outSeed) {
    (void)d; (void)clientID;
    if (!gDriver) return kAudioHardwareUnspecifiedError;

    uint64_t ticksAtZero;
    if (is_output_device(id)) {
        ticksAtZero = gDriver->outputHostTicksAtZero;
    } else {
        ticksAtZero = gDriver->inputHostTicksAtZero;
    }

    /* Current time in host ticks */
    uint64_t now = mach_absolute_time();
    uint64_t elapsed = now - ticksAtZero;

    /* Convert ticks to nanoseconds */
    uint64_t elapsed_ns = elapsed * gDriver->timebase.numer / gDriver->timebase.denom;

    /* How many full clock periods have elapsed */
    uint64_t ns_per_period = (uint64_t)CLOCK_PERIOD_FRAMES * 1000000000ULL / SAMPLE_RATE;
    uint64_t periods = elapsed_ns / ns_per_period;

    /* The zero timestamp for the most recent period boundary */
    *outSampleTime = (Float64)(periods * CLOCK_PERIOD_FRAMES);
    *outHostTime = ticksAtZero + (periods * ns_per_period * gDriver->timebase.denom / gDriver->timebase.numer);
    *outSeed = 1;

    return kAudioHardwareNoError;
}

static OSStatus driver_WillDoIOOperation(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID, UInt32 opID, Boolean *outWill, Boolean *outIsInput) {
    (void)d; (void)clientID;
    *outWill = false;
    *outIsInput = false;

    if (is_output_device(id)) {
        if (opID == kAudioServerPlugInIOOperationWriteMix) {
            *outWill = true;
            *outIsInput = false;
        }
    } else if (is_input_device(id)) {
        if (opID == kAudioServerPlugInIOOperationReadInput) {
            *outWill = true;
            *outIsInput = true;
        }
    }

    return kAudioHardwareNoError;
}

static OSStatus driver_BeginIOOperation(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID, UInt32 opID, UInt32 ioSize, const AudioServerPlugInIOCycleInfo *ioCycle) {
    (void)d; (void)id; (void)clientID; (void)opID; (void)ioSize; (void)ioCycle;
    return kAudioHardwareNoError;
}

static OSStatus driver_DoIOOperation(AudioServerPlugInDriverRef d, AudioObjectID id, AudioObjectID streamID, UInt32 clientID, UInt32 opID, UInt32 ioSize, const AudioServerPlugInIOCycleInfo *ioCycle, void *ioMainBuffer, void *ioSecondaryBuffer) {
    (void)d; (void)streamID; (void)clientID; (void)ioCycle; (void)ioSecondaryBuffer;
    if (!gDriver) return kAudioHardwareUnspecifiedError;

    if (is_output_device(id) && opID == kAudioServerPlugInIOOperationWriteMix) {
        /* Apps have mixed audio into ioMainBuffer — copy to output ring */
        float *buf = (float *)ioMainBuffer;
        ring_write(&gDriver->outputRing, buf, ioSize);
    } else if (is_input_device(id) && opID == kAudioServerPlugInIOOperationReadInput) {
        /* Read from input ring into ioMainBuffer; pad with silence on underflow */
        float *buf = (float *)ioMainBuffer;
        uint64_t got = ring_read(&gDriver->inputRing, buf, ioSize);
        if (got < ioSize) {
            memset(buf + got * NUM_CHANNELS, 0, (ioSize - got) * BYTES_PER_FRAME);
        }
    }

    return kAudioHardwareNoError;
}

static OSStatus driver_EndIOOperation(AudioServerPlugInDriverRef d, AudioObjectID id, UInt32 clientID, UInt32 opID, UInt32 ioSize, const AudioServerPlugInIOCycleInfo *ioCycle) {
    (void)d; (void)id; (void)clientID; (void)opID; (void)ioSize; (void)ioCycle;
    return kAudioHardwareNoError;
}

/* ------------------------------------------------------------------ */
/*  Factory function — entry point for AudioServerPlugIn               */
/* ------------------------------------------------------------------ */

void *BungholeAudio_Create(CFAllocatorRef allocator, CFUUIDRef typeUUID) {
    (void)allocator;

    /* Verify the requested type is AudioServerPlugInDriverInterface.
       macOS ≤15: 443ABEB8-E7B0-48D3-B2A0-381E2D0BB556
       macOS 26+: 443ABAB8-E7B3-491A-B985-BEB9187030DB */
    CFUUIDRef typeOld = CFUUIDGetConstantUUIDWithBytes(kCFAllocatorDefault,
        0x44,0x3A,0xBE,0xB8, 0xE7,0xB0, 0x48,0xD3,
        0xB2,0xA0, 0x38,0x1E,0x2D,0x0B,0xB5,0x56);
    CFUUIDRef typeNew = CFUUIDGetConstantUUIDWithBytes(kCFAllocatorDefault,
        0x44,0x3A,0xBA,0xB8, 0xE7,0xB3, 0x49,0x1A,
        0xB9,0x85, 0xBE,0xB9,0x18,0x70,0x30,0xDB);

    if (!CFEqual(typeUUID, typeOld) && !CFEqual(typeUUID, typeNew)) {
        return NULL;
    }

    /* Allocate driver state */
    gDriver = (DriverState *)calloc(1, sizeof(DriverState));
    if (!gDriver) return NULL;

    gDriver->interface = &gDriverInterface;
    gDriver->refCount = 1;

    return &gDriver->interface;
}
