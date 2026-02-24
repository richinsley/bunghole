//go:build darwin

#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreAudio/CoreAudio.h>
#import <Cocoa/Cocoa.h>

#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <pthread.h>

typedef struct {
    void *stream;
    void *delegate;
    void *filter;
    void *buffer;
} SCKAudioCaptureHandle;

typedef struct {
    pthread_mutex_t lock;
    int16_t *samples;
    size_t len;
    size_t cap;
} SCKAudioRing;

static const int kTargetSampleRate = 48000;
static const int kTargetChannels = 2;
static const size_t kMaxBufferedSamples = 48000 * 2 * 2; // ~2 seconds stereo

static inline int16_t float_to_int16(float v) {
    if (v > 1.0f) v = 1.0f;
    if (v < -1.0f) v = -1.0f;
    float scaled = v * 32767.0f;
    if (scaled >= 0.0f) return (int16_t)(scaled + 0.5f);
    return (int16_t)(scaled - 0.5f);
}

static inline float load_sample_as_float(const uint8_t *base, size_t idx,
                                         UInt32 bitsPerChannel,
                                         BOOL isFloat, BOOL isSignedInt) {
    if (isFloat && bitsPerChannel == 32) {
        const float *f = (const float *)base;
        return f[idx];
    }
    if (isSignedInt && bitsPerChannel == 16) {
        const int16_t *s = (const int16_t *)base;
        return ((float)s[idx]) / 32768.0f;
    }
    if (isSignedInt && bitsPerChannel == 32) {
        const int32_t *s = (const int32_t *)base;
        return ((float)s[idx]) / 2147483648.0f;
    }
    return 0.0f;
}

static void ring_append(SCKAudioRing *ring, const int16_t *samples, size_t count) {
    if (!ring || !samples || count == 0) return;

    if (ring->len + count > ring->cap) {
        size_t newCap = ring->cap ? ring->cap : 16384;
        while (newCap < ring->len + count) {
            newCap *= 2;
        }
        int16_t *newBuf = realloc(ring->samples, newCap * sizeof(int16_t));
        if (!newBuf) return;
        ring->samples = newBuf;
        ring->cap = newCap;
    }

    memcpy(ring->samples + ring->len, samples, count * sizeof(int16_t));
    ring->len += count;

    if (ring->len > kMaxBufferedSamples) {
        size_t drop = ring->len - kMaxBufferedSamples;
        memmove(ring->samples, ring->samples + drop, (ring->len - drop) * sizeof(int16_t));
        ring->len -= drop;
    }
}

@interface SCKAudioDelegate : NSObject <SCStreamOutput, SCStreamDelegate>
@property (nonatomic, assign) SCKAudioRing *ring;
@end

@implementation SCKAudioDelegate

- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
        ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeAudio) return;
    if (!sampleBuffer || !CMSampleBufferDataIsReady(sampleBuffer)) return;

    CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(sampleBuffer);
    const AudioStreamBasicDescription *asbd = fmt ? CMAudioFormatDescriptionGetStreamBasicDescription(fmt) : NULL;
    if (!asbd || asbd->mFormatID != kAudioFormatLinearPCM) return;

    size_t frames = (size_t)CMSampleBufferGetNumSamples(sampleBuffer);
    if (frames == 0) return;

    size_t ablSize = 0;
    OSStatus status = CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(
        sampleBuffer,
        &ablSize,
        NULL,
        0,
        kCFAllocatorDefault,
        kCFAllocatorDefault,
        kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment,
        NULL
    );
    if (status != noErr || ablSize == 0) return;

    AudioBufferList *abl = (AudioBufferList *)malloc(ablSize);
    if (!abl) return;

    CMBlockBufferRef blockBuffer = NULL;
    status = CMSampleBufferGetAudioBufferListWithRetainedBlockBuffer(
        sampleBuffer,
        &ablSize,
        abl,
        ablSize,
        kCFAllocatorDefault,
        kCFAllocatorDefault,
        kCMSampleBufferFlag_AudioBufferList_Assure16ByteAlignment,
        &blockBuffer
    );
    if (status != noErr) {
        free(abl);
        return;
    }

    UInt32 srcChannels = asbd->mChannelsPerFrame;
    if (srcChannels == 0 || abl->mNumberBuffers == 0) {
        if (blockBuffer) CFRelease(blockBuffer);
        free(abl);
        return;
    }

    BOOL nonInterleaved = (asbd->mFormatFlags & kAudioFormatFlagIsNonInterleaved) != 0;
    BOOL isFloat = (asbd->mFormatFlags & kAudioFormatFlagIsFloat) != 0;
    BOOL isSignedInt = (asbd->mFormatFlags & kAudioFormatFlagIsSignedInteger) != 0;
    UInt32 bitsPerChannel = asbd->mBitsPerChannel;

    int16_t *out = (int16_t *)malloc(frames * kTargetChannels * sizeof(int16_t));
    if (!out) {
        if (blockBuffer) CFRelease(blockBuffer);
        free(abl);
        return;
    }

    for (size_t i = 0; i < frames; i++) {
        float lr[2] = {0.0f, 0.0f};
        for (int ch = 0; ch < 2; ch++) {
            UInt32 srcCh = (srcChannels == 1) ? 0 : (UInt32)ch;
            if (srcCh >= srcChannels) srcCh = srcChannels - 1;

            const AudioBuffer *buf;
            size_t idx;
            if (nonInterleaved) {
                UInt32 bufIdx = srcCh;
                if (bufIdx >= abl->mNumberBuffers) bufIdx = abl->mNumberBuffers - 1;
                buf = &abl->mBuffers[bufIdx];
                idx = i;
            } else {
                buf = &abl->mBuffers[0];
                idx = i * srcChannels + srcCh;
            }

            if (!buf->mData) {
                lr[ch] = 0.0f;
                continue;
            }

            lr[ch] = load_sample_as_float((const uint8_t *)buf->mData, idx,
                                          bitsPerChannel, isFloat, isSignedInt);
        }

        out[i * 2] = float_to_int16(lr[0]);
        out[i * 2 + 1] = float_to_int16(lr[1]);
    }

    pthread_mutex_lock(&self.ring->lock);
    ring_append(self.ring, out, frames * 2);
    pthread_mutex_unlock(&self.ring->lock);

    free(out);
    if (blockBuffer) CFRelease(blockBuffer);
    free(abl);
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    NSLog(@"audio stream stopped: %@", error);
}

@end

