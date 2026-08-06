import {emptyBuildRequest, isMacOSBuild, primaryArtifactPath, validateAllSteps} from "./request";
import {selectDesktopBackendMode} from "./backendMode";
import type {
  BuildCleanupPlan,
  BuildLogEvent,
  BuildProgressEvent,
  BuildRequest,
  DesktopDefaults,
  DirectoryKind,
  ValidationIssue,
} from "../types";

interface WailsAppBinding {
  GetDefaults(): Promise<DesktopDefaults>;
  ChooseDirectory(kind: DirectoryKind, current: string): Promise<string>;
  ValidateBuild(request: BuildRequest): Promise<ValidationIssue[]>;
  StartBuild(request: BuildRequest): Promise<string>;
  CopyBuildArtifactToDesktop(jobId: string): Promise<string>;
  GetBuildCleanupPlan(jobId: string): Promise<BuildCleanupPlan>;
  CleanupBuild(jobId: string, planId: string): Promise<string>;
  CancelBuild(): Promise<boolean>;
}

interface WailsRuntimeBinding {
  EventsOn(eventName: string, callback: (payload: unknown) => void): (() => void) | void;
}

declare global {
  interface Window {
    go?: {
      main?: {
        App?: WailsAppBinding;
      };
    };
    runtime?: WailsRuntimeBinding;
  }
}

type ProgressListener = (event: BuildProgressEvent) => void;
type LogListener = (event: BuildLogEvent) => void;

const progressListeners = new Set<ProgressListener>();
const logListeners = new Set<LogListener>();
let mockTimers: number[] = [];
let mockJobId = "";
let mockPercent = 0;
let mockArtifactPath = "";
let mockDryRun = false;
let mockArtifactVerified = false;
let mockDesktopCopyPath = "";
let mockCleanupPlanId = "";
let mockCleanupPlanSequence = 0;
let mockCleanupCompleted = false;
let mockCleanupInProgress = false;

function wailsApp(): WailsAppBinding | undefined {
  return window.go?.main?.App;
}

const backendMode = selectDesktopBackendMode(
  import.meta.env.DEV && import.meta.env.MODE === "preview",
  Boolean(wailsApp()),
  Boolean(window.runtime),
);

const unavailableMessage =
  "The GeneralsX desktop bridge did not load. Close and reopen the application; no build was started.";

function requireWailsApp(): WailsAppBinding {
  const app = wailsApp();
  if (backendMode !== "wails" || !app) {
    throw new Error(unavailableMessage);
  }
  return app;
}

function detectHost(): Pick<DesktopDefaults, "hostOS" | "hostArch"> {
  const platform = navigator.userAgent.toLowerCase();
  if (platform.includes("windows")) {
    return {hostOS: "windows", hostArch: "amd64"};
  }
  if (platform.includes("linux")) {
    return {hostOS: "linux", hostArch: "amd64"};
  }
  return {hostOS: "darwin", hostArch: "arm64"};
}

function previewDesktopCopyPath(artifactPath: string): string {
  const fileName = artifactPath.split(/[\\/]/).pop() || "GeneralsXZH-build";
  const host = detectHost();
  if (host.hostOS === "windows") {
    return `C:\\Users\\Commander\\Desktop\\${fileName}`;
  }
  return host.hostOS === "linux"
    ? `/home/commander/Desktop/${fileName}`
    : `/Users/commander/Desktop/${fileName}`;
}

function mockDefaults(): DesktopDefaults {
  const host = detectHost();
  const windows = host.hostOS === "windows";
  const home = windows
    ? "C:\\Users\\Commander"
    : host.hostOS === "linux"
      ? "/home/commander"
      : "/Users/commander";
  const target: BuildRequest["target"] =
    host.hostOS === "windows" ? "windows" : host.hostOS === "linux" ? "linux" : "macos";
  const outputName =
    target === "windows"
      ? "GeneralsXZH-windows-amd64-sfx.exe"
      : target === "macos"
        ? "GeneralsXZH-macos-arm64-sfx"
        : "GeneralsXZH-linux-amd64-sfx";
  const separator = windows ? "\\" : "/";
  const join = (...parts: string[]) => parts.join(separator);

  return {
    ...host,
    request: {
      ...emptyBuildRequest,
      target,
      repoRoot: join(home, "GeneralsX", "source"),
      assetsDir: join(home, "GeneralsX", "GeneralsZH"),
      cacheDir: join(home, ".cache", "GeneralsX", "builder"),
      steamCMDDir: join(home, ".cache", "GeneralsX", "builder", "steamcmd"),
      output: join(home, "GeneralsX", "source", "build", "sfx", outputName),
      appOutput:
        target === "macos"
          ? join(home, "GeneralsX", "source", "build", "sfx", "GeneralsXZH.app")
          : "",
    },
  };
}

function emitMockProgress(event: BuildProgressEvent): void {
  mockPercent = event.percent;
  progressListeners.forEach((listener) => listener(event));
}

function emitMockLog(event: BuildLogEvent): void {
  logListeners.forEach((listener) => listener(event));
}

