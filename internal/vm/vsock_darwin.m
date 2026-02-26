//go:build darwin

#import <Virtualization/Virtualization.h>
#include <stdint.h>
#include <dispatch/dispatch.h>

// Go callback declared in vsock_darwin.go
extern void vsock_go_accepted(int fd, uint32_t port);

// ---- Vsock Listener Delegate ----

@interface VsockListenerDelegate : NSObject <VZVirtioSocketListenerDelegate>
@property (nonatomic, strong) NSMutableArray *connections;
@end

@implementation VsockListenerDelegate

- (instancetype)init {
    self = [super init];
    if (self) {
        _connections = [[NSMutableArray alloc] init];
    }
    return self;
}

- (BOOL)listener:(VZVirtioSocketListener *)listener
    shouldAcceptNewConnection:(VZVirtioSocketConnection *)connection
    fromSocketDevice:(VZVirtioSocketDevice *)socketDevice {

    // Retain the connection to keep the fd alive
    [self.connections addObject:connection];

    int fd = (int)connection.fileDescriptor;
    uint32_t port = (uint32_t)connection.destinationPort;
    vsock_go_accepted(fd, port);
    return YES;
}

@end

// ---- Static state ----

static VsockListenerDelegate *g_delegate = nil;

// ---- C API ----

int vm_vsock_listen(void *vm_ptr, uint32_t port) {
    __block int result = 0;

    void (^block)(void) = ^{
        @autoreleasepool {
            VZVirtualMachine *vm = (__bridge VZVirtualMachine *)vm_ptr;

            NSArray<VZSocketDevice *> *socketDevices = vm.socketDevices;
            if (socketDevices.count == 0) {
                NSLog(@"vm_vsock_listen: no socket devices on VM");
                result = -1;
                return;
            }

            VZVirtioSocketDevice *vsock = (VZVirtioSocketDevice *)socketDevices.firstObject;

            // Lazy-init shared delegate
            if (!g_delegate) {
                g_delegate = [[VsockListenerDelegate alloc] init];
            }

            VZVirtioSocketListener *listener = [[VZVirtioSocketListener alloc] init];
            listener.delegate = g_delegate;

            [vsock setSocketListener:listener forPort:port];
            NSLog(@"vm_vsock_listen: listening on port %u", port);
        }
    };

    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_sync(dispatch_get_main_queue(), block);
    }

    return result;
}

void vm_vsock_stop(void *vm_ptr, uint32_t port) {
    if (!vm_ptr) return;

    void (^block)(void) = ^{
        @autoreleasepool {
            VZVirtualMachine *vm = (__bridge VZVirtualMachine *)vm_ptr;

            NSArray<VZSocketDevice *> *socketDevices = vm.socketDevices;
            if (socketDevices.count > 0) {
                VZVirtioSocketDevice *vsock = (VZVirtioSocketDevice *)socketDevices.firstObject;
                [vsock removeSocketListenerForPort:port];
            }
        }
    };

    if ([NSThread isMainThread]) {
        block();
    } else {
        dispatch_sync(dispatch_get_main_queue(), block);
    }
}
