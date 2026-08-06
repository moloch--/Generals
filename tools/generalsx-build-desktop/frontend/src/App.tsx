import {Alert} from "@heroui/react/alert";
import {Button} from "@heroui/react/button";
import {Card} from "@heroui/react/card";
import {useCallback, useEffect, useRef, useState} from "react";

import {AppHeader} from "./components/AppHeader";
import {BuildStatus} from "./components/BuildStatus";
import {WizardNavigation} from "./components/WizardNavigation";
import {desktopBackend} from "./lib/backend";
import {selectFinalStepPane} from "./lib/execution";
import {
  applyDirectorySelection,
  emptyBuildRequest,
  normalizeBuildRequest,
  primaryArtifactPath,
  validateWizardStep,
} from "./lib/request";
import {GameFilesStep} from "./steps/GameFilesStep";
import {OptionsStep} from "./steps/OptionsStep";
import {ReviewStep} from "./steps/ReviewStep";
import {TargetStep} from "./steps/TargetStep";
import type {
  BuildLogEvent,
  BuildProgressEvent,
  BuildRequest,
  BuildRequestUpdater,
  DirectoryKind,
  ExecutionState,
  ValidationIssue,
} from "./types";
import {wizardSteps} from "./types";

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function executionState(status: string): ExecutionState | null {
  if (["success", "succeeded", "complete", "completed"].includes(status)) {
    return "success";
  }
  if (["error", "failed", "failure"].includes(status)) {
    return "error";
  }
  if (["cancelled", "canceled"].includes(status)) {
    return "cancelled";
  }
  if (["queued", "running", "started"].includes(status)) {
    return "running";
  }
  return null;
}

