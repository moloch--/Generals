export type CopyStatus = "idle" | "pending" | "copied" | "error";

export interface CopyFeedback {
  status: CopyStatus;
  message: string;
}

export const idleCopyFeedback: CopyFeedback = {status: "idle", message: ""};

// GeneralsX @feature Codex 05/08/2026 Keep the completed build pane visible until an active Desktop copy settles.
export function canResetAfterCopy(status: CopyStatus): boolean {
  return status !== "pending";
}

// GeneralsX @feature Codex 05/08/2026 Keep Desktop-copy state transitions consistent and retryable.
export async function runDesktopCopy(
  copy: () => Promise<string>,
  report: (feedback: CopyFeedback) => void,
): Promise<void> {
  report({status: "pending", message: ""});
  try {
    const destination = await copy();
    report({status: "copied", message: destination});
  } catch (error) {
    report({status: "error", message: error instanceof Error ? error.message : String(error)});
  }
}
