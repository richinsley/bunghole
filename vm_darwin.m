//go:build darwin

#import <Virtualization/Virtualization.h>
#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>
#include <sys/sysctl.h>
#include <sys/fcntl.h>
#include <unistd.h>
#include <dispatch/dispatch.h>

// ---- Types exposed to Go via cgo ----

typedef struct {
    void *vm;       // VZVirtualMachine*
    void *view;     // VZVirtualMachineView*
    void *window;   // NSWindow*
    void *delegate; // VMDelegate*
    int width;
    int height;
} VMHandle;

// ---- VM Delegate ----

@interface VMDelegate : NSObject <VZVirtualMachineDelegate>
@property (nonatomic, assign) BOOL stopped;
@property (nonatomic, strong) NSString *errorMessage;
@end

@implementation VMDelegate
- (void)virtualMachine:(VZVirtualMachine *)vm didStopWithError:(NSError *)error {
    self.stopped = YES;
    self.errorMessage = error.localizedDescription;
    NSLog(@"VM stopped with error: %@", error);
}
- (void)guestDidStopVirtualMachine:(VZVirtualMachine *)vm {
    self.stopped = YES;
    NSLog(@"VM guest initiated shutdown");
}
@end

// ---- Hardware config JSON ----

typedef struct {
    char *hardwareModelBase64;
    char *machineIdentifierBase64;
} HardwareConfig;

static HardwareConfig* load_hardware_config(const char *bundle_path) {
    @autoreleasepool {
        NSString *path = [NSString stringWithUTF8String:bundle_path];
        NSString *jsonPath = [path stringByAppendingPathComponent:@"hardware.json"];
        NSData *data = [NSData dataWithContentsOfFile:jsonPath];
        if (!data) return NULL;

        NSError *err = nil;
        NSDictionary *dict = [NSJSONSerialization JSONObjectWithData:data options:0 error:&err];
        if (err || !dict) return NULL;

        HardwareConfig *cfg = calloc(1, sizeof(HardwareConfig));
        NSString *hw = dict[@"hardwareModel"];
        NSString *mi = dict[@"machineIdentifier"];
        if (hw) cfg->hardwareModelBase64 = strdup([hw UTF8String]);
        if (mi) cfg->machineIdentifierBase64 = strdup([mi UTF8String]);
        return cfg;
    }
}

static void free_hardware_config(HardwareConfig *cfg) {
    if (!cfg) return;
    free(cfg->hardwareModelBase64);
    free(cfg->machineIdentifierBase64);
    free(cfg);
}

static int save_hardware_config(const char *bundle_path, NSData *hwModel, NSData *machineId) {
    @autoreleasepool {
        NSString *path = [NSString stringWithUTF8String:bundle_path];
        NSString *jsonPath = [path stringByAppendingPathComponent:@"hardware.json"];

        NSString *hwBase64 = [hwModel base64EncodedStringWithOptions:0];
        NSString *miBase64 = [machineId base64EncodedStringWithOptions:0];

        NSDictionary *dict = @{
            @"hardwareModel": hwBase64,
            @"machineIdentifier": miBase64,
        };

        NSError *err = nil;
        NSData *json = [NSJSONSerialization dataWithJSONObject:dict options:NSJSONWritingPrettyPrinted error:&err];
        if (err) return -1;

        return [json writeToFile:jsonPath atomically:YES] ? 0 : -1;
    }
}

// ---- Helper: physical core count ----

static int physical_core_count(void) {
    int count = 0;
    size_t size = sizeof(count);
    if (sysctlbyname("hw.perflevel0.physicalcpu", &count, &size, NULL, 0) == 0 && count > 0) {
        return count;
    }
    if (sysctlbyname("hw.physicalcpu", &count, &size, NULL, 0) == 0 && count > 0) {
        return count;
    }
    return 4;
}

// ---- Helper: host RAM in bytes ----

static uint64_t host_ram_bytes(void) {
    uint64_t ram = 0;
    size_t size = sizeof(ram);
    if (sysctlbyname("hw.memsize", &ram, &size, NULL, 0) == 0) {
        return ram;
    }
    return 8ULL * 1024 * 1024 * 1024;
}

// ---- NSApplication RunLoop ----

void vm_nsapp_run(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];
        [NSApp run];
    }
}

void vm_nsapp_stop(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp stop:nil];
        NSEvent *event = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
            location:NSMakePoint(0, 0) modifierFlags:0 timestamp:0
            windowNumber:0 context:nil subtype:0 data1:0 data2:0];
        [NSApp postEvent:event atStart:YES];
    });
}

