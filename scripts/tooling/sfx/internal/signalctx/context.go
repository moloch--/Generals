// Package signalctx creates cancellation contexts for host termination signals.
package signalctx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
)

type signalCause struct {
	signal os.Signal
}

func (cause *signalCause) Error() string {
	return fmt.Sprintf("received signal %v", cause.signal)
}

// NotifyContext returns a context canceled by an interactive interrupt and,
// on Unix hosts, SIGTERM. The cancellation cause retains the exact signal so
// callers can forward it and preserve the conventional process exit status.
// The stop function releases installed handlers.
func NotifyContext(parent context.Context) (context.Context, context.CancelFunc) {
	signalChannel := make(chan os.Signal, 1)
	signal.Notify(signalChannel, platformSignals()...)
	return notifyContext(parent, signalChannel, func() {
		signal.Stop(signalChannel)
	})
}

func notifyContext(
	parent context.Context,
	signalChannel <-chan os.Signal,
	stopSignals func(),
) (context.Context, context.CancelFunc) {
	ctx, cancelCause := context.WithCancelCause(parent)
	stopped := make(chan struct{})
	var stopOnce sync.Once

	go func() {
		select {
		case received := <-signalChannel:
			cancelCause(&signalCause{signal: received})
		case <-parent.Done():
			cancelCause(context.Cause(parent))
		case <-stopped:
		}
	}()

	stop := func() {
		stopOnce.Do(func() {
			stopSignals()
			close(stopped)
			cancelCause(context.Canceled)
		})
	}
	return ctx, stop
}

// ReceivedSignal returns the host signal that canceled a NotifyContext.
func ReceivedSignal(ctx context.Context) (os.Signal, bool) {
	if ctx == nil {
		return nil, false
	}
	var cause *signalCause
	if !errors.As(context.Cause(ctx), &cause) || cause.signal == nil {
		return nil, false
	}
	return cause.signal, true
}

// ExitCode returns the conventional wrapper status for a captured signal.
func ExitCode(ctx context.Context) (int, bool) {
	received, ok := ReceivedSignal(ctx)
	if !ok {
		return 0, false
	}
	return platformSignalExitCode(received)
}
