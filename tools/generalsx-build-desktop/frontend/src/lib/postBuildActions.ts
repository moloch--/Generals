import type {CleanupStatus} from "./cleanupFeedback";
import {canResetAfterCopy} from "./copyFeedback";
import type {CopyStatus} from "./copyFeedback";
import {canCopyBuildArtifact} from "./execution";
import type {ExecutionState} from "../types";

export interface PostBuildActions {
  showCopy: boolean;
  showCleanup: boolean;
  copyDisabled: boolean;
  cleanupDisabled: boolean;
  canReset: boolean;
  isBusy: boolean;
}

// GeneralsX @feature Codex 05/08/2026 Serialize post-build Copy and Cleanup interactions in one tested policy.
export function selectPostBuildActions(
  execution: ExecutionState,
  dryRun: boolean,
  copyStatus: CopyStatus,
  cleanupStatus: CleanupStatus,
): PostBuildActions {
  const showCopy = canCopyBuildArtifact(execution, dryRun);
  const copyPending = copyStatus === "pending";
  const cleanupPlanning = cleanupStatus === "planning";
  const cleanupPending = cleanupStatus === "pending";
  const cleanupBusy = cleanupPlanning || cleanupPending;
  const cleaned = cleanupStatus === "cleaned";
  const isBusy = copyPending || cleanupBusy;

  return {
    showCopy,
    showCleanup: showCopy && copyStatus === "copied",
    copyDisabled: copyStatus === "copied" || cleanupBusy || cleaned,
    cleanupDisabled: cleanupPending || cleaned,
    canReset: canResetAfterCopy(copyStatus) && !cleanupBusy,
    isBusy,
  };
}
