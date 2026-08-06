import type {BuildCleanupPlan} from "../types";

export type CleanupStatus = "idle" | "planning" | "ready" | "pending" | "cleaned" | "error";

export interface CleanupFeedback {
  status: CleanupStatus;
  message: string;
  plan: BuildCleanupPlan | null;
}

export const idleCleanupFeedback: CleanupFeedback = {status: "idle", message: "", plan: null};

export type CleanupPlanState = "unavailable" | "empty" | "ready";

// GeneralsX @feature Codex 05/08/2026 Distinguish a safe empty cleanup plan from an actionable deletion plan.
export function selectCleanupPlanState(plan: BuildCleanupPlan | null): CleanupPlanState {
  if (!plan) {
    return "unavailable";
  }
  return plan.entries.length === 0 ? "empty" : "ready";
}

// GeneralsX @feature Codex 05/08/2026 Keep destructive confirmation modal until an active cleanup settles.
export function canDismissCleanup(status: CleanupStatus): boolean {
  return status !== "pending";
}

function feedbackError(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

// GeneralsX @feature Codex 05/08/2026 Load the backend-authorized deletion list before opening confirmation.
export async function loadCleanupPlan(
  load: () => Promise<BuildCleanupPlan>,
  report: (feedback: CleanupFeedback) => void,
): Promise<BuildCleanupPlan | null> {
  report({status: "planning", message: "", plan: null});
  try {
    const plan = await load();
    report({status: "ready", message: "", plan});
    return plan;
  } catch (error) {
    report({status: "error", message: feedbackError(error), plan: null});
    return null;
  }
}

// GeneralsX @feature Codex 05/08/2026 Keep destructive cleanup pending until the backend reports its result.
export async function runBuildCleanup(
  cleanup: () => Promise<string>,
  plan: BuildCleanupPlan,
  report: (feedback: CleanupFeedback) => void,
): Promise<void> {
  report({status: "pending", message: "", plan});
  try {
    const message = await cleanup();
    report({status: "cleaned", message, plan});
  } catch (error) {
    report({status: "error", message: feedbackError(error), plan});
  }
}
