#import <Cocoa/Cocoa.h>
#include <stdlib.h>
#include <string.h>

static NSPasteboard *pasteboard = nil;
static NSInteger lastChangeCount = 0;

void clip_init(void) {
	@autoreleasepool {
		pasteboard = [NSPasteboard generalPasteboard];
		lastChangeCount = [pasteboard changeCount];
	}
}

void clip_set(const char *text, int len) {
	@autoreleasepool {
		NSString *str = [[NSString alloc] initWithBytes:text
			length:len encoding:NSUTF8StringEncoding];
		[pasteboard clearContents];
		[pasteboard setString:str forType:NSPasteboardTypeString];
		lastChangeCount = [pasteboard changeCount];
	}
}

// Returns 1 if clipboard changed (new text in out_text/out_len), 0 otherwise.
// Caller must free *out_text.
int clip_check(char **out_text, int *out_len) {
	@autoreleasepool {
		NSInteger current = [pasteboard changeCount];
		if (current == lastChangeCount) return 0;
		lastChangeCount = current;

		NSString *str = [pasteboard stringForType:NSPasteboardTypeString];
		if (!str) return 0;

		const char *utf8 = [str UTF8String];
		int slen = (int)strlen(utf8);
		*out_text = (char*)malloc(slen + 1);
		memcpy(*out_text, utf8, slen + 1);
		*out_len = slen;
		return 1;
	}
}

void clip_destroy(void) {
	// NSPasteboard is a singleton, no cleanup needed
}
