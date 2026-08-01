// Package progress reports best-effort launcher progress to a platform helper.
//
// GeneralsX @feature Codex 01/08/2026 Add a failure-isolated progress protocol for packaged launchers.
package progress

import (
	"encoding/json"
	"io"
	"math/bits"
	"sync"
)

const progressBuckets = int64(100)

type event struct {
	Message       string `json:"message,omitempty"`
	Completed     int64  `json:"completed,omitempty"`
	Total         int64  `json:"total,omitempty"`
	Indeterminate bool   `json:"indeterminate,omitempty"`
	Done          bool   `json:"done,omitempty"`
}

type reportMode uint8

const (
	reportNone reportMode = iota
	reportIndeterminate
	reportDeterminate
)

// Reporter sends newline-delimited JSON events to a packaged progress helper.
// A Reporter is safe for concurrent use. Every method, including methods on a
// nil Reporter, is a harmless no-op after setup or transport failure.
type Reporter struct {
	mu sync.Mutex

	writer io.WriteCloser
	stop   func()
	closed bool
	failed bool
	done   bool

	lastMode    reportMode
	lastMessage string
	lastTotal   int64
	lastBucket  int64
}

func newReporter(writer io.WriteCloser, stop func()) *Reporter {
	if writer == nil {
		return &Reporter{closed: true}
	}
	return &Reporter{
		writer:     writer,
		stop:       stop,
		lastBucket: -1,
	}
}

func newNoopReporter() *Reporter {
	return &Reporter{closed: true}
}

// Indeterminate displays label without a numeric completion value.
func (reporter *Reporter) Indeterminate(label string) {
	if reporter == nil {
		return
	}

	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.closed || reporter.failed ||
		reporter.lastMode == reportIndeterminate && reporter.lastMessage == label {
		return
	}
	if !reporter.writeLocked(event{Message: label, Indeterminate: true}) {
		return
	}
	reporter.lastMode = reportIndeterminate
	reporter.lastMessage = label
	reporter.lastTotal = 0
	reporter.lastBucket = -1
}

// Update displays a determinate progress value. Values are clamped to
// [0, total]. A non-positive total is reported as indeterminate. Repeated
// updates are reduced to at most one event per whole percentage point.
func (reporter *Reporter) Update(label string, completed, total int64) {
	if reporter == nil {
		return
	}
	if total <= 0 {
		reporter.Indeterminate(label)
		return
	}
	if completed < 0 {
		completed = 0
	} else if completed > total {
		completed = total
	}
	bucket := progressBucket(completed, total)

	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.closed || reporter.failed {
		return
	}
	if reporter.lastMode == reportDeterminate &&
		reporter.lastMessage == label &&
		reporter.lastTotal == total &&
		bucket <= reporter.lastBucket {
		return
	}
	if !reporter.writeLocked(event{
		Message:   label,
		Completed: completed,
		Total:     total,
	}) {
		return
	}
	reporter.lastMode = reportDeterminate
	reporter.lastMessage = label
	reporter.lastTotal = total
	reporter.lastBucket = bucket
}

func progressBucket(completed, total int64) int64 {
	high, low := bits.Mul64(uint64(completed), uint64(progressBuckets))
	bucket, _ := bits.Div64(high, low, uint64(total))
	return int64(bucket)
}

// Complete marks successful cache preparation. Failed or canceled preparation
// must skip this method so the helper never presents an error as 100% success.
func (reporter *Reporter) Complete() {
	if reporter == nil {
		return
	}

	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	if reporter.closed || reporter.failed || reporter.done {
		return
	}
	if reporter.writeLocked(event{Done: true}) {
		reporter.done = true
	}
}

// Close dismisses the helper and schedules bounded process cleanup. It is
// idempotent and intentionally does not expose or wait for helper failures.
func (reporter *Reporter) Close() {
	if reporter == nil {
		return
	}

	reporter.mu.Lock()
	if reporter.closed {
		reporter.mu.Unlock()
		return
	}
	reporter.closed = true
	if reporter.writer != nil {
		_ = reporter.writer.Close()
		reporter.writer = nil
	}
	stop := reporter.stop
	reporter.stop = nil
	reporter.mu.Unlock()

	if stop != nil {
		stop()
	}
}

func (reporter *Reporter) writeLocked(update event) bool {
	if reporter.writer == nil {
		reporter.failed = true
		return false
	}
	encoded, err := json.Marshal(update)
	if err == nil {
		encoded = append(encoded, '\n')
		err = writeAll(reporter.writer, encoded)
	}
	if err == nil {
		return true
	}
	reporter.failed = true
	_ = reporter.writer.Close()
	reporter.writer = nil
	return false
}

func writeAll(writer io.Writer, contents []byte) error {
	for len(contents) != 0 {
		written, err := writer.Write(contents)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		contents = contents[written:]
	}
	return nil
}
