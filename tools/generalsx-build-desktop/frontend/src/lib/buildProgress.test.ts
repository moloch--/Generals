import {describe, expect, it} from "vitest";

import type {BuildProgressEvent, ExecutionState} from "../types";
import {
  beginBuildCancellation,
  reduceBuildProgress,
  reduceExecutionProgress,
  recoverBuildCancellation,
  settleStartedExecution,
} from "./buildProgress";

function progress(
  status: BuildProgressEvent["status"],
  percent: number,
  phase = "verify",
): BuildProgressEvent {
  return {
    jobId: "job-1",
    phase,
    status,
    message: status === "running" ? "Verifying the completed build artifact" : "Build completed",
    percent,
  };
}

describe("build completion reconciliation", () => {
  it("keeps success at 100 when a delayed 95 percent event arrives", () => {
    const at95 = progress("running", 95);
    const complete = progress("success", 100, "complete");
    const current = reduceBuildProgress(reduceBuildProgress(at95, complete), at95);

    expect(current).toEqual(complete);
    expect(reduceExecutionProgress("success", at95)).toBe("success");
  });

  it.each<ExecutionState>(["success", "error", "cancelled"])(
    "does not reset terminal state %s when StartBuild resolves",
    (state) => {
      expect(settleStartedExecution(state)).toBe(state);
    },
  );

  it("moves validation to running only when no progress event won the race", () => {
    expect(settleStartedExecution("validating")).toBe("running");
    expect(settleStartedExecution("cancelling")).toBe("cancelling");
  });

  it.each(["error", "cancelled"] as const)(
    "keeps %s terminal progress sticky",
    (status) => {
      const terminal = progress(status, 95);
      expect(reduceBuildProgress(terminal, progress("running", 95))).toEqual(terminal);
      expect(reduceExecutionProgress(status, progress("running", 95))).toBe(status);
    },
  );

  it("keeps cancellation monotonic until the backend reports a terminal result", () => {
    const delayedVerification = progress("running", 95);

    expect(reduceExecutionProgress("cancelling", delayedVerification)).toBe("cancelling");
    expect(reduceExecutionProgress("cancelling", progress("success", 100, "complete"))).toBe("success");
    expect(reduceExecutionProgress("cancelling", progress("error", 95))).toBe("error");
    expect(reduceExecutionProgress("cancelling", progress("cancelled", 95))).toBe("cancelled");
  });

  it("lets the first backend terminal result replace a locally synthesized error", () => {
    expect(reduceExecutionProgress("error", progress("success", 100, "complete"))).toBe("success");
  });

  it("keeps success stable whether completion wins before or after a Cancel click", () => {
    const complete = progress("success", 100, "complete");
    const completionFirst = beginBuildCancellation(
      reduceExecutionProgress("running", complete),
    );
    const cancellationFirst = reduceExecutionProgress(
      beginBuildCancellation("running"),
      complete,
    );

    expect(completionFirst).toBe("success");
    expect(cancellationFirst).toBe("success");
  });

  it("makes a rejected cancellation request retryable without overwriting completion", () => {
    expect(recoverBuildCancellation("cancelling")).toBe("running");
    expect(recoverBuildCancellation("success")).toBe("success");
    expect(recoverBuildCancellation("error")).toBe("error");
    expect(recoverBuildCancellation("cancelled")).toBe("cancelled");
  });
});