function clearMockTimers(): void {
  mockTimers.forEach((timer) => window.clearTimeout(timer));
  mockTimers = [];
}

function scheduleMockBuild(jobId: string, request: BuildRequest): void {
  const buildsMacOSApplication = isMacOSBuild(request.target, detectHost().hostOS);
  const buildPhases: Array<Omit<BuildProgressEvent, "jobId"> & {log: string}> = [
    {
      phase: "prepare",
      status: "running",
      message: "Checking the host and build request",
      percent: 6,
      log: "Resolved the current host and private workspace.",
    },
    {
      phase: "source",
      status: "running",
      message: "Preparing the GeneralsX checkout",
      percent: 20,
      log: "Source checkout and recursive submodules are ready.",
    },
    {
      phase: "dependencies",
      status: "running",
      message: "Checking native build dependencies",
      percent: 38,
      log: "Native toolchain requirements passed preview validation.",
    },
    request.skipAssets
      ? {
          phase: "retail-assets",
          status: "running",
          message: "Validating owned Zero Hour files",
          percent: 55,
          log: "Existing retail asset inventory is complete.",
        }
      : {
          phase: "steamcmd",
          status: "running",
          message: "SteamCMD terminal handoff complete",
          percent: 55,
          log: "SteamCMD returned control without exposing credentials to the GUI.",
        },
    {
      phase: "game-build",
      status: "running",
      message: "Compiling GeneralsXZH",
      percent: 76,
      log: "Native game target linked successfully.",
    },
    {
      phase: "package",
      status: "running",
      message: buildsMacOSApplication
        ? "Packaging the branded macOS application"
        : "Packaging the self-extracting artifact",
      percent: 92,
      log: buildsMacOSApplication
        ? "Private retail payload packaged inside GeneralsXZH.app."
        : "Private retail payload staged for local packaging.",
    },
    {
      phase: "verify",
      status: "success",
      message: buildsMacOSApplication
        ? "Build and macOS application verification complete"
        : "Build and artifact verification complete",
      percent: 100,
      exitCode: 0,
      log: "Artifact checks passed. Preview build complete.",
    },
  ];
  const dryRunPhases: Array<Omit<BuildProgressEvent, "jobId"> & {log: string}> = [
    buildPhases[0]!,
    buildPhases[1]!,
    {
      phase: "plan",
      status: "running",
      message: "Planning external actions without changing the host",
      percent: 72,
      log: "Dry-run plan generated without invoking SteamCMD.",
    },
    {
      phase: "verify",
      status: "success",
      message: "Dry-run plan complete",
      percent: 100,
      exitCode: 0,
      log: "Preview dry run complete. No artifact was written.",
    },
  ];
  const phases = request.dryRun ? dryRunPhases : buildPhases;

  phases.forEach((phase, index) => {
    const timer = window.setTimeout(() => {
      emitMockLog({jobId, stream: "stdout", text: phase.log});
      const {log: _log, ...progress} = phase;
      if (jobId === mockJobId && progress.status === "success" && !request.dryRun) {
        mockArtifactVerified = true;
      }
      emitMockProgress({jobId, ...progress});
    }, 500 + index * 700);
    mockTimers.push(timer);
  });
}

function subscribeWailsEvent<T>(eventName: string, callback: (event: T) => void): (() => void) | null {
  const runtime = window.runtime;
  if (!runtime) {
    return null;
  }
  const unsubscribe = runtime.EventsOn(eventName, (payload) => callback(payload as T));
  return typeof unsubscribe === "function" ? unsubscribe : () => undefined;
}

