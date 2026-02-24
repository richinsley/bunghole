//go:build darwin

#import <ScreenCaptureKit/ScreenCaptureKit.h>
#import <CoreMedia/CoreMedia.h>
#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <pthread.h>

typedef struct {
    void *stream;          // SCStream*
    void *delegate;        // SCKCaptureDelegate*
    void *filter;          // SCContentFilter*
    int width;
    int height;
} SCKCaptureHandle;

// Latest captured frame data (CF types managed manually, not ARC)
typedef struct {
    uint8_t *data;
    int stride;
    int width;
    int height;
    CMSampleBufferRef sampleBuffer;
    CVPixelBufferRef pixelBuffer;
    pthread_mutex_t lock;
} SCKCaptureFrame;

// ---- Capture Delegate ----

@interface SCKCaptureDelegate : NSObject <SCStreamOutput, SCStreamDelegate>
@property (nonatomic, assign) SCKCaptureFrame *frame;
@end

@implementation SCKCaptureDelegate

- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
        ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen) return;

    CVPixelBufferRef pixelBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
    if (!pixelBuffer) return;

    CVReturn lockResult = CVPixelBufferLockBaseAddress(pixelBuffer, kCVPixelBufferLock_ReadOnly);
    if (lockResult != kCVReturnSuccess) return;

    uint8_t *baseAddress = (uint8_t *)CVPixelBufferGetBaseAddress(pixelBuffer);
    int stride = (int)CVPixelBufferGetBytesPerRow(pixelBuffer);
    int width = (int)CVPixelBufferGetWidth(pixelBuffer);
    int height = (int)CVPixelBufferGetHeight(pixelBuffer);

    pthread_mutex_lock(&self.frame->lock);

    // Release previous buffer
    if (self.frame->pixelBuffer) {
        CVPixelBufferUnlockBaseAddress(self.frame->pixelBuffer, kCVPixelBufferLock_ReadOnly);
        CVPixelBufferRelease(self.frame->pixelBuffer);
    }
    if (self.frame->sampleBuffer) {
        CFRelease(self.frame->sampleBuffer);
    }

    // Retain new buffer (CF types — manual retain)
    CFRetain(sampleBuffer);
    CVPixelBufferRetain(pixelBuffer);
    self.frame->sampleBuffer = sampleBuffer;
    self.frame->pixelBuffer = pixelBuffer;
    self.frame->data = baseAddress;
    self.frame->stride = stride;
    self.frame->width = width;
    self.frame->height = height;

    pthread_mutex_unlock(&self.frame->lock);
}

- (void)stream:(SCStream *)stream didStopWithError:(NSError *)error {
    NSLog(@"SCStream stopped: %@", error);
}

@end

// ---- Shared helpers ----

static int sck_start_stream(SCContentFilter *filter, int fps, int w, int h,
                            SCKCaptureHandle *out) {
    SCStreamConfiguration *config = [[SCStreamConfiguration alloc] init];
    config.width = w;
    config.height = h;
    config.minimumFrameInterval = CMTimeMake(1, fps);
    config.queueDepth = 3;
    config.pixelFormat = kCVPixelFormatType_32BGRA;
    config.showsCursor = YES;

    SCKCaptureDelegate *delegate = [[SCKCaptureDelegate alloc] init];
    SCKCaptureFrame *frame = calloc(1, sizeof(SCKCaptureFrame));
    pthread_mutex_init(&frame->lock, NULL);
    delegate.frame = frame;

    NSError *err = nil;
    SCStream *stream = [[SCStream alloc] initWithFilter:filter
        configuration:config delegate:delegate];

    [stream addStreamOutput:delegate type:SCStreamOutputTypeScreen
        sampleHandlerQueue:dispatch_get_global_queue(QOS_CLASS_USER_INTERACTIVE, 0)
        error:&err];
    if (err) {
        NSLog(@"sck_start_stream: addStreamOutput error: %@", err);
        pthread_mutex_destroy(&frame->lock);
        free(frame);
        return -1;
    }

    __block int startResult = 0;
    dispatch_semaphore_t sem = dispatch_semaphore_create(0);
    [stream startCaptureWithCompletionHandler:^(NSError *error) {
        if (error) {
            NSLog(@"sck_start_stream: startCapture error: %@", error);
            startResult = -1;
        }
        dispatch_semaphore_signal(sem);
    }];
    dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

    if (startResult != 0) {
        pthread_mutex_destroy(&frame->lock);
        free(frame);
        return -1;
    }

    out->stream = (void *)CFBridgingRetain(stream);
    out->delegate = (void *)CFBridgingRetain(delegate);
    out->filter = (void *)CFBridgingRetain(filter);
    out->width = w;
    out->height = h;
    return 0;
}

