// GeneralsX @feature Codex 01/08/2026 Present native macOS SFX extraction progress without linking AppKit into the Go launcher.

#import <AppKit/AppKit.h>
#import <Foundation/Foundation.h>

#include <stdint.h>
#include <stdio.h>
#include <string.h>

static NSString *const GXProgressErrorDomain = @"com.generalsx.generalsxzh.sfx.progress";

@interface GXProgressEvent : NSObject

@property(nonatomic, copy, nullable) NSString *message;
@property(nonatomic) int64_t completed;
@property(nonatomic) int64_t total;
@property(nonatomic) BOOL hasCompleted;
@property(nonatomic) BOOL hasTotal;
@property(nonatomic) BOOL indeterminate;
@property(nonatomic) BOOL done;

@end

@implementation GXProgressEvent
@end

static NSError *GXProgressError(NSString *description) {
    return [NSError errorWithDomain:GXProgressErrorDomain
                               code:1
                           userInfo:@{NSLocalizedDescriptionKey : description}];
}

static BOOL GXReadBoolean(NSDictionary<NSString *, id> *object,
                          NSString *key,
                          BOOL *value,
                          NSError **error) {
    id candidate = object[key];
    if (candidate == nil) {
        return YES;
    }
    if (![candidate isKindOfClass:[NSNumber class]]) {
        if (error != NULL) {
            *error = GXProgressError([NSString stringWithFormat:@"%@ must be a boolean", key]);
        }
        return NO;
    }
    *value = [candidate boolValue];
    return YES;
}

static BOOL GXReadInteger(NSDictionary<NSString *, id> *object,
                          NSString *key,
                          int64_t *value,
                          BOOL *present,
                          NSError **error) {
    id candidate = object[key];
    if (candidate == nil) {
        return YES;
    }
    if (![candidate isKindOfClass:[NSNumber class]]) {
        if (error != NULL) {
            *error = GXProgressError([NSString stringWithFormat:@"%@ must be an integer", key]);
        }
        return NO;
    }
    *value = [candidate longLongValue];
    *present = YES;
    return YES;
}

static GXProgressEvent *_Nullable GXParseProgressEvent(NSData *line, NSError **error) {
    NSError *jsonError = nil;
    id value = [NSJSONSerialization JSONObjectWithData:line options:0 error:&jsonError];
    if (value == nil) {
        if (error != NULL) {
            *error = jsonError;
        }
        return nil;
    }
    if (![value isKindOfClass:[NSDictionary class]]) {
        if (error != NULL) {
            *error = GXProgressError(@"progress event must be a JSON object");
        }
        return nil;
    }

    NSDictionary<NSString *, id> *object = value;
    GXProgressEvent *event = [[GXProgressEvent alloc] init];
    id message = object[@"message"];
    if (message != nil) {
        if (![message isKindOfClass:[NSString class]]) {
            if (error != NULL) {
                *error = GXProgressError(@"message must be a string");
            }
            return nil;
        }
        event.message = message;
    }
    int64_t completed = 0;
    int64_t total = 0;
    BOOL hasCompleted = NO;
    BOOL hasTotal = NO;
    BOOL indeterminate = NO;
    BOOL done = NO;
    if (!GXReadInteger(object, @"completed", &completed, &hasCompleted, error) ||
        !GXReadInteger(object, @"total", &total, &hasTotal, error) ||
        !GXReadBoolean(object, @"indeterminate", &indeterminate, error) ||
        !GXReadBoolean(object, @"done", &done, error)) {
        return nil;
    }
    event.completed = completed;
    event.total = total;
    event.hasCompleted = hasCompleted;
    event.hasTotal = hasTotal;
    event.indeterminate = indeterminate;
    event.done = done;
    return event;
}

@interface GXProgressController : NSObject <NSApplicationDelegate>

@property(nonatomic, strong) NSWindow *window;
@property(nonatomic, strong) NSTextField *statusLabel;
@property(nonatomic, strong) NSProgressIndicator *progressIndicator;
@property(nonatomic, strong) NSTextField *percentageLabel;

