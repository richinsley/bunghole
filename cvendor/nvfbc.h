/*
 * Minimal NvFBC type definitions for dynamic loading.
 * Based on the NVIDIA Capture SDK NvFBC 1.7 API (Linux).
 * Struct layouts match LizardByte/Sunshine's vendored NvFBC.h.
 * The library is loaded at runtime via dlopen("libnvidia-fbc.so.1").
 */
#ifndef NVFBC_H
#define NVFBC_H

#include <stdint.h>
#include "cuda_defs.h"

#define NVFBC_VERSION_MAJOR 1
#define NVFBC_VERSION_MINOR 7
#define NVFBC_VERSION (uint32_t)(NVFBC_VERSION_MINOR | (NVFBC_VERSION_MAJOR << 8))

#define NVFBC_STRUCT_VERSION(typeName, ver) \
    (uint32_t)(sizeof(typeName) | ((ver) << 16) | (NVFBC_VERSION << 24))

typedef uint64_t NVFBC_SESSION_HANDLE;
typedef uint32_t NVFBC_BOOL;
#define NVFBC_TRUE  1
#define NVFBC_FALSE 0

typedef enum {
    NVFBC_SUCCESS            = 0,
    NVFBC_ERR_API_VERSION    = 1,
    NVFBC_ERR_INTERNAL       = 2,
    NVFBC_ERR_INVALID_PARAM  = 3,
    NVFBC_ERR_INVALID_PTR    = 4,
    NVFBC_ERR_INVALID_HANDLE = 5,
    NVFBC_ERR_MAX_CLIENTS    = 6,
    NVFBC_ERR_UNSUPPORTED    = 7,
    NVFBC_ERR_OUT_OF_MEMORY  = 8,
    NVFBC_ERR_BAD_REQUEST    = 9,
    NVFBC_ERR_X              = 10,
    NVFBC_ERR_GL             = 11,
    NVFBC_ERR_CUDA           = 12,
} NVFBCSTATUS;

typedef enum {
    NVFBC_CAPTURE_TO_SYS      = 0,
    NVFBC_CAPTURE_SHARED_CUDA = 1,
    NVFBC_CAPTURE_TO_GL       = 2,
} NVFBC_CAPTURE_TYPE;

typedef enum {
    NVFBC_TRACKING_DEFAULT = 0,
    NVFBC_TRACKING_OUTPUT  = 1,
    NVFBC_TRACKING_SCREEN  = 2,
} NVFBC_TRACKING_TYPE;

typedef enum {
    NVFBC_BUFFER_FORMAT_BGRA    = 0,
    NVFBC_BUFFER_FORMAT_RGB     = 1,
    NVFBC_BUFFER_FORMAT_NV12    = 2,
    NVFBC_BUFFER_FORMAT_YUV444P = 3,
    NVFBC_BUFFER_FORMAT_ARGB    = 4,
} NVFBC_BUFFER_FORMAT;

#define NVFBC_TOCUDA_GRAB_FLAGS_NOFLAGS       0
#define NVFBC_TOCUDA_GRAB_FLAGS_NOWAIT        (1 << 0)
#define NVFBC_TOCUDA_GRAB_FLAGS_FORCE_REFRESH (1 << 2)

typedef struct { uint32_t w, h; } NVFBC_SIZE;
typedef struct { uint32_t x, y, w, h; } NVFBC_BOX;

/*
 * Frame grab info returned by NvFBC.
 */
typedef struct {
    uint32_t   dwWidth;
    uint32_t   dwHeight;
    uint32_t   dwByteSize;
    uint32_t   dwCurrentFrame;
    NVFBC_BOOL bIsNewFrame;
    uint64_t   ulTimestampUs;
    uint32_t   dwMissedFrames;
    NVFBC_BOOL bRequiredPostProcessing;
    NVFBC_BOOL bDirectCapture;
} NVFBC_FRAME_GRAB_INFO;

/*
 * Create handle parameters (version 2).
 * NvFBC creates its own CUDA context for TOCUDA capture.
 */