// ---- VM Create ----

int vm_create(const char *bundle_path, const char *shared_dir,
              int width, int height, VMHandle *out) {
    @autoreleasepool {
        memset(out, 0, sizeof(VMHandle));

        NSString *bundlePath = [NSString stringWithUTF8String:bundle_path];
        NSString *diskPath = [bundlePath stringByAppendingPathComponent:@"disk.img"];
        NSString *auxPath = [bundlePath stringByAppendingPathComponent:@"aux.img"];

        HardwareConfig *hwCfg = load_hardware_config(bundle_path);
        if (!hwCfg) {
            NSLog(@"vm_create: failed to load hardware.json");
            return -1;
        }

        NSData *hwModelData = [[NSData alloc] initWithBase64EncodedString:
            [NSString stringWithUTF8String:hwCfg->hardwareModelBase64] options:0];
        NSData *machineIdData = [[NSData alloc] initWithBase64EncodedString:
            [NSString stringWithUTF8String:hwCfg->machineIdentifierBase64] options:0];
        free_hardware_config(hwCfg);

        if (!hwModelData || !machineIdData) {
            NSLog(@"vm_create: invalid hardware config data");
            return -1;
        }

        VZMacHardwareModel *hardwareModel = [[VZMacHardwareModel alloc]
            initWithDataRepresentation:hwModelData];
        if (!hardwareModel) {
            NSLog(@"vm_create: invalid hardware model");
            return -1;
        }

        VZMacMachineIdentifier *machineIdentifier = [[VZMacMachineIdentifier alloc]
            initWithDataRepresentation:machineIdData];
        if (!machineIdentifier) {
            NSLog(@"vm_create: invalid machine identifier");
            return -1;
        }

        NSError *err = nil;
        VZMacAuxiliaryStorage *auxStorage = [[VZMacAuxiliaryStorage alloc]
            initWithURL:[NSURL fileURLWithPath:auxPath]];

        VZMacPlatformConfiguration *platform = [[VZMacPlatformConfiguration alloc] init];
        platform.hardwareModel = hardwareModel;
        platform.machineIdentifier = machineIdentifier;
        platform.auxiliaryStorage = auxStorage;

        VZMacOSBootLoader *bootLoader = [[VZMacOSBootLoader alloc] init];

        int cpuCount = physical_core_count();
        if (cpuCount < (int)VZVirtualMachineConfiguration.minimumAllowedCPUCount) {
            cpuCount = (int)VZVirtualMachineConfiguration.minimumAllowedCPUCount;
        }

        uint64_t ramBytes = host_ram_bytes() / 2;
        uint64_t maxRAM = 16ULL * 1024 * 1024 * 1024;
        if (ramBytes > maxRAM) ramBytes = maxRAM;
        uint64_t minRAM = VZVirtualMachineConfiguration.minimumAllowedMemorySize;
        if (ramBytes < minRAM) ramBytes = minRAM;

        VZMacGraphicsDeviceConfiguration *gpuDev = [[VZMacGraphicsDeviceConfiguration alloc] init];
        VZMacGraphicsDisplayConfiguration *display = [[VZMacGraphicsDisplayConfiguration alloc]
            initWithWidthInPixels:width heightInPixels:height pixelsPerInch:72];
        gpuDev.displays = @[display];

        VZDiskImageStorageDeviceAttachment *diskAttachment = [[VZDiskImageStorageDeviceAttachment alloc]
            initWithURL:[NSURL fileURLWithPath:diskPath] readOnly:NO error:&err];
        if (err) {
            NSLog(@"vm_create: disk attachment error: %@", err);
            return -1;
        }
        VZVirtioBlockDeviceConfiguration *disk = [[VZVirtioBlockDeviceConfiguration alloc]
            initWithAttachment:diskAttachment];

        VZVirtioNetworkDeviceConfiguration *net = [[VZVirtioNetworkDeviceConfiguration alloc] init];
        net.attachment = [[VZNATNetworkDeviceAttachment alloc] init];

        VZUSBKeyboardConfiguration *keyboard = [[VZUSBKeyboardConfiguration alloc] init];
        VZUSBScreenCoordinatePointingDeviceConfiguration *pointing =
            [[VZUSBScreenCoordinatePointingDeviceConfiguration alloc] init];

        VZVirtioSoundDeviceConfiguration *sound = [[VZVirtioSoundDeviceConfiguration alloc] init];
        VZVirtioSoundDeviceOutputStreamConfiguration *audioOut =
            [[VZVirtioSoundDeviceOutputStreamConfiguration alloc] init];
        sound.streams = @[audioOut];

        VZVirtualMachineConfiguration *config = [[VZVirtualMachineConfiguration alloc] init];
        config.platform = platform;
        config.bootLoader = bootLoader;
        config.CPUCount = cpuCount;
        config.memorySize = ramBytes;
        config.graphicsDevices = @[gpuDev];
        config.storageDevices = @[disk];
        config.networkDevices = @[net];
        config.keyboards = @[keyboard];
        config.pointingDevices = @[pointing];
        config.audioDevices = @[sound];

        if (shared_dir) {
            NSString *sharePath = [NSString stringWithUTF8String:shared_dir];
            VZSharedDirectory *sharedDirectory = [[VZSharedDirectory alloc]
                initWithURL:[NSURL fileURLWithPath:sharePath] readOnly:NO];
            VZSingleDirectoryShare *share = [[VZSingleDirectoryShare alloc]
                initWithDirectory:sharedDirectory];
            VZVirtioFileSystemDeviceConfiguration *fs = [[VZVirtioFileSystemDeviceConfiguration alloc]
                initWithTag:VZVirtioFileSystemDeviceConfiguration.macOSGuestAutomountTag];
            fs.share = share;
            config.directorySharingDevices = @[fs];
        }

        [config validateWithError:&err];
        if (err) {
            NSLog(@"vm_create: config validation error: %@", err);
            return -1;
        }

        // Create VM on main thread
        __block VZVirtualMachine *vm = nil;
        __block VMDelegate *delegate = nil;
        __block NSWindow *window = nil;
        __block VZVirtualMachineView *vmView = nil;

        void (^createBlock)(void) = ^{
            vm = [[VZVirtualMachine alloc] initWithConfiguration:config];
            delegate = [[VMDelegate alloc] init];
            vm.delegate = delegate;

            vmView = [[VZVirtualMachineView alloc] initWithFrame:NSMakeRect(0, 0, width, height)];
            vmView.virtualMachine = vm;
            vmView.capturesSystemKeys = YES;

            window = [[NSWindow alloc]
                initWithContentRect:NSMakeRect(-10000, -10000, width, height)
                styleMask:NSWindowStyleMaskBorderless
                backing:NSBackingStoreBuffered
                defer:NO];
            [window setContentView:vmView];
            [window setLevel:NSNormalWindowLevel];
            [window orderFront:nil];
        };

        if ([NSThread isMainThread]) {
            createBlock();
        } else {
            dispatch_sync(dispatch_get_main_queue(), createBlock);
        }

        // Manual retain for crossing into C void* storage
        out->vm = (void *)CFBridgingRetain(vm);
        out->view = (void *)CFBridgingRetain(vmView);
        out->window = (void *)CFBridgingRetain(window);
        out->delegate = (void *)CFBridgingRetain(delegate);
        out->width = width;
        out->height = height;

        NSLog(@"vm_create: VM created (%d CPUs, %llu MB RAM, %dx%d)",
              cpuCount, ramBytes / (1024*1024), width, height);
        return 0;
    }
}