@end


@implementation GXProgressController

- (void)applicationDidFinishLaunching:(NSNotification *)notification {
    (void)notification;
    [self createWindow];
    [self startReadingStandardInput];
}

- (BOOL)applicationShouldTerminateAfterLastWindowClosed:(NSApplication *)sender {
    (void)sender;
    return NO;
}

- (void)createWindow {
    const NSRect frame = NSMakeRect(0.0, 0.0, 460.0, 172.0);
    const NSWindowStyleMask style = NSWindowStyleMaskTitled | NSWindowStyleMaskFullSizeContentView;
    self.window = [[NSWindow alloc] initWithContentRect:frame
                                              styleMask:style
                                                backing:NSBackingStoreBuffered
                                                  defer:NO];
    self.window.title = @"Preparing GeneralsX Zero Hour";
    self.window.titleVisibility = NSWindowTitleHidden;
    self.window.titlebarAppearsTransparent = YES;
    self.window.movableByWindowBackground = YES;
    self.window.releasedWhenClosed = NO;
    self.window.tabbingMode = NSWindowTabbingModeDisallowed;
    self.window.animationBehavior = NSWindowAnimationBehaviorAlertPanel;
    self.window.collectionBehavior = NSWindowCollectionBehaviorMoveToActiveSpace |
                                     NSWindowCollectionBehaviorFullScreenAuxiliary;
    self.window.level = NSFloatingWindowLevel;
    self.window.accessibilityLabel = @"GeneralsX extraction progress";
    self.window.accessibilityIdentifier = @"generalsx-sfx-progress-window";
    [self.window standardWindowButton:NSWindowCloseButton].hidden = YES;
    [self.window standardWindowButton:NSWindowMiniaturizeButton].hidden = YES;
    [self.window standardWindowButton:NSWindowZoomButton].hidden = YES;

    NSVisualEffectView *background = [[NSVisualEffectView alloc] initWithFrame:frame];
    background.material = NSVisualEffectMaterialWindowBackground;
    background.blendingMode = NSVisualEffectBlendingModeBehindWindow;
    background.state = NSVisualEffectStateActive;
    background.translatesAutoresizingMaskIntoConstraints = NO;
    self.window.contentView = background;

    self.statusLabel = [NSTextField labelWithString:@"Preparing game files…"];
    self.statusLabel.font = [NSFont systemFontOfSize:15.0 weight:NSFontWeightSemibold];
    self.statusLabel.textColor = NSColor.labelColor;
    self.statusLabel.alignment = NSTextAlignmentCenter;
    self.statusLabel.lineBreakMode = NSLineBreakByTruncatingTail;
    self.statusLabel.translatesAutoresizingMaskIntoConstraints = NO;
    self.statusLabel.accessibilityLabel = @"Extraction status";
    self.statusLabel.accessibilityIdentifier = @"generalsx-sfx-progress-status";

    self.progressIndicator = [[NSProgressIndicator alloc] initWithFrame:NSZeroRect];
    self.progressIndicator.style = NSProgressIndicatorStyleBar;
    self.progressIndicator.controlSize = NSControlSizeRegular;
    self.progressIndicator.minValue = 0.0;
    self.progressIndicator.maxValue = 1.0;
    self.progressIndicator.indeterminate = YES;
    self.progressIndicator.usesThreadedAnimation = YES;
    self.progressIndicator.translatesAutoresizingMaskIntoConstraints = NO;
    self.progressIndicator.accessibilityLabel = @"Extraction progress";
    self.progressIndicator.accessibilityIdentifier = @"generalsx-sfx-progress-indicator";
    [self.progressIndicator startAnimation:nil];

    self.percentageLabel = [NSTextField labelWithString:@"Working…"];
    self.percentageLabel.font = [NSFont monospacedDigitSystemFontOfSize:13.0
                                                                weight:NSFontWeightMedium];
    self.percentageLabel.textColor = NSColor.secondaryLabelColor;
    self.percentageLabel.alignment = NSTextAlignmentCenter;
    self.percentageLabel.translatesAutoresizingMaskIntoConstraints = NO;
    self.percentageLabel.accessibilityLabel = @"Extraction percentage";
    self.percentageLabel.accessibilityIdentifier = @"generalsx-sfx-progress-percentage";

    [background addSubview:self.statusLabel];
    [background addSubview:self.progressIndicator];
    [background addSubview:self.percentageLabel];
    [NSLayoutConstraint activateConstraints:@[
        [self.statusLabel.topAnchor constraintEqualToAnchor:background.topAnchor constant:42.0],
        [self.statusLabel.leadingAnchor constraintEqualToAnchor:background.leadingAnchor constant:32.0],
        [self.statusLabel.trailingAnchor constraintEqualToAnchor:background.trailingAnchor constant:-32.0],
        [self.progressIndicator.topAnchor constraintEqualToAnchor:self.statusLabel.bottomAnchor constant:24.0],
        [self.progressIndicator.leadingAnchor constraintEqualToAnchor:background.leadingAnchor constant:44.0],
        [self.progressIndicator.trailingAnchor constraintEqualToAnchor:background.trailingAnchor constant:-44.0],
        [self.progressIndicator.heightAnchor constraintEqualToConstant:12.0],
        [self.percentageLabel.topAnchor constraintEqualToAnchor:self.progressIndicator.bottomAnchor constant:14.0],
        [self.percentageLabel.centerXAnchor constraintEqualToAnchor:background.centerXAnchor],
    ]];

    [self.window center];
    [self.window makeKeyAndOrderFront:nil];
    [NSApp activate];
}