typedef struct {
    uint32_t   dwVersion;
    const void *privateData;
    uint32_t   privateDataSize;
    NVFBC_BOOL bExternallyManagedContext;
    void       *glxCtx;
    void       *glxFBConfig;
} NVFBC_CREATE_HANDLE_PARAMS;
#define NVFBC_CREATE_HANDLE_PARAMS_VER NVFBC_STRUCT_VERSION(NVFBC_CREATE_HANDLE_PARAMS, 2)

/*
 * Destroy handle parameters.
 */
typedef struct {
    uint32_t dwVersion;
} NVFBC_DESTROY_HANDLE_PARAMS;
#define NVFBC_DESTROY_HANDLE_PARAMS_VER NVFBC_STRUCT_VERSION(NVFBC_DESTROY_HANDLE_PARAMS, 1)

/*
 * Get status parameters (version 2).
 * Simplified — we only read the first few fields. Over-allocate with pad
 * to ensure the struct is large enough for the library to write all fields
 * (NVFBC_RANDR_OUTPUT_INFO outputs[], etc).
 */
typedef struct {
    uint32_t   dwVersion;
    NVFBC_BOOL bIsCapturePossible;
    NVFBC_BOOL bCurrentlyCapturing;
    NVFBC_BOOL bCanCreateNow;
    NVFBC_SIZE screenSize;
    NVFBC_BOOL bXRandRAvailable;
    uint8_t    _pad[4096];
} NVFBC_GET_STATUS_PARAMS;
#define NVFBC_GET_STATUS_PARAMS_VER NVFBC_STRUCT_VERSION(NVFBC_GET_STATUS_PARAMS, 2)

/*
 * Create capture session parameters (version 6).
 */
typedef struct {
    uint32_t            dwVersion;
    NVFBC_CAPTURE_TYPE  eCaptureType;
    NVFBC_TRACKING_TYPE eTrackingType;
    uint32_t            dwOutputId;
    NVFBC_BOX           captureBox;
    NVFBC_SIZE          frameSize;
    NVFBC_BOOL          bWithCursor;
    NVFBC_BOOL          bDisableAutoModesetRecovery;
    NVFBC_BOOL          bRoundFrameSize;
    uint32_t            dwSamplingRateMs;
    NVFBC_BOOL          bPushModel;
    NVFBC_BOOL          bAllowDirectCapture;
} NVFBC_CREATE_CAPTURE_SESSION_PARAMS;
#define NVFBC_CREATE_CAPTURE_SESSION_PARAMS_VER NVFBC_STRUCT_VERSION(NVFBC_CREATE_CAPTURE_SESSION_PARAMS, 6)

/*
 * Destroy capture session parameters.
 */
typedef struct {
    uint32_t dwVersion;
} NVFBC_DESTROY_CAPTURE_SESSION_PARAMS;
#define NVFBC_DESTROY_CAPTURE_SESSION_PARAMS_VER NVFBC_STRUCT_VERSION(NVFBC_DESTROY_CAPTURE_SESSION_PARAMS, 1)

/*
 * TOCUDA setup parameters.
 */
typedef struct {
    uint32_t            dwVersion;
    NVFBC_BUFFER_FORMAT eBufferFormat;
} NVFBC_TOCUDA_SETUP_PARAMS;
#define NVFBC_TOCUDA_SETUP_PARAMS_VER NVFBC_STRUCT_VERSION(NVFBC_TOCUDA_SETUP_PARAMS, 1)

/*
 * TOCUDA grab frame parameters (version 2).
 * pCUDADeviceBuffer is void* (receives the CUdeviceptr value).
 */
typedef struct {
    uint32_t              dwVersion;
    uint32_t              dwFlags;
    void                  *pCUDADeviceBuffer;
    NVFBC_FRAME_GRAB_INFO *pFrameGrabInfo;
    uint32_t              dwTimeoutMs;
} NVFBC_TOCUDA_GRAB_FRAME_PARAMS;
#define NVFBC_TOCUDA_GRAB_FRAME_PARAMS_VER NVFBC_STRUCT_VERSION(NVFBC_TOCUDA_GRAB_FRAME_PARAMS, 2)

/*
 * Bind/release context parameters.
 */
