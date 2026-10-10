import {describe, expect, it} from "vitest";

import {
  applyDirectorySelection,
  emptyBuildRequest,
  isMacOSBuild,
  normalizeBuildRequest,
  primaryArtifactPath,
  prebuiltOnlineEndpoint,
  selectBuildMode,
  validateWizardStep,
} from "./request";

describe("normalizeBuildRequest", () => {
  it("trims supported text fields and clears target-specific values", () => {
    const normalized = normalizeBuildRequest({
      ...emptyBuildRequest,
      repoRoot: "  /tmp/source  ",
      target: "linux",
      appOutput: " /tmp/GeneralsXZH.app ",
      keepWindowsStage: true,
      onlineServerSource: " /tmp/server ",
    });

    expect(normalized.repoRoot).toBe("/tmp/source");
    expect(normalized.appOutput).toBe("");
    expect(normalized.keepWindowsStage).toBe(false);
    expect(normalized.onlineServerSource).toBe("");
  });

  it("never introduces Steam passwords or guard codes", () => {
    const normalized = normalizeBuildRequest({...emptyBuildRequest, steamUser: " commander "});
    const keys = Object.keys(normalized);

    expect(normalized.steamUser).toBe("commander");
    expect(keys).not.toContain("steamPassword");
    expect(keys).not.toContain("steamGuardCode");
  });
});

describe("prebuilt mode", () => {
  it("keeps source as the default and clears incompatible options when selected", () => {
    expect(emptyBuildRequest.buildMode).toBe("source");
    const source = {...emptyBuildRequest, target: "linux" as const, withOnlineServer: true, skipGameBuild: true,
      onlineEndpoint: "tls://private.example.net", acceptSDKLicenses: true, keepWindowsStage: true};
    const prebuilt = selectBuildMode(source, "prebuilt", "darwin");
    expect(prebuilt).toMatchObject({buildMode: "prebuilt", target: "auto", withOnlineServer: false,
      skipGameBuild: false, onlineEndpoint: prebuiltOnlineEndpoint, acceptSDKLicenses: false, keepWindowsStage: false});
    expect(source.withOnlineServer).toBe(true);
    expect(selectBuildMode(prebuilt, "source", "darwin").buildMode).toBe("source");
  });

  it("requires local assets and output without requiring a source checkout", () => {
    const request = {...emptyBuildRequest, buildMode: "prebuilt" as const, sourceRepo: "", sourceRef: ""};
    expect(validateWizardStep(1, request, false, "linux").map((issue) => issue.field)).toEqual(["assetsDir", "output"]);
    expect(validateWizardStep(2, request)).toEqual([]);
  });

  it("validates release selectors and prevents source-only customization", () => {
    const request = {...emptyBuildRequest, buildMode: "prebuilt" as const};
    for (const runtimeVersion of ["stable", "runtime-v1.2.3", "runtime-v1.2.3-rc.1"]) {
      expect(validateWizardStep(2, {...request, runtimeVersion})).toEqual([]);
    }
    for (const runtimeVersion of ["", "main", "https://example.com/payload", "../runtime-v1.2.3"]) {
      expect(validateWizardStep(2, {...request, runtimeVersion})).toContainEqual(expect.objectContaining({field: "runtimeVersion"}));
    }
    expect(validateWizardStep(2, {...request, runtimeRepository: "https://github.com/a/b"}))
      .toContainEqual(expect.objectContaining({field: "runtimeRepository"}));
    expect(validateWizardStep(2, {...request, onlineEndpoint: "tls://private.example.net"}))
      .toContainEqual(expect.objectContaining({field: "onlineEndpoint"}));
    expect(validateWizardStep(2, {...request, withOnlineServer: true}))
      .toContainEqual(expect.objectContaining({field: "buildMode"}));
  });
});

describe("validateWizardStep", () => {
  it("requires the file paths needed by the game-files step", () => {
    const issues = validateWizardStep(1, {...emptyBuildRequest, target: "macos"}, false, "darwin");

    expect(issues.map((issue) => issue.field)).toEqual(["repoRoot", "assetsDir", "appOutput"]);
  });

  it("requires only the platform's primary artifact path", () => {
    const macRequest = {
      ...emptyBuildRequest,
      target: "auto" as const,
      repoRoot: "/source",
      assetsDir: "/assets",
      appOutput: "/output/GeneralsXZH.app",
    };
    expect(validateWizardStep(1, macRequest, false, "darwin")).toEqual([]);

    const linuxIssues = validateWizardStep(1, {...macRequest, appOutput: ""}, false, "linux");
    expect(linuxIssues).toEqual([
      {field: "output", message: "Choose an SFX output path."},
    ]);
  });

  it("rejects malformed Online endpoints", () => {
    const issues = validateWizardStep(2, {
      ...emptyBuildRequest,
      onlineEndpoint: "https://online.example.net/path",
    });

    expect(issues).toContainEqual(
      expect.objectContaining({field: "onlineEndpoint"}),
    );
  });

  it("requires the retail ownership acknowledgement on review", () => {
    expect(validateWizardStep(3, emptyBuildRequest, false)).toHaveLength(1);
    expect(validateWizardStep(3, emptyBuildRequest, true)).toHaveLength(0);
  });
});

describe("primaryArtifactPath", () => {
  const request = {
    ...emptyBuildRequest,
    output: "/output/GeneralsXZH-sfx",
    appOutput: "/output/GeneralsXZH.app",
  };

  it("selects the branded application for explicit and automatic macOS builds", () => {
    expect(isMacOSBuild("macos", "linux")).toBe(true);
    expect(isMacOSBuild("auto", "darwin")).toBe(true);
    expect(primaryArtifactPath({...request, target: "macos"}, "darwin")).toBe(request.appOutput);
    expect(primaryArtifactPath({...request, target: "auto"}, "darwin")).toBe(request.appOutput);
  });

  it("preserves SFX output for Linux and Windows builds", () => {
    expect(isMacOSBuild("auto", "linux")).toBe(false);
    expect(primaryArtifactPath({...request, target: "linux"}, "linux")).toBe(request.output);
    expect(primaryArtifactPath({...request, target: "windows"}, "windows")).toBe(request.output);
  });
});

describe("applyDirectorySelection", () => {
  it("preserves an output filename when the backend returns a directory", () => {
    expect(applyDirectorySelection("output", "/old/GeneralsXZH-sfx", "/new/output")).toBe(
      "/new/output/GeneralsXZH-sfx",
    );
    expect(applyDirectorySelection("appOutput", "C:\\old\\GeneralsXZH.app", "C:\\new")).toBe(
      "C:\\new\\GeneralsXZH.app",
    );
  });

  it("uses directory selections directly for non-output paths", () => {
    expect(applyDirectorySelection("assetsDir", "/old/assets", "/new/assets")).toBe("/new/assets");
  });
});