export function App() {
  const [request, setRequest] = useState<BuildRequest>(emptyBuildRequest);
  const [hostOS, setHostOS] = useState("");
  const [hostArch, setHostArch] = useState("");
  const [currentStep, setCurrentStep] = useState(0);
  const [legalAcknowledged, setLegalAcknowledged] = useState(false);
  const [issues, setIssues] = useState<ValidationIssue[]>([]);
  const [execution, setExecution] = useState<ExecutionState>("idle");
  const [progress, setProgress] = useState<BuildProgressEvent | null>(null);
  const [logs, setLogs] = useState<BuildLogEvent[]>([]);
  const [executionError, setExecutionError] = useState("");
  const [defaultsError, setDefaultsError] = useState("");
  const [isLoadingDefaults, setIsLoadingDefaults] = useState(true);
  const activeJobRef = useRef("");
  const isStartingRef = useRef(false);

  const loadDefaults = useCallback(async () => {
    setIsLoadingDefaults(true);
    setDefaultsError("");
    try {
      const defaults = await desktopBackend.getDefaults();
      setHostOS(defaults.hostOS);
      setHostArch(defaults.hostArch);
      setRequest({...emptyBuildRequest, ...defaults.request});
    } catch (error) {
      setDefaultsError(errorMessage(error));
    } finally {
      setIsLoadingDefaults(false);
    }
  }, []);

  useEffect(() => {
    void loadDefaults();
  }, [loadDefaults]);

  useEffect(() => {
    const stopProgress = desktopBackend.onProgress((event) => {
      if (!activeJobRef.current && isStartingRef.current) {
        activeJobRef.current = event.jobId;
      }
      if (event.jobId !== activeJobRef.current) {
        return;
      }
      setProgress(event);
      const nextState = executionState(event.status);
      if (event.message.toLowerCase().includes("cancellation requested")) {
        setExecution("cancelling");
      } else if (nextState) {
        setExecution(nextState);
      }
      if (nextState === "error") {
        setExecutionError(
          event.exitCode === undefined ? event.message : `${event.message} (exit code ${event.exitCode})`,
        );
      }
    });
    const stopLog = desktopBackend.onLog((event) => {
      if (!activeJobRef.current && isStartingRef.current) {
        activeJobRef.current = event.jobId;
      }
      if (event.jobId !== activeJobRef.current) {
        return;
      }
      setLogs((previous) => [...previous, event].slice(-500));
    });

    return () => {
      stopProgress();
      stopLog();
    };
  }, []);

  const updateRequest = useCallback<BuildRequestUpdater>((field, value) => {
    setRequest((previous) => ({...previous, [field]: value}));
    setIssues([]);
  }, []);

  const browse = useCallback(async (kind: DirectoryKind) => {
    try {
      const selectedDirectory = await desktopBackend.chooseDirectory(kind, request[kind]);
      if (!selectedDirectory) {
        return;
      }
      updateRequest(kind, applyDirectorySelection(kind, request[kind], selectedDirectory));
    } catch (error) {
      setIssues([{field: kind, message: errorMessage(error)}]);
    }
  }, [request, updateRequest]);

  const moveToStep = useCallback((nextStep: number) => {
    if (execution !== "idle" || nextStep < 0 || nextStep >= wizardSteps.length) {
      return;
    }
    if (nextStep <= currentStep) {
      setIssues([]);
      setCurrentStep(nextStep);
      return;
    }

    for (let step = currentStep; step < nextStep; step += 1) {
      const stepIssues = validateWizardStep(step, request, legalAcknowledged, hostOS);
      if (stepIssues.length > 0) {
        setIssues(stepIssues);
        setCurrentStep(step);
        return;
      }
    }
    setIssues([]);
    setCurrentStep(nextStep);
  }, [currentStep, execution, hostOS, legalAcknowledged, request]);

  const startBuild = useCallback(async () => {
    const normalized = normalizeBuildRequest(request);
    setRequest(normalized);
    setIssues([]);
    setExecutionError("");
    setLogs([]);
    setProgress(null);
    setExecution("validating");

    try {
      const validationIssues = await desktopBackend.validateBuild(normalized, legalAcknowledged);
      setIssues(validationIssues);
      if (validationIssues.some((issue) => issue.severity !== "warning")) {
        setExecution("idle");
        return;
      }

      isStartingRef.current = true;
      const jobId = await desktopBackend.startBuild(normalized);
      activeJobRef.current = jobId;
      isStartingRef.current = false;
      setExecution("running");
    } catch (error) {
      isStartingRef.current = false;
      setExecutionError(errorMessage(error));
      setExecution("error");
    }
  }, [legalAcknowledged, request]);

  const cancelBuild = useCallback(async () => {
    setExecution("cancelling");
    try {
      const cancelled = await desktopBackend.cancelBuild();
      if (!cancelled) {
        setExecutionError("The builder could not cancel the active job. It may have already finished.");
        setExecution("error");
      }
    } catch (error) {
      setExecutionError(errorMessage(error));
      setExecution("error");
    }
  }, []);

  const resetExecution = useCallback(() => {
    activeJobRef.current = "";
    isStartingRef.current = false;
    setProgress(null);
    setLogs([]);
    setExecutionError("");
    setExecution("idle");
  }, []);

  const primaryLabel = request.dryRun ? "Run dry plan" : "Start build";
  // GeneralsX @bugfix Codex 05/08/2026 Replace the final review with build activity throughout execution.
  const finalStepPane = selectFinalStepPane(execution);

  return (
    <div className="min-h-screen bg-background text-foreground">
      <AppHeader hostArch={hostArch} hostOS={hostOS} isMock={desktopBackend.isMock()} />

      <main className="mx-auto max-w-6xl space-y-7 px-5 py-8 sm:px-8 sm:py-10">
        <WizardNavigation currentStep={currentStep} onStepChange={moveToStep} />

        {defaultsError ? (
          <Alert status="danger">
            <Alert.Indicator />
            <Alert.Content>
              <Alert.Title>Could not load builder defaults</Alert.Title>
              <Alert.Description>{defaultsError}</Alert.Description>
            </Alert.Content>
            <Button variant="danger-soft" onPress={() => void loadDefaults()}>Retry</Button>
          </Alert>
        ) : null}

        {isLoadingDefaults ? (
          <Card className="w-full" variant="secondary">
            <Card.Header>
              <Card.Title>Preparing the builder</Card.Title>
              <Card.Description>Resolving host-aware paths and defaults…</Card.Description>
            </Card.Header>
          </Card>
        ) : defaultsError ? null : (
          <>
            {currentStep === 0 ? (
              <TargetStep hostArch={hostArch} hostOS={hostOS} target={request.target} onUpdate={updateRequest} />
            ) : null}
            {currentStep === 1 ? (
              <GameFilesStep
                hostOS={hostOS}
                request={request}
                onBrowse={(kind) => void browse(kind)}
                onUpdate={updateRequest}
              />
            ) : null}
            {currentStep === 2 ? (
              <OptionsStep request={request} onBrowse={(kind) => void browse(kind)} onUpdate={updateRequest} />
            ) : null}
            {currentStep === 3 ? (
              finalStepPane === "review" ? (
                <ReviewStep
                  hostOS={hostOS}
                  issues={issues}
                  legalAcknowledged={legalAcknowledged}
                  request={request}
                  onLegalAcknowledgedChange={(acknowledged) => {
                    setLegalAcknowledged(acknowledged);
                    setIssues((previous) => previous.filter((issue) => issue.field !== "legalAcknowledged"));
                  }}
                />
              ) : (
                <BuildStatus
                  artifactPath={primaryArtifactPath(request, hostOS)}
                  dryRun={request.dryRun}
                  error={executionError}
                  logs={logs}
                  progress={progress}
                  state={execution}
                  onCancel={() => void cancelBuild()}
                  onCleanup={(planId) => desktopBackend.cleanupBuild(activeJobRef.current, planId)}
                  onCopyToDesktop={() => desktopBackend.copyBuildArtifactToDesktop(activeJobRef.current)}
                  onGetCleanupPlan={() => desktopBackend.getBuildCleanupPlan(activeJobRef.current)}
                  onReset={resetExecution}
                />
              )
            ) : null}

            {currentStep !== 3 && issues.length > 0 ? (
              <Alert status="danger">
                <Alert.Indicator />
                <Alert.Content>
                  <Alert.Title>Complete this step to continue</Alert.Title>
                  <Alert.Description>{issues[0]?.message}</Alert.Description>
                </Alert.Content>
              </Alert>
            ) : null}

            {execution === "idle" ? (
              <div className="flex flex-col-reverse items-stretch justify-between gap-3 sm:flex-row sm:items-center">
                <Button
                  isDisabled={currentStep === 0}
                  variant="secondary"
                  onPress={() => moveToStep(currentStep - 1)}
                >
                  Back
                </Button>
                <div className="flex flex-col-reverse gap-3 sm:flex-row">
                  {currentStep < wizardSteps.length - 1 ? (
                    <Button onPress={() => moveToStep(currentStep + 1)}>Continue</Button>
                  ) : (
                    <Button isDisabled={!legalAcknowledged} onPress={() => void startBuild()}>
                      {primaryLabel}
                    </Button>
                  )}
                </div>
              </div>
            ) : null}
          </>
        )}
      </main>
    </div>
  );
}