typedef struct { uint32_t dwVersion; } NVFBC_BIND_CONTEXT_PARAMS;
#define NVFBC_BIND_CONTEXT_PARAMS_VER NVFBC_STRUCT_VERSION(NVFBC_BIND_CONTEXT_PARAMS, 1)

typedef struct { uint32_t dwVersion; } NVFBC_RELEASE_CONTEXT_PARAMS;
#define NVFBC_RELEASE_CONTEXT_PARAMS_VER NVFBC_STRUCT_VERSION(NVFBC_RELEASE_CONTEXT_PARAMS, 1)

/*
 * Opaque forward declarations for function pointer types we don't use.
 */
typedef struct { uint32_t dwVersion; } NVFBC_TOSYS_SETUP_PARAMS;
typedef struct { uint32_t dwVersion; } NVFBC_TOSYS_GRAB_FRAME_PARAMS;
typedef struct { uint32_t dwVersion; } NVFBC_TOGL_SETUP_PARAMS;
typedef struct { uint32_t dwVersion; } NVFBC_TOGL_GRAB_FRAME_PARAMS;


/*
 * API function list — populated by NvFBCCreateInstance().
 * dwVersion must be set to NVFBC_VERSION (NOT NVFBC_STRUCT_VERSION).
 * Layout matches the NVIDIA Capture SDK 1.7 header with padding slots.
 */
typedef struct {
    uint32_t dwVersion;
    const char* (*nvFBCGetLastErrorStr)(NVFBC_SESSION_HANDLE);
    NVFBCSTATUS (*nvFBCCreateHandle)(NVFBC_SESSION_HANDLE *, NVFBC_CREATE_HANDLE_PARAMS *);
    NVFBCSTATUS (*nvFBCDestroyHandle)(NVFBC_SESSION_HANDLE, NVFBC_DESTROY_HANDLE_PARAMS *);
    NVFBCSTATUS (*nvFBCGetStatus)(NVFBC_SESSION_HANDLE, NVFBC_GET_STATUS_PARAMS *);
    NVFBCSTATUS (*nvFBCCreateCaptureSession)(NVFBC_SESSION_HANDLE, NVFBC_CREATE_CAPTURE_SESSION_PARAMS *);
    NVFBCSTATUS (*nvFBCDestroyCaptureSession)(NVFBC_SESSION_HANDLE, NVFBC_DESTROY_CAPTURE_SESSION_PARAMS *);
    NVFBCSTATUS (*nvFBCToSysSetUp)(NVFBC_SESSION_HANDLE, NVFBC_TOSYS_SETUP_PARAMS *);
    NVFBCSTATUS (*nvFBCToSysGrabFrame)(NVFBC_SESSION_HANDLE, NVFBC_TOSYS_GRAB_FRAME_PARAMS *);
    NVFBCSTATUS (*nvFBCToCudaSetUp)(NVFBC_SESSION_HANDLE, NVFBC_TOCUDA_SETUP_PARAMS *);
    NVFBCSTATUS (*nvFBCToCudaGrabFrame)(NVFBC_SESSION_HANDLE, NVFBC_TOCUDA_GRAB_FRAME_PARAMS *);
    void *_pad1;
    void *_pad2;
    void *_pad3;
    NVFBCSTATUS (*nvFBCBindContext)(NVFBC_SESSION_HANDLE, NVFBC_BIND_CONTEXT_PARAMS *);
    NVFBCSTATUS (*nvFBCReleaseContext)(NVFBC_SESSION_HANDLE, NVFBC_RELEASE_CONTEXT_PARAMS *);
    void *_pad4;
    void *_pad5;
    void *_pad6;
    void *_pad7;
    NVFBCSTATUS (*nvFBCToGLSetUp)(NVFBC_SESSION_HANDLE, NVFBC_TOGL_SETUP_PARAMS *);
    NVFBCSTATUS (*nvFBCToGLGrabFrame)(NVFBC_SESSION_HANDLE, NVFBC_TOGL_GRAB_FRAME_PARAMS *);
} NVFBC_API_FUNCTION_LIST;

/*
 * Entry point loaded via dlsym.
 */
typedef NVFBCSTATUS (*PFN_NvFBCCreateInstance)(NVFBC_API_FUNCTION_LIST *);

#endif /* NVFBC_H */
