//go:build darwin

#import <Cocoa/Cocoa.h>
#import <Virtualization/Virtualization.h>
#include <dispatch/dispatch.h>
#include <objc/runtime.h>
#include <pthread.h>

static int _vm_buttons_down = 0;  // bitmask of held buttons


void vm_input_key(void *view, int keycode, int press, const char *chars) {
    dispatch_async(dispatch_get_main_queue(), ^{
        @autoreleasepool {
            VZVirtualMachineView *vmView = (__bridge VZVirtualMachineView *)view;
            NSWindow *window = vmView.window;
            if (!window) return;

            // Ensure VM view is the key responder for keyboard delivery.
            [window makeFirstResponder:vmView];
            [window makeKeyWindow];

            NSString *characters = @"";
            if (chars && chars[0] != '\0') {
                characters = [NSString stringWithUTF8String:chars];
                if (!characters) characters = @"";
            }

            NSEventType type = press ? NSEventTypeKeyDown : NSEventTypeKeyUp;
            NSEvent *event = [NSEvent keyEventWithType:type
                location:NSZeroPoint
                modifierFlags:0
                timestamp:[[NSProcessInfo processInfo] systemUptime]
                windowNumber:[window windowNumber]
                context:nil
                characters:characters
                charactersIgnoringModifiers:characters
                isARepeat:NO
                keyCode:(unsigned short)keycode];

            if (press) {
                [vmView keyDown:event];
            } else {
                [vmView keyUp:event];
            }
        }
    });
}

void vm_input_mouse_move(void *view, double x, double y) {
    dispatch_async(dispatch_get_main_queue(), ^{
        @autoreleasepool {
            VZVirtualMachineView *vmView = (__bridge VZVirtualMachineView *)view;
            NSWindow *window = vmView.window;
            if (!window) return;

            // Convert from top-left origin (web) to bottom-left origin (AppKit)
            NSRect frame = vmView.frame;
            NSPoint point = NSMakePoint(x, frame.size.height - y);

            NSEventType type;
            if (_vm_buttons_down & 1) {
                type = NSEventTypeLeftMouseDragged;
            } else if (_vm_buttons_down & 4) {
                type = NSEventTypeRightMouseDragged;
            } else if (_vm_buttons_down & 2) {
                type = NSEventTypeOtherMouseDragged;
            } else {
                type = NSEventTypeMouseMoved;
            }

            NSEvent *event = [NSEvent mouseEventWithType:type
                location:point
                modifierFlags:0
                timestamp:[[NSProcessInfo processInfo] systemUptime]
                windowNumber:[window windowNumber]
                context:nil
                eventNumber:0
                clickCount:0
                pressure:(_vm_buttons_down ? 1.0 : 0.0)];

            switch (type) {
                case NSEventTypeLeftMouseDragged:  [vmView mouseDragged:event]; break;
                case NSEventTypeRightMouseDragged: [vmView rightMouseDragged:event]; break;
                case NSEventTypeOtherMouseDragged: [vmView otherMouseDragged:event]; break;
                default:                           [vmView mouseMoved:event]; break;
            }
        }
    });
}

void vm_input_mouse_button(void *view, int button, int press, double x, double y) {
    dispatch_async(dispatch_get_main_queue(), ^{
        @autoreleasepool {
            VZVirtualMachineView *vmView = (__bridge VZVirtualMachineView *)view;
            NSWindow *window = vmView.window;
            if (!window) return;

            NSRect frame = vmView.frame;
            NSPoint point = NSMakePoint(x, frame.size.height - y);

            NSEventType type;
            int mask;
            if (button == 0) {
                type = press ? NSEventTypeLeftMouseDown : NSEventTypeLeftMouseUp;
                mask = 1;
            } else if (button == 2) {
                type = press ? NSEventTypeRightMouseDown : NSEventTypeRightMouseUp;
                mask = 4;
            } else {
                type = press ? NSEventTypeOtherMouseDown : NSEventTypeOtherMouseUp;
                mask = 2;
            }

            if (press) {
                _vm_buttons_down |= mask;
            } else {
                _vm_buttons_down &= ~mask;
            }

            NSEvent *event = [NSEvent mouseEventWithType:type
                location:point
                modifierFlags:0
                timestamp:[[NSProcessInfo processInfo] systemUptime]
                windowNumber:[window windowNumber]
                context:nil
                eventNumber:0
                clickCount:1
                pressure:press ? 1.0 : 0.0];

            switch (type) {
                case NSEventTypeLeftMouseDown:   [vmView mouseDown:event]; break;
                case NSEventTypeLeftMouseUp:     [vmView mouseUp:event]; break;
                case NSEventTypeRightMouseDown:  [vmView rightMouseDown:event]; break;
                case NSEventTypeRightMouseUp:    [vmView rightMouseUp:event]; break;
                case NSEventTypeOtherMouseDown:  [vmView otherMouseDown:event]; break;
                case NSEventTypeOtherMouseUp:    [vmView otherMouseUp:event]; break;
                default: break;
            }
        }
    });
}

void vm_input_scroll(void *view, double dx, double dy, double x, double y) {
    dispatch_async(dispatch_get_main_queue(), ^{
        @autoreleasepool {
            VZVirtualMachineView *vmView = (__bridge VZVirtualMachineView *)view;
            NSWindow *window = vmView.window;
            if (!window) return;

            NSRect frame = vmView.frame;
            NSPoint point = NSMakePoint(x, frame.size.height - y);

            // Create scroll wheel event using CGEvent and convert to NSEvent
            CGEventRef cgEvent = CGEventCreateScrollWheelEvent(NULL,
                kCGScrollEventUnitPixel, 2, (int32_t)(-dy), (int32_t)(-dx));
            if (!cgEvent) return;

            CGEventSetLocation(cgEvent, CGPointMake(x, y));

            NSEvent *event = [NSEvent eventWithCGEvent:cgEvent];
            CFRelease(cgEvent);

            if (event) {
                [vmView scrollWheel:event];
            }
        }
    });
}
