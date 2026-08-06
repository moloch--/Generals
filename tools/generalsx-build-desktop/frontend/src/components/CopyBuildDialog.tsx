import {Alert} from "@heroui/react/alert";
import {Button} from "@heroui/react/button";
import {Label} from "@heroui/react/label";
import {Modal} from "@heroui/react/modal";
import {ProgressBar} from "@heroui/react/progress-bar";

import type {CopyFeedback} from "../lib/copyFeedback";

interface CopyBuildDialogProps {
  feedback: CopyFeedback;
  isDisabled: boolean;
  isOpen: boolean;
  onOpen: () => void;
  onOpenChange: (isOpen: boolean) => void;
  onRetry: () => void;
}

const byteFormatter = new Intl.NumberFormat(undefined, {maximumFractionDigits: 1});

function formatByteCount(bytes: number): string {
  const safeBytes = Math.max(0, bytes);
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let value = safeBytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  return `${byteFormatter.format(value)} ${units[unitIndex]}`;
}

function copyPhaseLabel(feedback: CopyFeedback): string {
  if (feedback.status === "copied") {
    return "Copy Complete";
  }
  switch (feedback.phase) {
    case "copying":
      return "Copying to Desktop";
    case "verifying":
      return "Verifying Desktop Copy";
    case "publishing":
      return "Publishing to Desktop";
    default:
      return "Preparing Desktop Copy";
  }
}

export function copyProgressPresentation(feedback: CopyFeedback) {
  const isPending = feedback.status === "pending";
  const hasByteTotal = feedback.totalBytes > 0;
  const copiedAllBytes = hasByteTotal && feedback.bytesCopied >= feedback.totalBytes;
  const isPostCopyPhase = feedback.phase === "verifying" || feedback.phase === "publishing";
  const isIndeterminate = isPending && (
    !hasByteTotal ||
    isPostCopyPhase ||
    (copiedAllBytes && feedback.phase !== "copying")
  );
  const valueLabel = hasByteTotal
    ? `${formatByteCount(feedback.bytesCopied)} of ${formatByteCount(feedback.totalBytes)}`
    : undefined;

  return {isIndeterminate, valueLabel};
}

// GeneralsX @feature Codex 05/08/2026 Keep byte copy and verification visible in a focused, controlled modal.
export function CopyBuildDialog({
  feedback,
  isDisabled,
  isOpen,
  onOpen,
  onOpenChange,
  onRetry,
}: CopyBuildDialogProps) {
  const isPending = feedback.status === "pending";
  const isCopied = feedback.status === "copied";
  const isError = feedback.status === "error";
  const {isIndeterminate, valueLabel} = copyProgressPresentation(feedback);

  return (
    <>
      <Button
        aria-expanded={isOpen}
        aria-haspopup="dialog"
        className="min-w-44"
        isDisabled={isDisabled}
        isPending={isPending}
        onPress={onOpen}
      >
        {isPending ? "Copying…" : isCopied ? "Copied to Desktop" : "Copy to Desktop"}
      </Button>

      <Modal.Backdrop
        isDismissable={!isPending}
        isKeyboardDismissDisabled={isPending}
        isOpen={isOpen}
        onOpenChange={(open) => {
          if (!isPending) {
            onOpenChange(open);
          }
        }}
      >
        <Modal.Container placement="center">
          <Modal.Dialog aria-busy={isPending} className="sm:max-w-[400px]">
            {!isPending ? <Modal.CloseTrigger /> : null}
            <Modal.Header>
              <Modal.Heading>
                {isCopied ? "Copied to Desktop" : isError ? "Copy Could Not Finish" : "Copying to Desktop"}
              </Modal.Heading>
              <p className="mt-1.5 text-sm leading-5 text-muted">
                {isPending
                  ? "Keep the build tool open while the artifact is copied and verified."
                  : isCopied
                    ? "The verified build is ready on your Desktop."
                    : "The original verified build remains available. You can retry safely."}
              </p>
            </Modal.Header>
            <Modal.Body className="space-y-4">
              <ProgressBar
                aria-label="Copy to Desktop progress"
                color={isError ? "danger" : isCopied ? "success" : "accent"}
                isIndeterminate={isIndeterminate}
                value={feedback.percent}
                valueLabel={valueLabel}
              >
                <Label>{copyPhaseLabel(feedback)}</Label>
                {valueLabel || !isIndeterminate ? (
                  <ProgressBar.Output className="tabular-nums">
                    {valueLabel}
                  </ProgressBar.Output>
                ) : null}
                <ProgressBar.Track>
                  <ProgressBar.Fill />
                </ProgressBar.Track>
              </ProgressBar>

              {isCopied ? (
                <code className="block break-all rounded-2xl bg-surface-secondary p-3 text-xs text-foreground">
                  {feedback.message}
                </code>
              ) : null}

              {isError ? (
                <Alert role="alert" status="danger">
                  <Alert.Indicator />
                  <Alert.Content>
                    <Alert.Title>Could not copy to Desktop</Alert.Title>
                    <Alert.Description>{feedback.message}</Alert.Description>
                  </Alert.Content>
                </Alert>
              ) : null}
            </Modal.Body>
            {!isPending ? (
              <Modal.Footer>
                {isError ? (
                  <>
                    <Button variant="secondary" onPress={() => onOpenChange(false)}>
                      Close
                    </Button>
                    <Button onPress={onRetry}>Retry</Button>
                  </>
                ) : (
                  <Button variant="secondary" onPress={() => onOpenChange(false)}>
                    Close
                  </Button>
                )}
              </Modal.Footer>
            ) : null}
          </Modal.Dialog>
        </Modal.Container>
      </Modal.Backdrop>
    </>
  );
}
