import {Alert} from "@heroui/react/alert";
import {Button} from "@heroui/react/button";
import {Card} from "@heroui/react/card";
import {Chip} from "@heroui/react/chip";
import {Label} from "@heroui/react/label";
import {ProgressBar} from "@heroui/react/progress-bar";
import {ScrollShadow} from "@heroui/react/scroll-shadow";
import {useLayoutEffect, useRef, useState} from "react";

import {CleanupBuildDialog} from "./CleanupBuildDialog";
import {
  canDismissCleanup,
  idleCleanupFeedback,
  loadCleanupPlan,
  runBuildCleanup,
} from "../lib/cleanupFeedback";
import type {CleanupFeedback} from "../lib/cleanupFeedback";
import {idleCopyFeedback, runDesktopCopy} from "../lib/copyFeedback";
import type {CopyFeedback} from "../lib/copyFeedback";
import {
  DEFAULT_FOLLOW_LOG_TAIL,
  isAtLogTail,
  scrollLogToTail,
} from "../lib/logScroll";
import {selectPostBuildActions} from "../lib/postBuildActions";
import {formatPhase} from "../lib/request";
import type {BuildCleanupPlan, BuildLogEvent, BuildProgressEvent, ExecutionState} from "../types";

interface BuildStatusProps {
  state: ExecutionState;
  progress: BuildProgressEvent | null;
  logs: BuildLogEvent[];
  error: string;
  artifactPath: string;
  dryRun: boolean;
  onCancel: () => void;
  onCleanup: (planId: string) => Promise<string>;
  onCopyToDesktop: () => Promise<string>;
  onGetCleanupPlan: () => Promise<BuildCleanupPlan>;
  onReset: () => void;
}

function statusChip(state: ExecutionState) {
  if (state === "success") {
    return {color: "success" as const, label: "Complete"};
  }
  if (state === "error") {
    return {color: "danger" as const, label: "Failed"};
  }
  if (state === "cancelled") {
    return {color: "warning" as const, label: "Cancelled"};
  }
  if (state === "cancelling") {
    return {color: "warning" as const, label: "Cancelling"};
  }
  return {color: "accent" as const, label: state === "validating" ? "Validating" : "Running"};
}

