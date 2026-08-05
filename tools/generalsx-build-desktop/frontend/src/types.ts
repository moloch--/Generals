export type BuildTarget = "auto" | "macos" | "linux" | "windows";

export interface BuildRequest {
  repoRoot: string;
  sourceRepo: string;
  sourceRef: string;
  target: BuildTarget;
  assetsDir: string;
  steamUser: string;
  cacheDir: string;
  steamCMDDir: string;
  output: string;
  appOutput: string;
  installDeps: boolean;
  acceptSDKLicenses: boolean;
  withOnlineServer: boolean;
  onlineServerSource: string;
  onlineServerRepo: string;
  onlineServerRef: string;
  onlineEndpoint: string;
  skipAssets: boolean;
  skipGameBuild: boolean;
  dryRun: boolean;
  nonInteractive: boolean;
  keepWindowsStage: boolean;
}

export type BuildRequestUpdater = <K extends keyof BuildRequest>(field: K, value: BuildRequest[K]) => void;

export interface DesktopDefaults {
  hostOS: string;
  hostArch: string;
  request: BuildRequest;
}

export type DirectoryKind =
  | "repoRoot"
  | "assetsDir"
  | "cacheDir"
  | "steamCMDDir"
  | "output"
  | "appOutput"
  | "onlineServerSource";

export interface ValidationIssue {
  field: string;
  message: string;
  severity?: "error" | "warning";
}

export type BuildProgressStatus = "queued" | "running" | "success" | "error" | "cancelled";

export interface BuildProgressEvent {
  jobId: string;
  phase: string;
  status: BuildProgressStatus;
  message: string;
  percent: number;
  exitCode?: number;
}

export interface BuildLogEvent {
  jobId: string;
  stream: "stdout" | "stderr";
  text: string;
}

export type ExecutionState =
  | "idle"
  | "validating"
  | "running"
  | "cancelling"
  | "success"
  | "error"
  | "cancelled";

export const wizardSteps = [
  {title: "Target", description: "Choose the native platform"},
  {title: "Game Files", description: "Source, retail data, and output"},
  {title: "Options", description: "Select build behavior"},
  {title: "Review", description: "Confirm ownership and build"},
] as const;