// ---- VM Start ----

int vm_start(VMHandle *h) {
    @autoreleasepool {
        VZVirtualMachine *vm = (__bridge VZVirtualMachine *)h->vm;

        __block int result = 0;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);

        void (^startBlock)(void) = ^{
            [vm startWithCompletionHandler:^(NSError *error) {
                if (error) {
                    NSLog(@"vm_start: error: %@", error);
                    result = -1;
                } else {
                    NSLog(@"vm_start: VM started successfully");
                }
                dispatch_semaphore_signal(sem);
            }];
        };

        if ([NSThread isMainThread]) {
            startBlock();
        } else {
            dispatch_async(dispatch_get_main_queue(), startBlock);
        }

        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
        return result;
    }
}

// ---- VM Stop ----

void vm_stop(VMHandle *h) {
    @autoreleasepool {
        if (!h || !h->vm) return;
        VZVirtualMachine *vm = (__bridge VZVirtualMachine *)h->vm;

        dispatch_semaphore_t sem = dispatch_semaphore_create(0);

        void (^stopBlock)(void) = ^{
            if (vm.canRequestStop) {
                [vm requestStopWithError:nil];
            }
            dispatch_after(dispatch_time(DISPATCH_TIME_NOW, 3 * NSEC_PER_SEC),
                dispatch_get_main_queue(), ^{
                    if (vm.state != VZVirtualMachineStateStopped) {
                        [vm stopWithCompletionHandler:^(NSError *error) {
                            dispatch_semaphore_signal(sem);
                        }];
                    } else {
                        dispatch_semaphore_signal(sem);
                    }
                });
        };

        if ([NSThread isMainThread]) {
            stopBlock();
        } else {
            dispatch_async(dispatch_get_main_queue(), stopBlock);
        }

        dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 10 * NSEC_PER_SEC));
    }
}

