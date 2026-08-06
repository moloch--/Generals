import type {CopyProgressEvent} from "../types";

export type CopyStatus = "idle" | "pending" | "copied" | "error";

export interface CopyFeedback {
  status: CopyStatus;
  jobId: string;
  operationId: string;
  phase: CopyProgressEvent["phase"];
  message: string;
  bytesCopied: number;
  totalBytes: number;
  percent: number;
}

export const idleCopyFeedback: CopyFeedback = {
  status: "idle",
  jobId: "",
  operationId: "",
  phase: "preparing",
  message: "",
  bytesCopied: 0,
  totalBytes: 0,
  percent: 0,
};

export type CopyFeedbackUpdater = (current: CopyFeedback) => CopyFeedback;
export type CopyFeedbackReporter = (update: CopyFeedbackUpdater) => void;

let copyOperationSequence = 0;

export function createCopyOperationId(): string {
  copyOperationSequence += 1;
  return `desktop-copy-${Date.now().toString(36)}-${copyOperationSequence.toString(36)}`;
}

export function beginDesktopCopy(jobId: string, operationId: string): CopyFeedback {
  return {...idleCopyFeedback, status: "pending", jobId, operationId};
}

function copyEventMatches(current: CopyFeedback, event: CopyProgressEvent): boolean {
  return current.jobId === event.jobId && current.operationId === event.operationId;
}

// GeneralsX @feature Codex 05/08/2026 Keep byte progress monotonic and make streamed terminal states sticky.
export function reduceDesktopCopyProgress(
  current: CopyFeedback,
  event: CopyProgressEvent,
): CopyFeedback {
  if (!copyEventMatches(current, event) || current.status === "copied" || current.status === "error") {
    return current;
  }
  const bytesCopied = Math.max(current.bytesCopied, Math.max(0, event.bytesCopied));
  const totalBytes = Math.max(current.totalBytes, Math.max(0, event.totalBytes));
  const percent = Math.max(current.percent, Math.min(100, Math.max(0, event.percent)));
  if (event.status === "success") {
    return {
      ...current,
      status: "copied",
      phase: "complete",
      message: event.message,
      bytesCopied: totalBytes,
      totalBytes,
      percent: 100,
    };
  }
  if (event.status === "error" || event.status === "cancelled") {
    return {
      ...current,
      status: "error",
      phase: event.phase,
      message: event.message,
      bytesCopied,
      totalBytes,
      percent,
    };
  }
  return {
    ...current,
    status: "pending",
    phase: event.phase,
    message: event.message,
    bytesCopied,
    totalBytes,
    percent,
  };
}

function settleDesktopCopy(
  current: CopyFeedback,
  jobId: string,
  operationId: string,
  status: "copied" | "error",
  message: string,
): CopyFeedback {
  if (current.jobId !== jobId || current.operationId !== operationId) {
    return current;
  }
  return {
    ...current,
    status,
    phase: status === "copied" ? "complete" : current.phase,
    message,
    bytesCopied: status === "copied" ? current.totalBytes : current.bytesCopied,
    percent: status === "copied" ? 100 : current.percent,
  };
}

// GeneralsX @feature Codex 05/08/2026 Treat the synchronous RPC as authoritative if streamed events are delayed or lost.
export async function runDesktopCopy(
  jobId: string,
  operationId: string,
  copy: () => Promise<string>,
  report: CopyFeedbackReporter,
): Promise<void> {
  report(() => beginDesktopCopy(jobId, operationId));
  try {
    const destination = await copy();
    report((current) => settleDesktopCopy(current, jobId, operationId, "copied", destination));
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    report((current) => settleDesktopCopy(current, jobId, operationId, "error", message));
  }
}

// GeneralsX @feature Codex 05/08/2026 Keep the completed build pane visible until an active Desktop copy settles.
export function canResetAfterCopy(status: CopyStatus): boolean {
  return status !== "pending";
}