export function BuildStatus({
  state,
  progress,
  logs,
  error,
  artifactPath,
  dryRun,
  onCancel,
  onCleanup,
  onCopyToDesktop,
  onGetCleanupPlan,
  onReset,
}: BuildStatusProps) {
  const [copyFeedback, setCopyFeedback] = useState<CopyFeedback>(idleCopyFeedback);
  const [cleanupFeedback, setCleanupFeedback] = useState<CleanupFeedback>(idleCleanupFeedback);
  const [isCleanupDialogOpen, setIsCleanupDialogOpen] = useState(false);
  const logViewportRef = useRef<HTMLDivElement>(null);
  const followLogTailRef = useRef(DEFAULT_FOLLOW_LOG_TAIL);

  useLayoutEffect(() => {
    if (state === "idle" || state === "validating") {
      followLogTailRef.current = DEFAULT_FOLLOW_LOG_TAIL;
    }
  }, [state]);

  // GeneralsX @feature Codex 05/08/2026 Follow streamed build output until the user deliberately scrolls back.
  useLayoutEffect(() => {
    const viewport = logViewportRef.current;
    if (viewport) {
      scrollLogToTail(viewport, followLogTailRef.current);
    }
  }, [logs]);

  if (state === "idle") {
    return null;
  }

  const chip = statusChip(state);
  const isActive = state === "validating" || state === "running" || state === "cancelling";
  const isIndeterminate = state === "validating" || (isActive && (progress?.percent ?? 0) < 0);
  const percent = Math.min(100, Math.max(0, progress?.percent ?? 0));
  const phase = progress?.phase ? formatPhase(progress.phase) : "Request Validation";
  const postBuildActions = selectPostBuildActions(
    state,
    dryRun,
    copyFeedback.status,
    cleanupFeedback.status,
  );

  // GeneralsX @feature Codex 05/08/2026 Require an exact backend cleanup plan before showing confirmation.
  const openCleanupDialog = async () => {
    const plan = await loadCleanupPlan(onGetCleanupPlan, setCleanupFeedback);
    if (plan) {
      setIsCleanupDialogOpen(true);
    }
  };

  const changeCleanupDialogOpen = (open: boolean) => {
    if (!canDismissCleanup(cleanupFeedback.status)) {
      return;
    }
    setIsCleanupDialogOpen(open);
    if (!open && cleanupFeedback.status === "ready") {
      setCleanupFeedback(idleCleanupFeedback);
    }
  };

  const confirmCleanup = async () => {
    const plan = cleanupFeedback.plan;
    if (!plan || cleanupFeedback.status !== "ready") {
      return;
    }
    await runBuildCleanup(() => onCleanup(plan.planId), plan, setCleanupFeedback);
    setIsCleanupDialogOpen(false);
  };

  return (
    <Card aria-busy={isActive || postBuildActions.isBusy} className="w-full" variant="secondary">
      <Card.Header className="flex-row items-start justify-between gap-4">
        <div>
          <Card.Title>Build Activity</Card.Title>
          <Card.Description aria-live="polite">
            {progress?.message || (state === "validating" ? "Checking paths and build settings…" : "Preparing the build…")}
          </Card.Description>
        </div>
        <Chip className="shrink-0" color={chip.color} variant="soft">
          <Chip.Label>{chip.label}</Chip.Label>
        </Chip>
      </Card.Header>

      <Card.Content className="space-y-5">
        <ProgressBar
          aria-label="Overall build progress"
          color={state === "error" ? "danger" : state === "success" ? "success" : "accent"}
          isIndeterminate={isIndeterminate}
          value={percent}
        >
          <Label>{phase}</Label>
          {!isIndeterminate ? <ProgressBar.Output className="tabular-nums" /> : null}
          <ProgressBar.Track>
            <ProgressBar.Fill />
          </ProgressBar.Track>
        </ProgressBar>

        {state === "success" ? (
          <Alert status="success">
            <Alert.Indicator />
            <Alert.Content>
              <Alert.Title>
                {dryRun
                  ? "Dry-run plan complete"
                  : cleanupFeedback.status === "cleaned"
                    ? "Build files cleaned up"
                    : copyFeedback.status === "copied"
                      ? "Copied to Desktop"
                      : "Verified artifact created"}
              </Alert.Title>
              <Alert.Description aria-live="polite" className="break-all">
                {dryRun
                  ? "No artifact was written and SteamCMD was not invoked."
                  : cleanupFeedback.status === "cleaned"
                    ? (
                        <>
                          <span className="block">{cleanupFeedback.message}</span>
                          <span className="mt-1 block">
                            Desktop copy preserved at {cleanupFeedback.plan?.desktopCopyPath || copyFeedback.message}.
                          </span>
                        </>
                      )
                  : copyFeedback.status === "copied"
                    ? copyFeedback.message
                    : artifactPath}
              </Alert.Description>
            </Alert.Content>
          </Alert>
        ) : null}

        {copyFeedback.status === "error" ? (
          <Alert role="alert" status="danger">
            <Alert.Indicator />
            <Alert.Content>
              <Alert.Title>Could not copy to Desktop</Alert.Title>
              <Alert.Description>{copyFeedback.message}</Alert.Description>
            </Alert.Content>
          </Alert>
        ) : null}

        {cleanupFeedback.status === "error" ? (
          <Alert role="alert" status="danger">
            <Alert.Indicator />
            <Alert.Content>
              <Alert.Title>
                {cleanupFeedback.plan ? "Could not clean up build files" : "Could not prepare cleanup"}
              </Alert.Title>
              <Alert.Description>{cleanupFeedback.message}</Alert.Description>
            </Alert.Content>
          </Alert>
        ) : null}

        {state === "error" ? (
          <Alert status="danger">
            <Alert.Indicator />
            <Alert.Content>
              <Alert.Title>Build failed</Alert.Title>
              <Alert.Description>{error || "Review the final log lines and update the build settings."}</Alert.Description>
            </Alert.Content>
          </Alert>
        ) : null}

        {state === "cancelled" ? (
          <Alert status="warning">
            <Alert.Indicator />
            <Alert.Content>
              <Alert.Title>Build cancelled</Alert.Title>
              <Alert.Description>Completed work was stopped. Review the log before starting again.</Alert.Description>
            </Alert.Content>
          </Alert>
        ) : null}

        <div className="space-y-2">
          <div className="flex items-center justify-between gap-4">
            <h3 className="text-sm font-medium" id="build-log-heading">Plain-text log</h3>
            <span className="text-xs tabular-nums text-muted">{logs.length} lines</span>
          </div>
          <ScrollShadow
            ref={logViewportRef}
            aria-labelledby="build-log-heading"
            className="max-h-72 overflow-y-auto rounded-2xl bg-background p-4"
            role="region"
            tabIndex={0}
            onScroll={(event) => {
              followLogTailRef.current = isAtLogTail(event.currentTarget);
            }}
          >
            <pre className="min-h-28 whitespace-pre-wrap break-words font-mono text-xs leading-5 text-muted">
              {logs.length > 0
                ? logs.map((entry) => `${entry.stream === "stderr" ? "[stderr] " : ""}${entry.text}`).join("\n")
                : "Waiting for builder output…"}
            </pre>
          </ScrollShadow>
        </div>
      </Card.Content>

      <Card.Footer className="flex flex-wrap justify-end gap-3">
        {isActive ? (
          <Button
            isDisabled={state === "validating" || state === "cancelling"}
            variant="danger-soft"
            onPress={onCancel}
          >
            {state === "cancelling" ? "Cancelling…" : "Cancel build"}
          </Button>
        ) : (
          <>
            <Button
              isDisabled={!postBuildActions.canReset}
              variant="secondary"
              onPress={onReset}
            >
              Review settings
            </Button>
            {postBuildActions.showCleanup ? (
              <CleanupBuildDialog
                isDisabled={postBuildActions.cleanupDisabled}
                isOpen={isCleanupDialogOpen}
                plan={cleanupFeedback.plan}
                status={cleanupFeedback.status}
                onConfirm={() => void confirmCleanup()}
                onOpen={() => void openCleanupDialog()}
                onOpenChange={changeCleanupDialogOpen}
              />
            ) : null}
            {postBuildActions.showCopy ? (
              <Button
                className="min-w-44"
                isDisabled={postBuildActions.copyDisabled}
                isPending={copyFeedback.status === "pending"}
                onPress={() => void runDesktopCopy(onCopyToDesktop, setCopyFeedback)}
              >
                {copyFeedback.status === "pending"
                  ? "Copying…"
                  : copyFeedback.status === "copied"
                    ? "Copied to Desktop"
                    : "Copy to Desktop"}
              </Button>
            ) : null}
          </>
        )}
      </Card.Footer>
    </Card>
  );
}