static int sck_audio_start_stream(SCContentFilter *filter, SCKAudioCaptureHandle *out) {
    SCStreamConfiguration *config = [[SCStreamConfiguration alloc] init];
    config.capturesAudio = YES;
    config.sampleRate = kTargetSampleRate;
    config.channelCount = kTargetChannels;
    config.excludesCurrentProcessAudio = NO;
    config.queueDepth = 3;

    SCKAudioDelegate *delegate = [[SCKAudioDelegate alloc] init];
    SCKAudioRing *ring = calloc(1, sizeof(SCKAudioRing));
    if (!ring) return -1;
    pthread_mutex_init(&ring->lock, NULL);
    delegate.ring = ring;

    NSError *err = nil;
    SCStream *stream = [[SCStream alloc] initWithFilter:filter configuration:config delegate:delegate];
    [stream addStreamOutput:delegate
                       type:SCStreamOutputTypeAudio
         sampleHandlerQueue:dispatch_get_global_queue(QOS_CLASS_USER_INTERACTIVE, 0)
                      error:&err];
    if (err) {
        NSLog(@"sck_audio_start_stream: add output failed: %@", err);
        pthread_mutex_destroy(&ring->lock);
        free(ring);
        return -1;
    }

    __block int startResult = 0;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [stream startCaptureWithCompletionHandler:^(NSError *error) {
        if (error) {
            NSLog(@"sck_audio_start_stream: start failed: %@", error);
            startResult = -1;
        }
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

    if (startResult != 0) {
        pthread_mutex_destroy(&ring->lock);
        free(ring);
        return -1;
    }

    out->stream = (void *)CFBridgingRetain(stream);
    out->delegate = (void *)CFBridgingRetain(delegate);
    out->filter = (void *)CFBridgingRetain(filter);
    out->buffer = (void *)ring;
    return 0;
}

int sck_audio_start_display(SCKAudioCaptureHandle *out) {
    @autoreleasepool {
        memset(out, 0, sizeof(SCKAudioCaptureHandle));

        __block SCDisplay *mainDisplay = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        [SCShareableContent getShareableContentWithCompletionHandler:
            ^(SCShareableContent *content, NSError *error) {
                if (error) {
                    NSLog(@"sck_audio_start_display: shareable content error: %@", error);
                    dispatch_semaphore_signal(sem);
                    return;
                }
                for (SCDisplay *d in content.displays) {
                    if (!mainDisplay || (d.width * d.height > mainDisplay.width * mainDisplay.height)) {
                        mainDisplay = d;
                    }
                }
                dispatch_semaphore_signal(sem);
            }];
        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

        if (!mainDisplay) {
            NSLog(@"sck_audio_start_display: no display found");
            return -1;
        }

        SCContentFilter *filter = [[SCContentFilter alloc] initWithDisplay:mainDisplay excludingWindows:@[]];
        return sck_audio_start_stream(filter, out);
    }
}

int sck_audio_start_window(uint32_t windowID, SCKAudioCaptureHandle *out) {
    @autoreleasepool {
        memset(out, 0, sizeof(SCKAudioCaptureHandle));

        if (windowID == 0) {
            NSLog(@"sck_audio_start_window: invalid window id");
            return -1;
        }

        __block SCContentFilter *filter = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);

        void (^lookupBlock)(void) = ^{
            [SCShareableContent getShareableContentWithCompletionHandler:
                ^(SCShareableContent *content, NSError *error) {
                    if (error) {
                        NSLog(@"sck_audio_start_window: shareable content error: %@", error);
                        dispatch_semaphore_signal(sem);
                        return;
                    }

                    SCWindow *targetWindow = nil;
                    for (SCWindow *w in content.windows) {
                        if (w.windowID == windowID) {
                            targetWindow = w;
                            break;
                        }
                    }

                    if (!targetWindow) {
                        NSLog(@"sck_audio_start_window: window %u not found", windowID);
                        dispatch_semaphore_signal(sem);
                        return;
                    }

                    filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:targetWindow];
                    dispatch_semaphore_signal(sem);
                }];
        };

        if ([NSThread isMainThread]) {
            lookupBlock();
        } else {
            dispatch_async(dispatch_get_main_queue(), lookupBlock);
        }

        if (dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC)) != 0) {
            NSLog(@"sck_audio_start_window: timed out waiting for shareable content");
            return -1;
        }

        if (!filter) {
            return -1;
        }

        return sck_audio_start_stream(filter, out);
    }
}

int sck_audio_read_frame(SCKAudioCaptureHandle *h, int16_t *dst, int samples_per_channel) {
    if (!h || !dst || !h->buffer || samples_per_channel <= 0) return -1;

    size_t needed = (size_t)samples_per_channel * kTargetChannels;
    SCKAudioRing *ring = (SCKAudioRing *)h->buffer;

    pthread_mutex_lock(&ring->lock);
    if (ring->len < needed) {
        pthread_mutex_unlock(&ring->lock);
        return -1;
    }

    memcpy(dst, ring->samples, needed * sizeof(int16_t));
    ring->len -= needed;
    if (ring->len > 0) {
        memmove(ring->samples, ring->samples + needed, ring->len * sizeof(int16_t));
    }
    pthread_mutex_unlock(&ring->lock);
    return 0;
}

void sck_audio_stop(SCKAudioCaptureHandle *h) {
    @autoreleasepool {
        if (!h) return;

        if (h->stream) {
            SCStream *stream = CFBridgingRelease(h->stream);
            dispatch_semaphore_t sem = dispatch_semaphore_create(0);
            [stream stopCaptureWithCompletionHandler:^(NSError *error) {
                dispatch_semaphore_signal(sem);
            }];
            dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
            (void)stream;
        }

        if (h->delegate) {
            SCKAudioDelegate *delegate = CFBridgingRelease(h->delegate);
            (void)delegate;
        }

        if (h->filter) {
            id filter = CFBridgingRelease(h->filter);
            (void)filter;
        }

        if (h->buffer) {
            SCKAudioRing *ring = (SCKAudioRing *)h->buffer;
            pthread_mutex_lock(&ring->lock);
            free(ring->samples);
            ring->samples = NULL;
            ring->len = 0;
            ring->cap = 0;
            pthread_mutex_unlock(&ring->lock);
            pthread_mutex_destroy(&ring->lock);
            free(ring);
            h->buffer = NULL;
        }

        h->stream = NULL;
        h->delegate = NULL;
        h->filter = NULL;
    }
}
