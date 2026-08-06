//go:build windows

// GeneralsX @bugfix Codex 02/08/2026 Restore visible cold-cache extraction progress in standalone Windows launchers.
package progress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	windowsProgressQueueSize = 16
	windowsProgressScale     = 1000

	classAlreadyExists = syscall.Errno(1410)

	colorWindow       = 5
	defaultGUIFont    = 17
	generalsXIcon     = 1
	idcArrow          = 32512
	idiApplication    = 32512
	pmRemove          = 0x0001
	swShowNormal      = 1
	wmSetFont         = 0x0030
	wmUser            = 0x0400
	wsCaption         = 0x00C00000
	wsChild           = 0x40000000
	wsExDlgModalFrame = 0x00000001
	wsExTopmost       = 0x00000008
	wsMinimizeBox     = 0x00020000
	wsSysMenu         = 0x00080000
	wsVisible         = 0x10000000

	pbsSmooth     = 0x00000001
	pbmSetPos     = wmUser + 2
	pbmSetRange32 = wmUser + 6
)

var (
	// GeneralsX @bugfix Codex 02/08/2026 Restrict standalone UI dependencies to the trusted system directory.
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	comctl32 = windows.NewLazySystemDLL("comctl32.dll")

	getModuleHandleW   = kernel32.NewProc("GetModuleHandleW")
	getStockObject     = gdi32.NewProc("GetStockObject")
	initCommonControls = comctl32.NewProc("InitCommonControlsEx")
	createWindowExW    = user32.NewProc("CreateWindowExW")
	defWindowProcW     = user32.NewProc("DefWindowProcW")
	destroyWindow      = user32.NewProc("DestroyWindow")
	dispatchMessageW   = user32.NewProc("DispatchMessageW")
	getSystemMetrics   = user32.NewProc("GetSystemMetrics")
	loadCursorW        = user32.NewProc("LoadCursorW")
	loadIconW          = user32.NewProc("LoadIconW")
	peekMessageW       = user32.NewProc("PeekMessageW")
	registerClassExW   = user32.NewProc("RegisterClassExW")
	sendMessageW       = user32.NewProc("SendMessageW")
	setWindowTextW     = user32.NewProc("SetWindowTextW")
	showWindow         = user32.NewProc("ShowWindow")
	translateMessage   = user32.NewProc("TranslateMessage")
	updateWindow       = user32.NewProc("UpdateWindow")

	windowsProgressCallback = syscall.NewCallback(windowsProgressWindowProcedure)
)

type windowsProgressRunner func(<-chan []byte)

type windowsProgressWriter struct {
	mu      sync.Mutex
	updates chan []byte
	closed  bool
}

func (writer *windowsProgressWriter) Write(contents []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return 0, errors.New("Windows progress presenter is closed")
	}

	copyOfContents := append([]byte(nil), contents...)
	select {
	case writer.updates <- copyOfContents:
	default:
		// Presentation is best-effort. Retain the newest state without ever
		// applying backpressure to multi-gigabyte cache extraction.
		select {
		case <-writer.updates:
		default:
		}
		select {
		case writer.updates <- copyOfContents:
		default:
		}
	}
	return len(contents), nil
}

func (writer *windowsProgressWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if !writer.closed {
		writer.closed = true
		close(writer.updates)
	}
	return nil
}

// Open starts an in-process native progress window. Presentation remains
// best-effort: a missing desktop or failed Win32 control can never stop cache
// extraction or native game launch.
func Open() *Reporter {
	return openWindowsProgress(runWindowsProgress)
}

func openWindowsProgress(run windowsProgressRunner) *Reporter {
	if run == nil {
		return newNoopReporter()
	}
	updates := make(chan []byte, windowsProgressQueueSize)
	go run(updates)
	return newReporter(&windowsProgressWriter{updates: updates}, nil)
}

type windowsPoint struct {
	x int32
	y int32
}

type windowsMessage struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	point   windowsPoint
	private uint32
}

type windowsClass struct {
	size        uint32
	style       uint32
	windowProc  uintptr
	classExtra  int32
	windowExtra int32
	instance    uintptr
	icon        uintptr
	cursor      uintptr
	background  uintptr
	menuName    *uint16
	className   *uint16
	iconSmall   uintptr
}

type windowsCommonControls struct {
	size    uint32
	classes uint32
}

type windowsProgressWindow struct {
	window                uintptr
	label                 uintptr
	bar                   uintptr
	indeterminate         bool
	indeterminatePosition int
}

func runWindowsProgress(updates <-chan []byte) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	window := createWindowsProgressWindow()
	if window == nil {
		for range updates {
		}
		return
	}
	defer destroyWindow.Call(window.window)

	ticker := time.NewTicker(33 * time.Millisecond)
	defer ticker.Stop()
	var message windowsMessage
	running := true
	for running {
		select {
		case encoded, open := <-updates:
			if !open {
				running = false
				break
			}
			var update event
			if err := json.Unmarshal(bytes.TrimSpace(encoded), &update); err == nil {
				window.apply(update)
			}
		case <-ticker.C:
			window.tick()
		}

		for {
			available, _, _ := peekMessageW.Call(
				uintptr(unsafe.Pointer(&message)), 0, 0, 0, pmRemove,
			)
			if available == 0 {
				break
			}
			translateMessage.Call(uintptr(unsafe.Pointer(&message)))
			dispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
		}
	}
}

