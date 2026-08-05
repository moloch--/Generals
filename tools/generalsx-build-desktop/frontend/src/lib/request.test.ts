import {describe, expect, it} from "vitest";

import {
  applyDirectorySelection,
  emptyBuildRequest,
  normalizeBuildRequest,
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

describe("validateWizardStep", () => {
  it("requires the file paths needed by the game-files step", () => {
    const issues = validateWizardStep(1, {...emptyBuildRequest, target: "macos"});

    expect(issues.map((issue) => issue.field)).toEqual(["repoRoot", "assetsDir", "output", "appOutput"]);
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
