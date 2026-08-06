import {Alert} from "@heroui/react/alert";
import {Button} from "@heroui/react/button";
import {Card} from "@heroui/react/card";
import {useCallback, useEffect, useRef, useState} from "react";

import {AppHeader} from "./components/AppHeader";
import {BuildStatus} from "./components/BuildStatus";
import {WizardNavigation} from "./components/WizardNavigation";
import {desktopBackend} from "./lib/backend";
import {
  beginBuildCancellation,
  isTerminalProgressStatus,
  reduceBuildProgress,
  reduceExecutionProgress,
  recoverBuildCancellation,
  settleStartedExecution,
} from "./lib/buildProgress";
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
  const terminalJobRef = useRef("");
  const pendingProgressRef = useRef<BuildProgressEvent[]>([]);
  const pendingLogsRef = useRef<BuildLogEvent[]>([]);

  const applyProgressEvent = useCallback((event: BuildProgressEvent) => {
    if (event.jobId !== activeJobRef.current || terminalJobRef.current === event.jobId) {
      return;
    }
    if (isTerminalProgressStatus(event.status)) {
      terminalJobRef.current = event.jobId;
    }
    setProgress((current) => reduceBuildProgress(current, event));
    setExecution((current) => reduceExecutionProgress(current, event));
    if (event.status === "error") {
      setExecutionError(
        event.exitCode === undefined ? event.message : `${event.message} (exit code ${event.exitCode})`,
      );
    } else if (isTerminalProgressStatus(event.status)) {
      setExecutionError("");
    }
  }, []);

  const applyLogEvent = useCallback((event: BuildLogEvent) => {
    if (event.jobId !== activeJobRef.current) {
      return;
    }
    setLogs((previous) => [...previous, event].slice(-500));
  }, []);

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
        pendingProgressRef.current = [...pendingProgressRef.current, event].slice(-100);
        return;
      }
      applyProgressEvent(event);
    });
    const stopLog = desktopBackend.onLog((event) => {
      if (!activeJobRef.current && isStartingRef.current) {
        pendingLogsRef.current = [...pendingLogsRef.current, event].slice(-500);
        return;
      }
      applyLogEvent(event);
    });

    return () => {
      stopProgress();
      stopLog();
    };
  }, [applyLogEvent, applyProgressEvent]);

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
    activeJobRef.current = "";
    terminalJobRef.current = "";
    pendingProgressRef.current = [];
    pendingLogsRef.current = [];

    let jobId = "";
    try {
      const validationIssues = await desktopBackend.validateBuild(normalized, legalAcknowledged);
      setIssues(validationIssues);
      if (validationIssues.some((issue) => issue.severity !== "warning")) {
        setExecution("idle");
        return;
      }

      isStartingRef.current = true;
      jobId = await desktopBackend.startBuild(normalized);
      activeJobRef.current = jobId;
      isStartingRef.current = false;
      for (const event of pendingLogsRef.current) {
        applyLogEvent(event);
      }
      for (const event of pendingProgressRef.current) {
        applyProgressEvent(event);
      }
      pendingLogsRef.current = [];
      pendingProgressRef.current = [];
      setExecution(settleStartedExecution);
    } catch (error) {
      isStartingRef.current = false;
      pendingLogsRef.current = [];
      pendingProgressRef.current = [];
      setExecutionError(errorMessage(error));
      setExecution("error");
      return;
    }

    try {
      const terminal = await desktopBackend.waitForBuild(jobId);
      applyProgressEvent(terminal);
    } catch (error) {
      if (activeJobRef.current === jobId && terminalJobRef.current !== jobId) {
        setExecutionError(`Could not confirm the final build status: ${errorMessage(error)}`);
        setExecution("error");
      }
    }
  }, [applyLogEvent, applyProgressEvent, legalAcknowledged, request]);

  const cancelBuild = useCallback(async () => {
    const jobId = activeJobRef.current;
    setExecutionError("");
    setExecution(beginBuildCancellation);
    try {
      // A false response means completion may have won the race. It is not a
      // build failure; WaitForBuild remains the authoritative terminal result.
      await desktopBackend.cancelBuild();
    } catch (error) {
      if (activeJobRef.current === jobId && terminalJobRef.current !== jobId) {
        setExecutionError(`Could not send the cancellation request: ${errorMessage(error)}. The build is still active; try again.`);
        setExecution(recoverBuildCancellation);
      }
    }
  }, []);

  const resetExecution = useCallback(() => {
    activeJobRef.current = "";
    isStartingRef.current = false;
    terminalJobRef.current = "";
    pendingProgressRef.current = [];
    pendingLogsRef.current = [];
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
                  jobId={activeJobRef.current}
                  logs={logs}
                  progress={progress}
                  state={execution}
                  onCancel={() => void cancelBuild()}
                  onCleanup={(planId) => desktopBackend.cleanupBuild(activeJobRef.current, planId)}
                  onCopyProgress={desktopBackend.onCopyProgress}
                  onCopyToDesktop={(operationId) => desktopBackend.copyBuildArtifactToDesktop(activeJobRef.current, operationId)}
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