func createWindowsProgressWindow() *windowsProgressWindow {
	controls := windowsCommonControls{
		size:    uint32(unsafe.Sizeof(windowsCommonControls{})),
		classes: 0x00000020,
	}
	initialized, _, _ := initCommonControls.Call(uintptr(unsafe.Pointer(&controls)))
	if initialized == 0 {
		return nil
	}

	instance, _, _ := getModuleHandleW.Call(0)
	if instance == 0 {
		return nil
	}
	className := utf16Pointer("GeneralsXSFXProgressWindow")
	cursor, _, _ := loadCursorW.Call(0, idcArrow)
	icon := loadWindowsProgressIcon(instance)
	class := windowsClass{
		size:       uint32(unsafe.Sizeof(windowsClass{})),
		windowProc: windowsProgressCallback,
		instance:   instance,
		icon:       icon,
		cursor:     cursor,
		background: colorWindow + 1,
		className:  className,
		iconSmall:  icon,
	}
	registered, _, registerError := registerClassExW.Call(uintptr(unsafe.Pointer(&class)))
	if registered == 0 && registerError != classAlreadyExists {
		return nil
	}

	const width = 520
	const height = 154
	screenWidth, _, _ := getSystemMetrics.Call(0)
	screenHeight, _, _ := getSystemMetrics.Call(1)
	x := (int(screenWidth) - width) / 2
	y := (int(screenHeight) - height) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	title := utf16Pointer("GeneralsX - Preparing Game")
	windowStyle := uintptr(wsCaption | wsSysMenu | wsMinimizeBox | wsVisible)
	window, _, _ := createWindowExW.Call(
		wsExDlgModalFrame|wsExTopmost,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		windowStyle,
		uintptr(x), uintptr(y), width, height,
		0, 0, instance, 0,
	)
	if window == 0 {
		return nil
	}

	staticClass := utf16Pointer("STATIC")
	initialLabel := utf16Pointer("Preparing game files...")
	label, _, _ := createWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(staticClass)),
		uintptr(unsafe.Pointer(initialLabel)),
		wsChild|wsVisible,
		24, 23, 456, 24,
		window, 0, instance, 0,
	)
	progressClass := utf16Pointer("msctls_progress32")
	bar, _, _ := createWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(progressClass)),
		0,
		wsChild|wsVisible|pbsSmooth,
		24, 57, 456, 24,
		window, 0, instance, 0,
	)
	if label == 0 || bar == 0 {
		destroyWindow.Call(window)
		return nil
	}

	font, _, _ := getStockObject.Call(defaultGUIFont)
	if font != 0 {
		sendMessageW.Call(label, wmSetFont, font, 1)
		sendMessageW.Call(bar, wmSetFont, font, 1)
	}
	sendMessageW.Call(bar, pbmSetRange32, 0, windowsProgressScale)
	showWindow.Call(window, swShowNormal)
	updateWindow.Call(window)
	return &windowsProgressWindow{window: window, label: label, bar: bar}
}

func loadWindowsProgressIcon(instance uintptr) uintptr {
	// GeneralsX @feature Codex 05/08/2026 Use the branded PE resource for both the SFX file and progress window.
	icon, _, _ := loadIconW.Call(instance, generalsXIcon)
	if icon == 0 {
		icon, _, _ = loadIconW.Call(0, idiApplication)
	}
	return icon
}

func (window *windowsProgressWindow) apply(update event) {
	label := update.Message
	if update.Indeterminate {
		if label == "" {
			label = "Preparing game files..."
		}
		window.setLabel(label)
		window.indeterminate = true
		window.indeterminatePosition = 0
		sendMessageW.Call(window.bar, pbmSetPos, 0, 0)
		return
	}

	window.indeterminate = false
	if update.Done {
		window.setLabel("Starting game...")
		sendMessageW.Call(window.bar, pbmSetPos, windowsProgressScale, 0)
		return
	}
	if update.Total <= 0 {
		return
	}

	completed := update.Completed
	if completed < 0 {
		completed = 0
	} else if completed > update.Total {
		completed = update.Total
	}
	position := int(float64(completed) * windowsProgressScale / float64(update.Total))
	percentage := position / (windowsProgressScale / 100)
	if label == "" {
		label = "Extracting game files..."
	}
	window.setLabel(fmt.Sprintf("%s %d%%", label, percentage))
	sendMessageW.Call(window.bar, pbmSetPos, uintptr(position), 0)
}

func (window *windowsProgressWindow) tick() {
	if !window.indeterminate {
		return
	}
	window.indeterminatePosition += 20
	if window.indeterminatePosition > windowsProgressScale {
		window.indeterminatePosition = 0
	}
	sendMessageW.Call(window.bar, pbmSetPos, uintptr(window.indeterminatePosition), 0)
}

func (window *windowsProgressWindow) setLabel(label string) {
	text := utf16Pointer(label)
	setWindowTextW.Call(window.label, uintptr(unsafe.Pointer(text)))
}

func utf16Pointer(value string) *uint16 {
	encoded, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		encoded, _ = syscall.UTF16PtrFromString("GeneralsX")
	}
	return encoded
}

func windowsProgressWindowProcedure(window, message, wParam, lParam uintptr) uintptr {
	result, _, _ := defWindowProcW.Call(window, message, wParam, lParam)
	return result
}