// ---- VM Destroy ----

void vm_destroy(VMHandle *h) {
    @autoreleasepool {
        if (!h) return;

        void (^destroyBlock)(void) = ^{
            if (h->window) {
                NSWindow *window = CFBridgingRelease(h->window);
                [window orderOut:nil];
                (void)window;
            }
            if (h->view) {
                id view = CFBridgingRelease(h->view);
                (void)view;
            }
            if (h->vm) {
                id vm = CFBridgingRelease(h->vm);
                (void)vm;
            }
            if (h->delegate) {
                id delegate = CFBridgingRelease(h->delegate);
                (void)delegate;
            }
        };

        if ([NSThread isMainThread]) {
            destroyBlock();
        } else {
            dispatch_sync(dispatch_get_main_queue(), destroyBlock);
        }
    }
}

// ---- Accessors ----

void* vm_get_window(VMHandle *h) {
    return h ? h->window : NULL;
}

void* vm_get_view(VMHandle *h) {
    return h ? h->view : NULL;
}

// ---- Setup / Install ----

int vm_fetch_restore_url(char **out_url, uint64_t *out_size) {
    @autoreleasepool {
        __block int result = 0;
        __block NSString *urlString = nil;
        __block uint64_t imageSize = 0;

        dispatch_semaphore_t sem = dispatch_semaphore_create(0);

        [VZMacOSRestoreImage fetchLatestSupportedWithCompletionHandler:
            ^(VZMacOSRestoreImage *restoreImage, NSError *error) {
                if (error || !restoreImage) {
                    NSLog(@"vm_fetch_restore_url: error: %@", error);
                    result = -1;
                } else {
                    urlString = restoreImage.URL.absoluteString;

                    VZMacOSConfigurationRequirements *reqs =
                        restoreImage.mostFeaturefulSupportedConfiguration;
                    if (reqs) {
                        imageSize = reqs.minimumSupportedMemorySize;
                    }
                }
                dispatch_semaphore_signal(sem);
            }];

        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

        if (result == 0 && urlString) {
            *out_url = strdup([urlString UTF8String]);
            *out_size = imageSize;
        }
        return result;
    }
}

int vm_download_ipsw(const char *url, const char *dest,
                     void (*progress)(uint64_t done, uint64_t total)) {
    @autoreleasepool {
        NSURL *srcURL = [NSURL URLWithString:[NSString stringWithUTF8String:url]];
        NSString *destPath = [NSString stringWithUTF8String:dest];

        __block int result = 0;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);

        NSURLSessionConfiguration *sessionConfig = [NSURLSessionConfiguration defaultSessionConfiguration];
        NSURLSession *session = [NSURLSession sessionWithConfiguration:sessionConfig];

        NSURLSessionDownloadTask *task = [session downloadTaskWithURL:srcURL
            completionHandler:^(NSURL *location, NSURLResponse *response, NSError *error) {
                if (error) {
                    NSLog(@"vm_download_ipsw: error: %@", error);
                    result = -1;
                } else {
                    NSError *moveErr = nil;
                    [[NSFileManager defaultManager] moveItemAtURL:location
                        toURL:[NSURL fileURLWithPath:destPath] error:&moveErr];
                    if (moveErr) {
                        NSLog(@"vm_download_ipsw: move error: %@", moveErr);
                        result = -1;
                    }
                }
                dispatch_semaphore_signal(sem);
            }];

        [task resume];
        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
        return result;
    }
}

