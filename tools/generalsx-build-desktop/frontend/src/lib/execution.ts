import type {ExecutionState} from "../types";

export type FinalStepPane = "review" | "activity";

// GeneralsX @bugfix Codex 05/08/2026 Keep the final-step review and build activity panes mutually exclusive.
export function selectFinalStepPane(execution: ExecutionState): FinalStepPane {
  return execution === "idle" ? "review" : "activity";
}

// GeneralsX @feature Codex 05/08/2026 Offer Desktop copying only for a real successfully built artifact.
export function canCopyBuildArtifact(execution: ExecutionState, dryRun: boolean): boolean {
  return execution === "success" && !dryRun;
}