export const desktopBackend = {
  isMock(): boolean {
    return backendMode === "preview";
  },

  async getDefaults(): Promise<DesktopDefaults> {
    return backendMode === "preview" ? mockDefaults() : requireWailsApp().GetDefaults();
  },

  async chooseDirectory(kind: DirectoryKind, current: string): Promise<string> {
    if (backendMode === "wails") {
      return requireWailsApp().ChooseDirectory(kind, current);
    }
    if (backendMode === "unavailable") {
      throw new Error(unavailableMessage);
    }
    const previewCurrent =
      kind === "output" || kind === "appOutput" ? current.replace(/[\\/][^\\/]*$/, "") : current;
    return window.prompt("Preview the selected directory", previewCurrent) ?? "";
  },

  async validateBuild(request: BuildRequest, legalAcknowledged: boolean): Promise<ValidationIssue[]> {
    const localIssues = validateAllSteps(request, legalAcknowledged, detectHost().hostOS);
    if (localIssues.length > 0) {
      return localIssues;
    }
    return backendMode === "preview" ? [] : requireWailsApp().ValidateBuild(request);
  },

  async startBuild(request: BuildRequest): Promise<string> {
    if (backendMode === "wails") {
      return requireWailsApp().StartBuild(request);
    }
    if (backendMode === "unavailable") {
      throw new Error(unavailableMessage);
    }
    clearMockTimers();
    mockPercent = 0;
    mockJobId = `preview-${Date.now()}`;
    // GeneralsX @feature Codex 05/08/2026 Preview the same primary artifact users receive from a native build.
    mockArtifactPath = primaryArtifactPath(request, detectHost().hostOS);
    mockDryRun = request.dryRun;
    mockArtifactVerified = false;
    mockDesktopCopyPath = "";
    mockCleanupPlanId = "";
    mockCleanupCompleted = false;
    mockCleanupInProgress = false;
    scheduleMockBuild(mockJobId, request);
    return mockJobId;
  },

  async copyBuildArtifactToDesktop(jobId: string): Promise<string> {
    if (backendMode === "wails") {
      return requireWailsApp().CopyBuildArtifactToDesktop(jobId);
    }
    if (backendMode === "unavailable") {
      throw new Error(unavailableMessage);
    }
    if (
      jobId !== mockJobId ||
      mockDryRun ||
      !mockArtifactPath ||
      !mockArtifactVerified ||
      mockCleanupCompleted ||
      mockCleanupInProgress
    ) {
      throw new Error("No verified build artifact is available for this preview build.");
    }
    await new Promise((resolve) => window.setTimeout(resolve, 400));
    mockDesktopCopyPath = previewDesktopCopyPath(mockArtifactPath);
    return mockDesktopCopyPath;
  },

  // GeneralsX @feature Codex 05/08/2026 Preview the same explicit cleanup plan required by the native backend.
  async getBuildCleanupPlan(jobId: string): Promise<BuildCleanupPlan> {
    if (backendMode === "wails") {
      return requireWailsApp().GetBuildCleanupPlan(jobId);
    }
    if (backendMode === "unavailable") {
      throw new Error(unavailableMessage);
    }
    if (
      jobId !== mockJobId ||
      !mockArtifactVerified ||
      !mockDesktopCopyPath ||
      mockCleanupCompleted ||
      mockCleanupInProgress
    ) {
      throw new Error("No copied build artifact is available for cleanup in this preview build.");
    }
    await new Promise((resolve) => window.setTimeout(resolve, 300));
    if (
      jobId !== mockJobId ||
      !mockArtifactVerified ||
      !mockDesktopCopyPath ||
      mockCleanupCompleted ||
      mockCleanupInProgress
    ) {
      throw new Error("No copied build artifact is available for cleanup in this preview build.");
    }
    mockCleanupPlanId = `${jobId}-cleanup-${++mockCleanupPlanSequence}`;
    return {
      jobId,
      planId: mockCleanupPlanId,
      desktopCopyPath: mockDesktopCopyPath,
      entries: [{label: "Generated build artifact", path: mockArtifactPath}],
    };
  },

  // GeneralsX @feature Codex 05/08/2026 Simulate cleanup without removing the preserved preview Desktop copy.
  async cleanupBuild(jobId: string, planId: string): Promise<string> {
    if (backendMode === "wails") {
      return requireWailsApp().CleanupBuild(jobId, planId);
    }
    if (backendMode === "unavailable") {
      throw new Error(unavailableMessage);
    }
    if (!planId || planId !== mockCleanupPlanId) {
      throw new Error("The cleanup plan has expired or does not match this preview build. Review cleanup again.");
    }
    if (
      jobId !== mockJobId ||
      !mockArtifactVerified ||
      !mockDesktopCopyPath ||
      mockCleanupCompleted ||
      mockCleanupInProgress
    ) {
      throw new Error("No copied build artifact is available for cleanup in this preview build.");
    }
    mockCleanupInProgress = true;
    mockCleanupPlanId = "";
    try {
      await new Promise((resolve) => window.setTimeout(resolve, 500));
      mockCleanupCompleted = true;
      mockArtifactVerified = false;
      return "Removed the generated build artifact from the preview build workspace.";
    } finally {
      mockCleanupInProgress = false;
    }
  },

  async cancelBuild(): Promise<boolean> {
    if (backendMode === "wails") {
      return requireWailsApp().CancelBuild();
    }
    if (backendMode === "unavailable") {
      throw new Error(unavailableMessage);
    }
    if (!mockJobId) {
      return false;
    }
    clearMockTimers();
    mockArtifactVerified = false;
    mockDesktopCopyPath = "";
    mockCleanupPlanId = "";
    mockCleanupCompleted = false;
    mockCleanupInProgress = false;
    emitMockLog({jobId: mockJobId, stream: "stderr", text: "Preview build cancelled by the user."});
    emitMockProgress({
      jobId: mockJobId,
      phase: "cancelled",
      status: "cancelled",
      message: "Build cancelled",
      percent: mockPercent,
    });
    mockJobId = "";
    return true;
  },

  onProgress(listener: ProgressListener): () => void {
    if (backendMode === "wails") {
      return subscribeWailsEvent<BuildProgressEvent>("builder:progress", listener) ?? (() => undefined);
    }
    if (backendMode === "preview") {
      progressListeners.add(listener);
      return () => progressListeners.delete(listener);
    }
    return () => undefined;
  },

  onLog(listener: LogListener): () => void {
    if (backendMode === "wails") {
      return subscribeWailsEvent<BuildLogEvent>("builder:log", listener) ?? (() => undefined);
    }
    if (backendMode === "preview") {
      logListeners.add(listener);
      return () => logListeners.delete(listener);
    }
    return () => undefined;
  },
};