- (void)applyEvent:(GXProgressEvent *)event {
    if (event.message.length > 0) {
        self.statusLabel.stringValue = event.message;
    }

    if (event.done) {
        [self.progressIndicator stopAnimation:nil];
        self.progressIndicator.indeterminate = NO;
        self.progressIndicator.minValue = 0.0;
        self.progressIndicator.maxValue = 1.0;
        self.progressIndicator.doubleValue = 1.0;
        self.percentageLabel.stringValue = @"100%";
        if (event.message.length == 0) {
            self.statusLabel.stringValue = @"Ready to launch…";
        }
        return;
    }

    const BOOL determinate = !event.indeterminate && event.hasCompleted && event.hasTotal && event.total > 0;
    if (!determinate) {
        self.progressIndicator.indeterminate = YES;
        [self.progressIndicator startAnimation:nil];
        self.percentageLabel.stringValue = @"Working…";
        return;
    }

    int64_t boundedCompleted = event.completed;
    if (boundedCompleted < 0) {
        boundedCompleted = 0;
    } else if (boundedCompleted > event.total) {
        boundedCompleted = event.total;
    }
    const double fraction = (double)boundedCompleted / (double)event.total;
    [self.progressIndicator stopAnimation:nil];
    self.progressIndicator.indeterminate = NO;
    self.progressIndicator.minValue = 0.0;
    self.progressIndicator.maxValue = 1.0;
    self.progressIndicator.doubleValue = fraction;
    const NSInteger percentage = (NSInteger)(fraction * 100.0);
    self.percentageLabel.stringValue = [NSString stringWithFormat:@"%ld%%", (long)percentage];
}

- (void)processLine:(NSData *)line {
    if (line.length == 0) {
        return;
    }
    NSError *error = nil;
    GXProgressEvent *event = GXParseProgressEvent(line, &error);
    if (event == nil) {
        return;
    }
    dispatch_async(dispatch_get_main_queue(), ^{
        [self applyEvent:event];
    });
}

