import {AlertDialog} from "@heroui/react/alert-dialog";
import {Button} from "@heroui/react/button";

import {
  canDismissCleanup,
  selectCleanupPlanState,
} from "../lib/cleanupFeedback";
import type {CleanupStatus} from "../lib/cleanupFeedback";
import type {BuildCleanupPlan} from "../types";

interface CleanupBuildDialogProps {
  isOpen: boolean;
  isDisabled: boolean;
  plan: BuildCleanupPlan | null;
  status: CleanupStatus;
  onConfirm: () => void;
  onOpen: () => void;
  onOpenChange: (isOpen: boolean) => void;
}

// GeneralsX @feature Codex 05/08/2026 Confirm the backend-authorized build cleanup without hiding its exact paths.
export function CleanupBuildDialog({
  isOpen,
  isDisabled,
  plan,
  status,
  onConfirm,
  onOpen,
  onOpenChange,
}: CleanupBuildDialogProps) {
  const isPlanning = status === "planning";
  const isPending = status === "pending";
  const isCleaned = status === "cleaned";
  const canDismiss = canDismissCleanup(status);
  const planState = selectCleanupPlanState(plan);
  const isEmptyPlan = planState === "empty";
  const hasActionablePlan = planState === "ready";

  return (
    <>
      <Button
        aria-expanded={isOpen}
        aria-haspopup="dialog"
        className="min-w-32"
        isDisabled={isDisabled}
        isPending={isPlanning}
        variant="danger-soft"
        onPress={onOpen}
      >
        {isPlanning ? "Preparing…" : isCleaned ? "Cleaned Up" : "Cleanup"}
      </Button>

      <AlertDialog.Backdrop
        isOpen={isOpen}
        isDismissable={false}
        isKeyboardDismissDisabled={!canDismiss}
        onOpenChange={(open) => {
          if (canDismiss) {
            onOpenChange(open);
          }
        }}
      >
        <AlertDialog.Container>
          <AlertDialog.Dialog aria-busy={isPending} className="sm:max-w-[440px]">
            <AlertDialog.Header>
              <AlertDialog.Icon status={isEmptyPlan ? "success" : "danger"} />
              <AlertDialog.Heading>
                {isEmptyPlan ? "Nothing to clean up" : "Clean up build files?"}
              </AlertDialog.Heading>
            </AlertDialog.Header>
            <AlertDialog.Body>
              {isEmptyPlan ? (
                <div className="space-y-2">
                  <p>All existing files were preserved.</p>
                  <p>Nothing is currently eligible for cleanup.</p>
                </div>
              ) : hasActionablePlan && plan ? (
                <>
                  <p>The following completed-build files will be permanently removed:</p>
                  <ul className="mt-4 space-y-3">
                    {plan.entries.map((entry) => (
                      <li className="rounded-2xl bg-surface-secondary p-3" key={`${entry.label}:${entry.path}`}>
                        <span className="block text-sm font-medium text-foreground">{entry.label}</span>
                        <code className="mt-1 block break-all text-xs text-muted">{entry.path}</code>
                      </li>
                    ))}
                  </ul>
                </>
              ) : (
                <p>No cleanup plan is available. Close this dialog and try again.</p>
              )}
              {plan ? (
                <p className="mt-4 text-sm text-muted">
                  Your Desktop copy is preserved at{" "}
                  <code className="break-all text-foreground">{plan.desktopCopyPath}</code>.
                </p>
              ) : null}
            </AlertDialog.Body>
            <AlertDialog.Footer>
              {hasActionablePlan ? (
                <>
                  <Button
                    isDisabled={!canDismiss}
                    variant="tertiary"
                    onPress={() => onOpenChange(false)}
                  >
                    Cancel
                  </Button>
                  <Button
                    className="min-w-36"
                    isPending={isPending}
                    variant="danger"
                    onPress={onConfirm}
                  >
                    {isPending ? "Cleaning up…" : "Clean Up Files"}
                  </Button>
                </>
              ) : (
                <Button variant="secondary" onPress={() => onOpenChange(false)}>
                  Close
                </Button>
              )}
            </AlertDialog.Footer>
          </AlertDialog.Dialog>
        </AlertDialog.Container>
      </AlertDialog.Backdrop>
    </>
  );
}
