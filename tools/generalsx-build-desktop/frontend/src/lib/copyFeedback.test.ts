import {describe, expect, it} from "vitest";

import type {CopyProgressEvent} from "../types";
import {
  beginDesktopCopy,
  canResetAfterCopy,
  createCopyOperationId,
  idleCopyFeedback,
  reduceDesktopCopyProgress,
  runDesktopCopy,
} from "./copyFeedback";
import type {CopyFeedback, CopyFeedbackUpdater} from "./copyFeedback";

function progress(overrides: Partial<CopyProgressEvent> = {}): CopyProgressEvent {
  return {
    jobId: "job-1",
    operationId: "copy-1",
    phase: "copying",
    status: "running",
    message: "Copying artifact bytes to Desktop",
    bytesCopied: 50,
    totalBytes: 100,
    percent: 50,
    ...overrides,
  };
}

function feedbackRecorder(initial: CopyFeedback = idleCopyFeedback) {
  let current = initial;
  const states: CopyFeedback[] = [];
  return {
    get current() {
      return current;
    },
    states,
    report(update: CopyFeedbackUpdater) {
      current = update(current);
      states.push(current);
    },
  };
}

describe("Desktop copy feedback", () => {
  it("uses the synchronous result when progress events are missed", async () => {
    const recorder = feedbackRecorder();
    let finishCopy: (destination: string) => void = () => undefined;
    const copy = new Promise<string>((resolve) => {
      finishCopy = resolve;
    });

    const operation = runDesktopCopy("job-1", "copy-1", () => copy, recorder.report);
    expect(recorder.current).toMatchObject({status: "pending", jobId: "job-1", operationId: "copy-1"});
    finishCopy("/Users/commander/Desktop/GeneralsXZH.app");
    await operation;

    expect(recorder.current).toMatchObject({
      status: "copied",
      phase: "complete",
      message: "/Users/commander/Desktop/GeneralsXZH.app",
      percent: 100,
    });
  });

  it("keeps byte progress monotonic through verification and makes terminal events sticky", () => {
    let current = beginDesktopCopy("job-1", "copy-1");
    current = reduceDesktopCopyProgress(current, progress({bytesCopied: 75, percent: 75}));
    current = reduceDesktopCopyProgress(current, progress({bytesCopied: 20, percent: 20}));
    expect(current).toMatchObject({status: "pending", bytesCopied: 75, totalBytes: 100, percent: 75});

    current = reduceDesktopCopyProgress(current, progress({
      phase: "verifying",
      message: "Verifying copied artifact",
      bytesCopied: 100,
      percent: 100,
    }));
    expect(current).toMatchObject({status: "pending", phase: "verifying", percent: 100});

    current = reduceDesktopCopyProgress(current, progress({
      phase: "publishing",
      message: "Publishing verified artifact",
      bytesCopied: 100,
      percent: 100,
    }));
    expect(current).toMatchObject({status: "pending", phase: "publishing", percent: 100});

    current = reduceDesktopCopyProgress(current, progress({
      phase: "complete",
      status: "success",
      message: "/Desktop/GeneralsXZH.app",
      bytesCopied: 100,
      percent: 100,
    }));
    const terminal = current;
    current = reduceDesktopCopyProgress(current, progress({percent: 99, bytesCopied: 99}));
    expect(current).toBe(terminal);
  });

  it("ignores wrong jobs and stale retry operations", () => {
    const current = beginDesktopCopy("job-2", "copy-new");
    expect(reduceDesktopCopyProgress(current, progress())).toBe(current);
    expect(reduceDesktopCopyProgress(current, progress({jobId: "job-2", operationId: "copy-old"}))).toBe(current);
  });

  it("does not let a settled stale RPC overwrite a newer retry", async () => {
    const recorder = feedbackRecorder();
    let rejectOldCopy: (error: Error) => void = () => undefined;
    const oldCopy = new Promise<string>((_resolve, reject) => {
      rejectOldCopy = reject;
    });
    const oldOperation = runDesktopCopy(
      "job-1",
      "copy-old",
      () => oldCopy,
      recorder.report,
    );
    recorder.report(() => beginDesktopCopy("job-1", "copy-new"));
    rejectOldCopy(new Error("old copy failed"));
    await oldOperation;
    expect(recorder.current).toMatchObject({status: "pending", operationId: "copy-new"});
  });

  it("reports an authoritative retryable RPC error", async () => {
    const recorder = feedbackRecorder();
    await runDesktopCopy(
      "job-1",
      "copy-1",
      () => Promise.reject(new Error("Desktop is unavailable")),
      recorder.report,
    );
    expect(recorder.current).toMatchObject({status: "error", message: "Desktop is unavailable"});
  });

  it("blocks reset only while a copy is pending and creates distinct operation IDs", () => {
    expect(canResetAfterCopy("idle")).toBe(true);
    expect(canResetAfterCopy("pending")).toBe(false);
    expect(canResetAfterCopy("copied")).toBe(true);
    expect(canResetAfterCopy("error")).toBe(true);
    expect(createCopyOperationId()).not.toBe(createCopyOperationId());
  });
});
