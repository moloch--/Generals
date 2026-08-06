import {describe, expect, it} from "vitest";

import type {ExecutionState} from "../types";
import {canCopyBuildArtifact, selectFinalStepPane} from "./execution";

describe("selectFinalStepPane", () => {
  it("shows the build review only while execution is idle", () => {
    expect(selectFinalStepPane("idle")).toBe("review");
  });

  it.each<ExecutionState>([
    "validating",
    "running",
    "cancelling",
    "success",
    "error",
    "cancelled",
  ])("shows build activity while execution is %s", (execution) => {
    expect(selectFinalStepPane(execution)).toBe("activity");
  });
});

describe("canCopyBuildArtifact", () => {
  it("allows Desktop copying after a real successful build", () => {
    expect(canCopyBuildArtifact("success", false)).toBe(true);
  });

  it.each<[ExecutionState, boolean]>([
    ["idle", false],
    ["validating", false],
    ["running", false],
    ["cancelling", false],
    ["error", false],
    ["cancelled", false],
    ["success", true],
  ])("does not offer Desktop copying for %s with dryRun=%s", (execution, dryRun) => {
    expect(canCopyBuildArtifact(execution, dryRun)).toBe(false);
  });
});