int vm_create_bundle(const char *ipsw_path, const char *bundle_path, uint64_t disk_gb) {
    @autoreleasepool {
        NSString *bundlePath = [NSString stringWithUTF8String:bundle_path];
        NSString *ipswPath = [NSString stringWithUTF8String:ipsw_path];
        NSString *diskPath = [bundlePath stringByAppendingPathComponent:@"disk.img"];
        NSString *auxPath = [bundlePath stringByAppendingPathComponent:@"aux.img"];

        NSFileManager *fm = [NSFileManager defaultManager];
        NSError *err = nil;

        [fm createDirectoryAtPath:bundlePath withIntermediateDirectories:YES attributes:nil error:&err];
        if (err) {
            NSLog(@"vm_create_bundle: mkdir error: %@", err);
            return -1;
        }

        // Load restore image to get hardware model
        __block VZMacOSRestoreImage *restoreImage = nil;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);

        [VZMacOSRestoreImage fetchLatestSupportedWithCompletionHandler:
            ^(VZMacOSRestoreImage *image, NSError *error) {
                if (error) {
                    NSLog(@"vm_create_bundle: fetch restore image error: %@", error);
                }
                // Now load from local file
                dispatch_semaphore_signal(sem);
            }];
        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

        // Load from local IPSW file using NSData approach
        NSURL *ipswURL = [NSURL fileURLWithPath:ipswPath];
        sem = dispatch_semaphore_create(0);
        [VZMacOSRestoreImage fetchLatestSupportedWithCompletionHandler:
            ^(VZMacOSRestoreImage *image, NSError *error) {
                // We just need a supported hardware model - get it from latest
                if (!error) restoreImage = image;
                dispatch_semaphore_signal(sem);
            }];
        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

        if (!restoreImage) return -1;

        VZMacOSConfigurationRequirements *reqs = restoreImage.mostFeaturefulSupportedConfiguration;
        if (!reqs) {
            NSLog(@"vm_create_bundle: no supported configuration");
            return -1;
        }
        if (!reqs.hardwareModel.supported) {
            NSLog(@"vm_create_bundle: hardware model not supported on this host");
            return -1;
        }

        VZMacHardwareModel *hardwareModel = reqs.hardwareModel;
        VZMacMachineIdentifier *machineId = [[VZMacMachineIdentifier alloc] init];

        // Create auxiliary storage
        VZMacAuxiliaryStorage *auxStorage = [[VZMacAuxiliaryStorage alloc]
            initCreatingStorageAtURL:[NSURL fileURLWithPath:auxPath]
            hardwareModel:hardwareModel
            options:VZMacAuxiliaryStorageInitializationOptionAllowOverwrite
            error:&err];
        if (err || !auxStorage) {
            NSLog(@"vm_create_bundle: aux storage error: %@", err);
            return -1;
        }

        // Create sparse disk image (minimum 64GB)
        uint64_t diskSize = disk_gb * 1024ULL * 1024ULL * 1024ULL;
        uint64_t minDisk = 64ULL * 1024ULL * 1024ULL * 1024ULL;
        if (diskSize < minDisk) diskSize = minDisk;

        int fd = open([diskPath UTF8String], O_RDWR | O_CREAT | O_TRUNC, 0644);
        if (fd < 0) {
            NSLog(@"vm_create_bundle: create disk error");
            return -1;
        }
        ftruncate(fd, diskSize);
        close(fd);

        // Save hardware config
        if (save_hardware_config(bundle_path, hardwareModel.dataRepresentation,
                                 machineId.dataRepresentation) != 0) {
            NSLog(@"vm_create_bundle: save hardware config error");
            return -1;
        }

        NSLog(@"vm_create_bundle: bundle created at %@ (disk: %llu GB)", bundlePath, disk_gb);
        return 0;
    }
}