- (void)startReadingStandardInput {
    __weak GXProgressController *weakSelf = self;
    dispatch_async(dispatch_get_global_queue(QOS_CLASS_UTILITY, 0), ^{
        NSFileHandle *input = NSFileHandle.fileHandleWithStandardInput;
        NSMutableData *pending = [[NSMutableData alloc] init];

        for (;;) {
            @autoreleasepool {
                NSData *chunk = input.availableData;
                if (chunk.length == 0) {
                    break;
                }
                [pending appendData:chunk];

                const uint8_t *bytes = pending.bytes;
                NSUInteger consumed = 0;
                for (NSUInteger index = 0; index < pending.length; ++index) {
                    if (bytes[index] != '\n') {
                        continue;
                    }
                    NSRange range = NSMakeRange(consumed, index - consumed);
                    [weakSelf processLine:[pending subdataWithRange:range]];
                    consumed = index + 1;
                }
                if (consumed > 0) {
                    [pending replaceBytesInRange:NSMakeRange(0, consumed) withBytes:NULL length:0];
                }
            }
        }

        if (pending.length > 0) {
            [weakSelf processLine:pending];
        }
        dispatch_async(dispatch_get_main_queue(), ^{
            [NSApp terminate:nil];
        });
    });
}

@end


static BOOL GXSelfTestCondition(BOOL condition, NSString *message) {
    if (!condition) {
        fprintf(stderr, "GeneralsX-SFX-Progress self-test failed: %s\n", message.UTF8String);
    }
    return condition;
}

static GXProgressEvent *_Nullable GXSelfTestParse(NSString *json) {
    NSError *error = nil;
    GXProgressEvent *event = GXParseProgressEvent([json dataUsingEncoding:NSUTF8StringEncoding], &error);
    if (event == nil) {
        fprintf(stderr,
                "GeneralsX-SFX-Progress self-test parse failed: %s\n",
                error.localizedDescription.UTF8String);
    }
    return event;
}

static int GXRunSelfTest(void) {
    GXProgressEvent *indeterminate = GXSelfTestParse(
        @"{\"message\":\"Authenticating payload…\",\"indeterminate\":true}");
    if (!GXSelfTestCondition(indeterminate != nil, @"parse indeterminate event") ||
        !GXSelfTestCondition(indeterminate.indeterminate, @"read indeterminate flag") ||
        !GXSelfTestCondition([indeterminate.message isEqualToString:@"Authenticating payload…"],
                             @"read status message")) {
        return 1;
    }

    GXProgressEvent *determinate = GXSelfTestParse(
        @"{\"message\":\"Extracting game files…\",\"completed\":123,\"total\":456}");
    if (!GXSelfTestCondition(determinate != nil, @"parse determinate event") ||
        !GXSelfTestCondition(determinate.hasCompleted && determinate.completed == 123,
                             @"read completed bytes") ||
        !GXSelfTestCondition(determinate.hasTotal && determinate.total == 456,
                             @"read total bytes")) {
        return 1;
    }

    GXProgressEvent *done = GXSelfTestParse(@"{\"done\":true}");
    if (!GXSelfTestCondition(done != nil && done.done, @"read completion event")) {
        return 1;
    }

    NSError *error = nil;
    GXProgressEvent *invalid = GXParseProgressEvent(
        [@"{\"completed\":\"not-a-number\"}" dataUsingEncoding:NSUTF8StringEncoding],
        &error);
    if (!GXSelfTestCondition(invalid == nil && error != nil, @"reject invalid field types")) {
        return 1;
    }

    puts("GeneralsX-SFX-Progress self-test passed.");
    return 0;
}

int main(int argc, const char *argv[]) {
    @autoreleasepool {
        if (argc == 2 && strcmp(argv[1], "--self-test") == 0) {
            return GXRunSelfTest();
        }
        if (argc != 1) {
            fprintf(stderr, "Usage: GeneralsX-SFX-Progress [--self-test]\n");
            return 2;
        }

        NSApplication *application = NSApplication.sharedApplication;
        application.activationPolicy = NSApplicationActivationPolicyAccessory;
        GXProgressController *controller = [[GXProgressController alloc] init];
        application.delegate = controller;
        [application run];
    }
    return 0;
}
