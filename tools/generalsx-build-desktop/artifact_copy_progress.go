package main

import (
	"context"
	"fmt"
	"io"
)

type artifactCopyProgress struct {
	phase       string
	message     string
	bytesCopied int64
	totalBytes  int64
	percent     int
}

type artifactCopyProgressReporter func(artifactCopyProgress)

type artifactCopyProgressContextKey struct{}

func withArtifactCopyProgress(ctx context.Context, report artifactCopyProgressReporter) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, artifactCopyProgressContextKey{}, report)
}

func reportArtifactCopyProgress(ctx context.Context, progress artifactCopyProgress) {
	report, _ := ctx.Value(artifactCopyProgressContextKey{}).(artifactCopyProgressReporter)
	if report != nil {
		report(progress)
	}
}

func reportArtifactCopyStage(ctx context.Context, phase, message string, bytesCopied, totalBytes int64) {
	reportArtifactCopyProgress(ctx, artifactCopyProgress{
		phase: phase, message: message, bytesCopied: bytesCopied, totalBytes: totalBytes,
		percent: artifactCopyPercent(bytesCopied, totalBytes),
	})
}

func artifactCopyPercent(bytesCopied, totalBytes int64) int {
	if totalBytes <= 0 || bytesCopied <= 0 {
		return 0
	}
	if bytesCopied >= totalBytes {
		return 100
	}
	return int(float64(bytesCopied) / float64(totalBytes) * 100)
}

type artifactCopyCounter struct {
	ctx         context.Context
	totalBytes  int64
	bytesCopied int64
	lastPercent int
}

func newArtifactCopyCounter(ctx context.Context, totalBytes int64) *artifactCopyCounter {
	counter := &artifactCopyCounter{ctx: ctx, totalBytes: totalBytes, lastPercent: -1}
	counter.report()
	return counter
}

func (counter *artifactCopyCounter) writer(destination io.Writer) io.Writer {
	return &artifactCopyCountingWriter{destination: destination, counter: counter}
}

func (counter *artifactCopyCounter) add(bytes int64) {
	if bytes <= 0 {
		return
	}
	counter.bytesCopied += bytes
	counter.report()
}

func (counter *artifactCopyCounter) report() {
	percent := artifactCopyPercent(counter.bytesCopied, counter.totalBytes)
	if percent == counter.lastPercent {
		return
	}
	counter.lastPercent = percent
	reportedBytes := counter.bytesCopied
	if reportedBytes > counter.totalBytes {
		reportedBytes = counter.totalBytes
	}
	reportArtifactCopyProgress(counter.ctx, artifactCopyProgress{
		phase: "copying", message: "Copying artifact bytes to Desktop",
		bytesCopied: reportedBytes, totalBytes: counter.totalBytes, percent: percent,
	})
}

func (counter *artifactCopyCounter) finish() error {
	if counter.bytesCopied != counter.totalBytes {
		return fmt.Errorf("copied %d bytes, expected %d", counter.bytesCopied, counter.totalBytes)
	}
	counter.report()
	return nil
}

type artifactCopyCountingWriter struct {
	destination io.Writer
	counter     *artifactCopyCounter
}

func (writer *artifactCopyCountingWriter) Write(contents []byte) (int, error) {
	written, err := writer.destination.Write(contents)
	writer.counter.add(int64(written))
	return written, err
}
