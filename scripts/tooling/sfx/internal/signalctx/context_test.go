package signalctx

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotifyContextRecordsInterrupt(t *testing.T) {
	signals := make(chan os.Signal, 1)
	var stopCalls atomic.Int32
	ctx, stop := notifyContext(context.Background(), signals, func() {
		stopCalls.Add(1)
	})
	t.Cleanup(stop)

	signals <- os.Interrupt
	waitForCancellation(t, ctx)

	received, ok := ReceivedSignal(ctx)
	if !ok || received != os.Interrupt {
		t.Fatalf("ReceivedSignal() = (%v, %v), want (%v, true)", received, ok, os.Interrupt)
	}
	if code, ok := ExitCode(ctx); !ok || code != 130 {
		t.Fatalf("ExitCode() = (%d, %v), want (130, true)", code, ok)
	}

	stop()
	stop()
	if calls := stopCalls.Load(); calls != 1 {
		t.Fatalf("stop signal handler calls = %d, want 1", calls)
	}
}

func TestNotifyContextPreservesParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	signals := make(chan os.Signal, 1)
	ctx, stop := notifyContext(parent, signals, func() {})
	t.Cleanup(stop)

	cancelParent()
	waitForCancellation(t, ctx)

	if received, ok := ReceivedSignal(ctx); ok {
		t.Fatalf("ReceivedSignal() = (%v, true), want no signal", received)
	}
	if code, ok := ExitCode(ctx); ok {
		t.Fatalf("ExitCode() = (%d, true), want no signal code", code)
	}
}

func TestNotifyContextStopCancelsWithoutSignal(t *testing.T) {
	signals := make(chan os.Signal, 1)
	ctx, stop := notifyContext(context.Background(), signals, func() {})
	stop()
	waitForCancellation(t, ctx)

	if received, ok := ReceivedSignal(ctx); ok {
		t.Fatalf("ReceivedSignal() = (%v, true), want no signal", received)
	}
}

func waitForCancellation(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("context was not canceled")
	}
}
