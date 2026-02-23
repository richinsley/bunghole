/*
 * Minimal CUDA type definitions for dynamic loading.
 * No CUDA SDK dependency at build time — everything is dlopen'd at runtime.
 * If the real cuda.h is already included, we skip our type stubs but still
 * provide the function pointer typedefs.
 */
#ifndef CUDA_DEFS_H
#define CUDA_DEFS_H

#include <stdint.h>

#ifdef __cuda_cuda_h__
/* Real cuda.h is available — use its types */
#else
/* Stub types matching the CUDA driver API ABI */
typedef int CUresult;
typedef int CUdevice;
typedef void *CUcontext;
typedef unsigned long long CUdeviceptr;

#define CUDA_SUCCESS 0
#endif

/* Function pointer types for dynamically loaded CUDA driver API */
typedef CUresult (*PFN_cuInit)(unsigned int);
typedef CUresult (*PFN_cuDeviceGet)(CUdevice *, int);
typedef CUresult (*PFN_cuDeviceGetName)(char *, int, CUdevice);
typedef CUresult (*PFN_cuDeviceGetByPCIBusId)(CUdevice *, const char *);
typedef CUresult (*PFN_cuCtxCreate)(CUcontext *, unsigned int, CUdevice);
typedef CUresult (*PFN_cuCtxDestroy)(CUcontext);
typedef CUresult (*PFN_cuCtxSetCurrent)(CUcontext);
typedef CUresult (*PFN_cuCtxGetCurrent)(CUcontext *);
typedef CUresult (*PFN_cuMemcpyDtoH)(void *, CUdeviceptr, size_t);

#endif /* CUDA_DEFS_H */
