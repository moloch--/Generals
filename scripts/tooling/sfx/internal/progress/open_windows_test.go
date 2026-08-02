//go:build windows

// GeneralsX @bugfix Codex 02/08/2026 Verify standalone Windows launchers route extraction events to their native presenter.
package progress

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
	"unsafe"
)

func TestOpenWindowsProgressRoutesReporterEvents(t *testing.T) {
	received := make(chan []event, 1)
	reporter := openWindowsProgress(func(updates <-chan []byte) {
		var events []event
		for encoded := range updates {
			var update event
			if err := json.Unmarshal(bytes.TrimSpace(encoded), &update); err != nil {
				t.Errorf("decode Windows progress update: %v", err)
				continue
			}
			events = append(events, update)
		}
		received <- events
	})

	reporter.Indeterminate("Checking package...")
	reporter.Update("Extracting files...", 1, 2)
	reporter.Complete()
	reporter.Close()

	select {
	case events := <-received:
		if len(events) != 3 {
			t.Fatalf("Windows progress events = %#v, want three events", events)
		}
		if !events[0].Indeterminate || events[0].Message != "Checking package..." {
			t.Fatalf("initial Windows progress event = %#v", events[0])
		}
		if events[1].Completed != 1 || events[1].Total != 2 || events[1].Message != "Extracting files..." {
			t.Fatalf("determinate Windows progress event = %#v", events[1])
		}
		if !events[2].Done {
			t.Fatalf("completion Windows progress event = %#v", events[2])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Windows progress presenter did not finish after Reporter.Close")
	}
}

func TestOpenWindowsProgressNeverBlocksOnStalledPresenter(t *testing.T) {
	release := make(chan struct{})
	reporter := openWindowsProgress(func(<-chan []byte) {
		<-release
	})

	finished := make(chan struct{})
	go func() {
		for completed := int64(0); completed <= 1000; completed++ {
			reporter.Update("Extracting files...", completed, 1000)
		}
		reporter.Close()
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled Windows progress presenter blocked extraction updates")
	}
	close(release)
}

func TestWindowsProgressIndeterminateTicker(t *testing.T) {
	window := windowsProgressWindow{indeterminate: true}
	window.tick()
	if window.indeterminatePosition != 20 {
		t.Fatalf("first indeterminate position = %d, want 20", window.indeterminatePosition)
	}
	window.indeterminatePosition = windowsProgressScale
	window.tick()
	if window.indeterminatePosition != 0 {
		t.Fatalf("wrapped indeterminate position = %d, want 0", window.indeterminatePosition)
	}
	window.indeterminate = false
	window.tick()
	if window.indeterminatePosition != 0 {
		t.Fatalf("determinate ticker changed position to %d", window.indeterminatePosition)
	}
}

// GeneralsX @bugfix Codex 02/08/2026 Provide an opt-in native window smoke test for interactive Windows validation.
func TestWindowsProgressVisualSmoke(t *testing.T) {
	if os.Getenv("GENERALSX_SFX_PROGRESS_VISUAL_TEST") != "1" {
		t.Skip("set GENERALSX_SFX_PROGRESS_VISUAL_TEST=1 on an interactive Windows desktop")
	}

	findWindowW := user32.NewProc("FindWindowW")
	className := utf16Pointer("GeneralsXSFXProgressWindow")
	reporter := Open()
	reporter.Indeterminate("Checking game package...")
	windowDeadline := time.Now().Add(2 * time.Second)
	var window uintptr
	for window == 0 && time.Now().Before(windowDeadline) {
		window, _, _ = findWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
		if window == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if window == 0 {
		reporter.Close()
		t.Fatal("native Windows progress window did not appear")
	}

	time.Sleep(750 * time.Millisecond)
	for completed := int64(0); completed <= 100; completed += 2 {
		reporter.Update("Extracting game files...", completed, 100)
		time.Sleep(20 * time.Millisecond)
	}
	reporter.Indeterminate("Verifying game files...")
	time.Sleep(750 * time.Millisecond)
	reporter.Complete()
	time.Sleep(300 * time.Millisecond)
	reporter.Close()

	closeDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(closeDeadline) {
		window, _, _ = findWindowW.Call(uintptr(unsafe.Pointer(className)), 0)
		if window == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("native Windows progress window remained after Reporter.Close")
}
