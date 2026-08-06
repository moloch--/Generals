import type {BuildRequest, DirectoryKind, ValidationIssue} from "../types";

export const emptyBuildRequest: BuildRequest = {
  repoRoot: "",
  sourceRepo: "https://github.com/moloch--/Generals.git",
  sourceRef: "main",
  target: "auto",
  assetsDir: "",
  steamUser: "",
  cacheDir: "",
  steamCMDDir: "",
  output: "",
  appOutput: "",
  installDeps: true,
  acceptSDKLicenses: false,
  withOnlineServer: false,
  onlineServerSource: "",
  onlineServerRepo: "https://github.com/moloch--/generals-server.git",
  onlineServerRef: "main",
  onlineEndpoint: "",
  skipAssets: false,
  skipGameBuild: false,
  dryRun: false,
  nonInteractive: false,
  keepWindowsStage: false,
};

const stringFields = [
  "repoRoot",
  "sourceRepo",
  "sourceRef",
  "assetsDir",
  "steamUser",
  "cacheDir",
  "steamCMDDir",
  "output",
  "appOutput",
  "onlineServerSource",
  "onlineServerRepo",
  "onlineServerRef",
  "onlineEndpoint",
] as const satisfies readonly (keyof BuildRequest)[];

export function normalizeBuildRequest(request: BuildRequest): BuildRequest {
  const normalized = {...request};

  for (const field of stringFields) {
    normalized[field] = request[field].trim();
  }

  if (!normalized.withOnlineServer) {
    normalized.onlineServerSource = "";
  }
  if (normalized.target === "linux" || normalized.target === "windows") {
    normalized.appOutput = "";
  }
  if (normalized.target !== "windows") {
    normalized.keepWindowsStage = false;
  }

  return normalized;
}

// GeneralsX @feature Codex 05/08/2026 Present the branded app as the primary artifact for macOS GUI builds.
export function isMacOSBuild(target: BuildRequest["target"], hostOS: string): boolean {
  return target === "macos" || (target === "auto" && hostOS === "darwin");
}

export function primaryArtifactPath(request: BuildRequest, hostOS: string): string {
  return isMacOSBuild(request.target, hostOS) ? request.appOutput : request.output;
}

export function isOnlineEndpointValid(value: string): boolean {
  if (value === "") {
    return true;
  }

  return /^(?:tls:\/\/)?(?:[a-zA-Z0-9](?:[a-zA-Z0-9.-]*[a-zA-Z0-9])?)(?::(?:[1-9]\d{0,3}|[1-5]\d{4}|6[0-4]\d{3}|65[0-4]\d{2}|655[0-2]\d|6553[0-5]))?$/.test(
    value,
  );
}

export function validateWizardStep(
  step: number,
  request: BuildRequest,
  legalAcknowledged = false,
  hostOS = "",
): ValidationIssue[] {
  const issues: ValidationIssue[] = [];

  if (step === 0 && !["auto", "macos", "linux", "windows"].includes(request.target)) {
    issues.push({field: "target", message: "Choose a supported build target."});
  }

  if (step === 1) {
    if (!request.repoRoot.trim()) {
      issues.push({field: "repoRoot", message: "Choose a source checkout or clone destination."});
    }
    if (!request.assetsDir.trim()) {
      issues.push({field: "assetsDir", message: "Choose the retail Zero Hour data directory."});
    }
    if (isMacOSBuild(request.target, hostOS) && !request.appOutput.trim()) {
      issues.push({field: "appOutput", message: "Choose a macOS application output path."});
    } else if (!isMacOSBuild(request.target, hostOS) && !request.output.trim()) {
      issues.push({field: "output", message: "Choose an SFX output path."});
    }
  }

  if (step === 2) {
    if (!request.sourceRepo.trim() || !request.sourceRef.trim()) {
      issues.push({field: "sourceRepo", message: "Source repository and ref are required."});
    }
    if (!isOnlineEndpointValid(request.onlineEndpoint.trim())) {
      issues.push({
        field: "onlineEndpoint",
        message: "Use a DNS name or IPv4 address, optional port, and optional tls:// prefix.",
      });
    }
    if (request.withOnlineServer && !request.onlineServerRepo.trim() && !request.onlineServerSource.trim()) {
      issues.push({
        field: "onlineServerSource",
        message: "Choose an Online server checkout or repository.",
      });
    }
    if (request.skipGameBuild && request.onlineEndpoint.trim()) {
      issues.push({
        field: "skipGameBuild",
        message: "Reusing a game build cannot compile a new Online endpoint.",
      });
    }
  }

  if (step === 3 && !legalAcknowledged) {
    issues.push({
      field: "legalAcknowledged",
      message: "Confirm that the retail files are owned and the artifact will remain private.",
    });
  }

  return issues;
}

export function validateAllSteps(
  request: BuildRequest,
  legalAcknowledged: boolean,
  hostOS = "",
): ValidationIssue[] {
  return [0, 1, 2, 3].flatMap((step) => validateWizardStep(step, request, legalAcknowledged, hostOS));
}

export function targetLabel(target: BuildRequest["target"]): string {
  switch (target) {
    case "macos":
      return "macOS · Apple Silicon";
    case "linux":
      return "Linux · x86-64";
    case "windows":
      return "Windows · x86";
    default:
      return "Current host";
  }
}

export function formatPhase(phase: string): string {
  return phase
    .split(/[-_]/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

export function applyDirectorySelection(
  kind: DirectoryKind,
  currentValue: string,
  selectedDirectory: string,
): string {
  if (kind !== "output" && kind !== "appOutput") {
    return selectedDirectory;
  }

  const currentParts = currentValue.split(/[\\/]/);
  const fallbackName = kind === "appOutput" ? "GeneralsXZH.app" : "GeneralsXZH-sfx";
  const fileName = currentParts.at(-1)?.trim() || fallbackName;
  const separator = selectedDirectory.includes("\\") && !selectedDirectory.includes("/") ? "\\" : "/";

  return `${selectedDirectory.replace(/[\\/]+$/, "")}${separator}${fileName}`;
}
