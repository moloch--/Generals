import {Alert} from "@heroui/react/alert";
import {Button} from "@heroui/react/button";
import {Card} from "@heroui/react/card";
import {Chip} from "@heroui/react/chip";
import {Label} from "@heroui/react/label";
import {ProgressBar} from "@heroui/react/progress-bar";
import {ScrollShadow} from "@heroui/react/scroll-shadow";

import {formatPhase} from "../lib/request";
import type {BuildLogEvent, BuildProgressEvent, ExecutionState} from "../types";

interface BuildStatusProps {
  state: ExecutionState;
  progress: BuildProgressEvent | null;
  logs: BuildLogEvent[];
  error: string;
  output: string;
  dryRun: boolean;
  onCancel: () => void;
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

export function BuildStatus({state, progress, logs, error, output, dryRun, onCancel, onReset}: BuildStatusProps) {
  if (state === "idle") {
    return null;
  }

  const chip = statusChip(state);
  const isActive = state === "validating" || state === "running" || state === "cancelling";
  const isIndeterminate = state === "validating" || (isActive && (progress?.percent ?? 0) < 0);
  const percent = Math.min(100, Math.max(0, progress?.percent ?? 0));
  const phase = progress?.phase ? formatPhase(progress.phase) : "Request Validation";

  return (
    <Card aria-busy={isActive} className="w-full" variant="secondary">
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
              <Alert.Title>{dryRun ? "Dry-run plan complete" : "Verified artifact created"}</Alert.Title>
              <Alert.Description className="break-all">
                {dryRun ? "No artifact was written and SteamCMD was not invoked." : output}
              </Alert.Description>
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

        <section aria-labelledby="build-log-heading" className="space-y-2">
          <div className="flex items-center justify-between gap-4">
            <h3 className="text-sm font-medium" id="build-log-heading">Plain-text log</h3>
            <span className="text-xs tabular-nums text-muted">{logs.length} lines</span>
          </div>
          <ScrollShadow className="max-h-72 overflow-y-auto rounded-2xl bg-background p-4">
            <pre className="min-h-28 whitespace-pre-wrap break-words font-mono text-xs leading-5 text-muted">
              {logs.length > 0
                ? logs.map((entry) => `${entry.stream === "stderr" ? "[stderr] " : ""}${entry.text}`).join("\n")
                : "Waiting for builder output…"}
            </pre>
          </ScrollShadow>
        </section>
      </Card.Content>

      <Card.Footer className="flex justify-end gap-3">
        {isActive ? (
          <Button
            isDisabled={state === "validating" || state === "cancelling"}
            variant="danger-soft"
            onPress={onCancel}
          >
            {state === "cancelling" ? "Cancelling…" : "Cancel build"}
          </Button>
        ) : (
          <Button variant="secondary" onPress={onReset}>Review settings</Button>
        )}
      </Card.Footer>
    </Card>
  );
}
