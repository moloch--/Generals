import type {BuildProgressEvent, BuildProgressStatus, ExecutionState} from "../types";

const terminalStatuses = new Set<BuildProgressStatus>(["success", "error", "cancelled"]);
const terminalStates = new Set<ExecutionState>(["success", "error", "cancelled"]);

export function isTerminalProgressStatus(status: BuildProgressStatus): boolean {
  return terminalStatuses.has(status);
}

export function executionStateForProgress(event: BuildProgressEvent): ExecutionState | null {
  if (event.status === "success") {
    return "success";
  }
  if (event.status === "error") {
    return "error";
  }
  if (event.status === "cancelled") {
    return "cancelled";
  }
  if (event.message.toLowerCase().includes("cancellation requested")) {
    return "cancelling";
  }
  if (event.status === "queued" || event.status === "running") {
    return "running";
  }
  return null;
}

// GeneralsX @bugfix Codex 05/08/2026 Make terminal build state immune to delayed progress events and RPC ordering.
export function reduceExecutionProgress(
  current: ExecutionState,
  event: BuildProgressEvent,
): ExecutionState {
  const next = executionStateForProgress(event);
  if (isTerminalProgressStatus(event.status)) {
    return next ?? current;
  }
  if (terminalStates.has(current) || current === "cancelling") {
    return current;
  }
  return next ?? current;
}

export function reduceBuildProgress(
  current: BuildProgressEvent | null,
  event: BuildProgressEvent,
): BuildProgressEvent {
  if (
    current?.jobId === event.jobId &&
    isTerminalProgressStatus(current.status)
  ) {
    return current;
  }
  return event;
}

export function settleStartedExecution(current: ExecutionState): ExecutionState {
  return current === "validating" ? "running" : current;
}

export function beginBuildCancellation(current: ExecutionState): ExecutionState {
  return current === "running" ? "cancelling" : current;
}

export function recoverBuildCancellation(current: ExecutionState): ExecutionState {
  return current === "cancelling" ? "running" : current;
}