int vm_install(const char *bundle_path, const char *ipsw_path,
               void (*progress)(double fraction)) {
    @autoreleasepool {
        NSString *bundlePath = [NSString stringWithUTF8String:bundle_path];
        NSString *ipswPath = [NSString stringWithUTF8String:ipsw_path];
        NSString *diskPath = [bundlePath stringByAppendingPathComponent:@"disk.img"];
        NSString *auxPath = [bundlePath stringByAppendingPathComponent:@"aux.img"];

        HardwareConfig *hwCfg = load_hardware_config(bundle_path);
        if (!hwCfg) {
            NSLog(@"vm_install: failed to load hardware config");
            return -1;
        }

        NSData *hwModelData = [[NSData alloc] initWithBase64EncodedString:
            [NSString stringWithUTF8String:hwCfg->hardwareModelBase64] options:0];
        NSData *machineIdData = [[NSData alloc] initWithBase64EncodedString:
            [NSString stringWithUTF8String:hwCfg->machineIdentifierBase64] options:0];
        free_hardware_config(hwCfg);

        VZMacHardwareModel *hardwareModel = [[VZMacHardwareModel alloc]
            initWithDataRepresentation:hwModelData];
        VZMacMachineIdentifier *machineId = [[VZMacMachineIdentifier alloc]
            initWithDataRepresentation:machineIdData];

        NSError *err = nil;
        VZMacAuxiliaryStorage *auxStorage = [[VZMacAuxiliaryStorage alloc]
            initWithURL:[NSURL fileURLWithPath:auxPath]];

        VZMacPlatformConfiguration *platform = [[VZMacPlatformConfiguration alloc] init];
        platform.hardwareModel = hardwareModel;
        platform.machineIdentifier = machineId;
        platform.auxiliaryStorage = auxStorage;

        VZMacOSBootLoader *bootLoader = [[VZMacOSBootLoader alloc] init];

        int cpuCount = physical_core_count();
        if (cpuCount < (int)VZVirtualMachineConfiguration.minimumAllowedCPUCount) {
            cpuCount = (int)VZVirtualMachineConfiguration.minimumAllowedCPUCount;
        }

        uint64_t ramBytes = host_ram_bytes() / 2;
        uint64_t maxRAM = 16ULL * 1024 * 1024 * 1024;
        if (ramBytes > maxRAM) ramBytes = maxRAM;
        uint64_t minRAM = VZVirtualMachineConfiguration.minimumAllowedMemorySize;
        if (ramBytes < minRAM) ramBytes = minRAM;

        VZMacGraphicsDeviceConfiguration *gpuDev = [[VZMacGraphicsDeviceConfiguration alloc] init];
        VZMacGraphicsDisplayConfiguration *display = [[VZMacGraphicsDisplayConfiguration alloc]
            initWithWidthInPixels:1920 heightInPixels:1080 pixelsPerInch:72];
        gpuDev.displays = @[display];

        VZDiskImageStorageDeviceAttachment *diskAttachment = [[VZDiskImageStorageDeviceAttachment alloc]
            initWithURL:[NSURL fileURLWithPath:diskPath] readOnly:NO error:&err];
        if (err) {
            NSLog(@"vm_install: disk attachment error: %@", err);
            return -1;
        }
        VZVirtioBlockDeviceConfiguration *disk = [[VZVirtioBlockDeviceConfiguration alloc]
            initWithAttachment:diskAttachment];

        VZVirtioNetworkDeviceConfiguration *net = [[VZVirtioNetworkDeviceConfiguration alloc] init];
        net.attachment = [[VZNATNetworkDeviceAttachment alloc] init];

        VZUSBKeyboardConfiguration *keyboard = [[VZUSBKeyboardConfiguration alloc] init];
        VZUSBScreenCoordinatePointingDeviceConfiguration *pointing =
            [[VZUSBScreenCoordinatePointingDeviceConfiguration alloc] init];

        VZVirtualMachineConfiguration *config = [[VZVirtualMachineConfiguration alloc] init];
        config.platform = platform;
        config.bootLoader = bootLoader;
        config.CPUCount = cpuCount;
        config.memorySize = ramBytes;
        config.graphicsDevices = @[gpuDev];
        config.storageDevices = @[disk];
        config.networkDevices = @[net];
        config.keyboards = @[keyboard];
        config.pointingDevices = @[pointing];

        [config validateWithError:&err];
        if (err) {
            NSLog(@"vm_install: config validation error: %@", err);
            return -1;
        }

        // Create VM and install (must be on main thread)
        __block int result = 0;
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);

        void (^installBlock)(void) = ^{
            VZVirtualMachine *vm = [[VZVirtualMachine alloc] initWithConfiguration:config];

            VZMacOSInstaller *installer = [[VZMacOSInstaller alloc]
                initWithVirtualMachine:vm restoreImageURL:[NSURL fileURLWithPath:ipswPath]];

            NSTimer *progressTimer = nil;
            if (progress) {
                NSProgress *installProgress = installer.progress;
                progressTimer = [NSTimer scheduledTimerWithTimeInterval:0.5 repeats:YES
                    block:^(NSTimer *t) {
                        progress(installProgress.fractionCompleted);
                    }];
            }

            [installer installWithCompletionHandler:^(NSError *error) {
                if (progressTimer) [progressTimer invalidate];
                if (error) {
                    NSLog(@"vm_install: install error: %@", error);
                    result = -1;
                } else {
                    NSLog(@"vm_install: macOS installed successfully");
                }
                dispatch_semaphore_signal(sem);
            }];
        };

        if ([NSThread isMainThread]) {
            installBlock();
        } else {
            dispatch_async(dispatch_get_main_queue(), installBlock);
        }

        dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
        return result;
    }
}
