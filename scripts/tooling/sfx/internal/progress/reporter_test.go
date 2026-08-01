// GeneralsX @feature Codex 01/08/2026 Verify progress protocol throttling and failure isolation.
package progress

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

func TestReporterProtocolThrottlesAndStops(t *testing.T) {
	sink := &recordingWriteCloser{maxWrite: 3}
	stops := 0
	reporter := newReporter(sink, func() { stops++ })

	reporter.Indeterminate("Preparing game files…")
	reporter.Indeterminate("Preparing game files…")
	reporter.Update("Extracting game files…", 0, 10_000)
	for completed := int64(1); completed < 100; completed++ {
		reporter.Update("Extracting game files…", completed, 10_000)
	}
	reporter.Update("Extracting game files…", 100, 10_000)
	reporter.Update("Extracting game files…", 100, 10_000)
	reporter.Update("Extracting game files…", 10_000, 10_000)
	reporter.Complete()
	reporter.Complete()
	reporter.Close()
	reporter.Close()

	if stops != 1 {
		t.Fatalf("helper stop count = %d, want 1", stops)
	}
	if sink.closeCount != 1 {
		t.Fatalf("helper input close count = %d, want 1", sink.closeCount)
	}
	events := decodeEvents(t, sink.Bytes())
	want := []event{
		{Message: "Preparing game files…", Indeterminate: true},
		{Message: "Extracting game files…", Total: 10_000},
		{Message: "Extracting game files…", Completed: 100, Total: 10_000},
		{Message: "Extracting game files…", Completed: 10_000, Total: 10_000},
		{Done: true},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("event %d = %#v, want %#v", index, events[index], want[index])
		}
	}
}

func TestReporterClampsValuesAndUsesIndeterminateFallback(t *testing.T) {
	sink := &recordingWriteCloser{}
	reporter := newReporter(sink, nil)
	reporter.Update("Starting", -10, 100)
	reporter.Update("Starting", 200, 100)
	reporter.Update("Waiting", 1, 0)
	reporter.Complete()
	reporter.Close()

	events := decodeEvents(t, sink.Bytes())
	want := []event{
		{Message: "Starting", Total: 100},
		{Message: "Starting", Completed: 100, Total: 100},
		{Message: "Waiting", Indeterminate: true},
		{Done: true},
	}
	if len(events) != len(want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("event %d = %#v, want %#v", index, events[index], want[index])
		}
	}
}

func TestProgressBucketIsPreciseAndDoesNotOverflow(t *testing.T) {
	maximum := int64(^uint64(0) >> 1)
	for _, test := range []struct {
		completed int64
		total     int64
		want      int64
	}{
		{completed: 999, total: 1999, want: 49},
		{completed: 1998, total: 1999, want: 99},
		{completed: maximum - 1, total: maximum, want: 99},
		{completed: maximum, total: maximum, want: 100},
	} {
		if got := progressBucket(test.completed, test.total); got != test.want {
			t.Fatalf("progressBucket(%d, %d) = %d, want %d", test.completed, test.total, got, test.want)
		}
	}
}

func TestReporterTransportFailureIsHarmlessAndStillReaps(t *testing.T) {
	sink := &recordingWriteCloser{writeErr: errors.New("helper closed")}
	stops := 0
	reporter := newReporter(sink, func() { stops++ })

	reporter.Indeterminate("Preparing")
	reporter.Update("Extracting", 1, 2)
	reporter.Complete()
	reporter.Close()

	if stops != 1 {
		t.Fatalf("helper stop count = %d, want 1", stops)
	}
	if sink.closeCount != 1 {
		t.Fatalf("failed helper input close count = %d, want 1", sink.closeCount)
	}

	var nilReporter *Reporter
	nilReporter.Indeterminate("ignored")
	nilReporter.Update("ignored", 1, 1)
	nilReporter.Complete()
	nilReporter.Close()
}

func TestReporterCloseWithoutCompleteDoesNotReportSuccess(t *testing.T) {
	sink := &recordingWriteCloser{}
	reporter := newReporter(sink, nil)
	reporter.Indeterminate("Extraction failed")
	reporter.Close()

	events := decodeEvents(t, sink.Bytes())
	if len(events) != 1 || events[0].Done {
		t.Fatalf("failed progress events = %#v, want no completion event", events)
	}
}

func decodeEvents(t *testing.T, contents []byte) []event {
	t.Helper()
	var events []event
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	for scanner.Scan() {
		var decoded event
		if err := json.Unmarshal(scanner.Bytes(), &decoded); err != nil {
			t.Fatalf("decode progress event %q: %v", scanner.Text(), err)
		}
		events = append(events, decoded)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

type recordingWriteCloser struct {
	bytes.Buffer
	maxWrite   int
	writeErr   error
	closeCount int
}

func (writer *recordingWriteCloser) Write(contents []byte) (int, error) {
	if writer.writeErr != nil {
		return 0, writer.writeErr
	}
	if writer.maxWrite > 0 && len(contents) > writer.maxWrite {
		contents = contents[:writer.maxWrite]
	}
	return writer.Buffer.Write(contents)
}

func (writer *recordingWriteCloser) Close() error {
	writer.closeCount++
	return nil
}