// ---- Host display capture ----

int sck_capture_start_display(int fps, SCKCaptureHandle *out) {
    @autoreleasepool {
        memset(out, 0, sizeof(SCKCaptureHandle));

        __block SCDisplay *mainDisplay = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);

        [SCShareableContent getShareableContentWithCompletionHandler:
            ^(SCShareableContent *content, NSError *error) {
                if (error) {
                    NSLog(@"sck_capture_start_display: error: %@", error);
                    dispatch_semaphore_signal(sem);
                    return;
                }
                // Find main display (largest or first)
                for (SCDisplay *d in content.displays) {
                    if (!mainDisplay || (d.width * d.height > mainDisplay.width * mainDisplay.height)) {
                        mainDisplay = d;
                    }
                }
                dispatch_semaphore_signal(sem);
            }];

        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

        if (!mainDisplay) {
            NSLog(@"sck_capture_start_display: no display found");
            return -1;
        }

        int w = (int)mainDisplay.width;
        int h = (int)mainDisplay.height;

        SCContentFilter *filter = [[SCContentFilter alloc]
            initWithDisplay:mainDisplay excludingWindows:@[]];

        int ret = sck_start_stream(filter, fps, w, h, out);
        if (ret == 0) {
            NSLog(@"sck_capture_start_display: capturing %dx%d @ %d fps", w, h, fps);
        }
        return ret;
    }
}

// ---- VM window capture ----

int sck_capture_start_window(uint32_t windowID, int fps, int w, int h, SCKCaptureHandle *out) {
    @autoreleasepool {
        memset(out, 0, sizeof(SCKCaptureHandle));

        if (windowID == 0) {
            NSLog(@"sck_capture_start_window: invalid window id");
            return -1;
        }

        __block SCContentFilter *filter = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);

        void (^lookupBlock)(void) = ^{
            [SCShareableContent getShareableContentWithCompletionHandler:
                ^(SCShareableContent *content, NSError *error) {
                    if (error) {
                        NSLog(@"sck_capture_start_window: error: %@", error);
                        dispatch_semaphore_signal(sem);
                        return;
                    }

                    SCWindow *targetWindow = nil;
                    for (SCWindow *win in content.windows) {
                        if (win.windowID == windowID) {
                            targetWindow = win;
                            break;
                        }
                    }

                    if (!targetWindow) {
                        NSLog(@"sck_capture_start_window: window %u not found", windowID);
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
            NSLog(@"sck_capture_start_window: timed out waiting for shareable content");
            return -1;
        }

        if (!filter) {
            return -1;
        }

        int ret = sck_start_stream(filter, fps, w, h, out);
        if (ret == 0) {
            NSLog(@"sck_capture_start_window: capturing window %u at %dx%d @ %d fps",
                  windowID, w, h, fps);
        }
        return ret;
    }
}

// ---- Shared grab / stop ----

int sck_capture_grab(SCKCaptureHandle *h, uint8_t **buf, int *stride, int *w, int *h_out) {
    SCKCaptureDelegate *delegate = (__bridge SCKCaptureDelegate *)h->delegate;
    SCKCaptureFrame *frame = delegate.frame;

    pthread_mutex_lock(&frame->lock);

    if (!frame->data) {
        pthread_mutex_unlock(&frame->lock);
        return -1;
    }

    *buf = frame->data;
    *stride = frame->stride;
    *w = frame->width;
    *h_out = frame->height;

    pthread_mutex_unlock(&frame->lock);
    return 0;
}

void sck_capture_stop(SCKCaptureHandle *h) {
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
            SCKCaptureDelegate *delegate = CFBridgingRelease(h->delegate);
            SCKCaptureFrame *frame = delegate.frame;
            if (frame) {
                pthread_mutex_lock(&frame->lock);
                if (frame->pixelBuffer) {
                    CVPixelBufferUnlockBaseAddress(frame->pixelBuffer, kCVPixelBufferLock_ReadOnly);
                    CVPixelBufferRelease(frame->pixelBuffer);
                }
                if (frame->sampleBuffer) {
                    CFRelease(frame->sampleBuffer);
                }
                pthread_mutex_unlock(&frame->lock);
                pthread_mutex_destroy(&frame->lock);
                free(frame);
            }
            (void)delegate;
        }

        if (h->filter) {
            id filter = CFBridgingRelease(h->filter);
            (void)filter;
        }
    }
}
