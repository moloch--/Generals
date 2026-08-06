package main

import (
	"context"
	"errors"
	"sync"
)

type copyProgressEmitter struct {
	mu          sync.Mutex
	app         *App
	ctx         context.Context
	jobID       string
	operationID string
	last        artifactCopyProgress
	terminal    bool
}

func newCopyProgressEmitter(app *App, ctx context.Context, jobID, operationID string) *copyProgressEmitter {
	emitter := &copyProgressEmitter{
		app: app, ctx: ctx, jobID: jobID, operationID: operationID,
		last: artifactCopyProgress{phase: "preparing", message: "Preparing the Desktop copy"},
	}
	emitter.report(emitter.last)
	return emitter
}

func (emitter *copyProgressEmitter) report(progress artifactCopyProgress) {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if emitter.terminal {
		return
	}
	if progress.totalBytes < emitter.last.totalBytes {
		progress.totalBytes = emitter.last.totalBytes
	}
	if progress.bytesCopied < emitter.last.bytesCopied {
		progress.bytesCopied = emitter.last.bytesCopied
	}
	if progress.totalBytes > 0 && progress.bytesCopied > progress.totalBytes {
		progress.bytesCopied = progress.totalBytes
	}
	progress.percent = artifactCopyPercent(progress.bytesCopied, progress.totalBytes)
	emitter.last = progress
	emitter.emitLocked("running", progress.message)
}

func (emitter *copyProgressEmitter) finish(destination string, err error) {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	if emitter.terminal {
		return
	}
	emitter.terminal = true
	status := "success"
	message := destination
	phase := "complete"
	if err != nil {
		status = "error"
		message = err.Error()
		phase = emitter.last.phase
		if errors.Is(err, context.Canceled) {
			status = "cancelled"
			message = "Desktop copy cancelled"
		}
	} else {
		emitter.last.bytesCopied = emitter.last.totalBytes
		emitter.last.percent = 100
	}
	emitter.last.phase = phase
	emitter.emitLocked(status, message)
}

func (emitter *copyProgressEmitter) emitLocked(status, message string) {
	if emitter.ctx == nil {
		return
	}
	emitter.app.dependencies.emit(emitter.ctx, copyProgressEventName, CopyProgressEvent{
		JobID: emitter.jobID, OperationID: emitter.operationID,
		Phase: emitter.last.phase, Status: status, Message: message,
		BytesCopied: emitter.last.bytesCopied, TotalBytes: emitter.last.totalBytes,
		Percent: emitter.last.percent,
	})
}
