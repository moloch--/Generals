import {describe, expect, it} from "vitest";

import type {BuildCleanupPlan} from "../types";
import type {CleanupFeedback} from "./cleanupFeedback";
import {
  canDismissCleanup,
  loadCleanupPlan,
  runBuildCleanup,
  selectCleanupPlanState,
} from "./cleanupFeedback";

const plan: BuildCleanupPlan = {
  jobId: "job-fixture",
  planId: "cleanup-plan-fixture",
  desktopCopyPath: "/Users/commander/Desktop/GeneralsXZH-sfx",
  entries: [{label: "Generated SFX artifact", path: "/tmp/build/GeneralsXZH-sfx"}],
};

describe("loadCleanupPlan", () => {
  it("reports planning before the authorized cleanup plan", async () => {
    const states: CleanupFeedback[] = [];
    const result = await loadCleanupPlan(
      () => Promise.resolve(plan),
      (feedback) => states.push(feedback),
    );

    expect(result).toEqual(plan);
    expect(states).toEqual([
      {status: "planning", message: "", plan: null},
      {status: "ready", message: "", plan},
    ]);
  });

  it("reports a retryable plan error", async () => {
    const states: CleanupFeedback[] = [];
    const result = await loadCleanupPlan(
      () => Promise.reject("cleanup plan unavailable"),
      (feedback) => states.push(feedback),
    );

    expect(result).toBeNull();
    expect(states.at(-1)).toEqual({status: "error", message: "cleanup plan unavailable", plan: null});
  });
});

describe("cleanup confirmation policy", () => {
  it("distinguishes unavailable, empty, and actionable plans", () => {
    expect(selectCleanupPlanState(null)).toBe("unavailable");
    expect(selectCleanupPlanState({...plan, entries: []})).toBe("empty");
    expect(selectCleanupPlanState(plan)).toBe("ready");
  });

  it("allows dismissal except while cleanup is pending", () => {
    expect(canDismissCleanup("idle")).toBe(true);
    expect(canDismissCleanup("planning")).toBe(true);
    expect(canDismissCleanup("ready")).toBe(true);
    expect(canDismissCleanup("pending")).toBe(false);
    expect(canDismissCleanup("cleaned")).toBe(true);
    expect(canDismissCleanup("error")).toBe(true);
  });
});

describe("runBuildCleanup", () => {
  it("reports pending before cleanup succeeds", async () => {
    const states: CleanupFeedback[] = [];
    let confirmedPlanId = "";
    await runBuildCleanup(
      () => {
        confirmedPlanId = plan.planId;
        return Promise.resolve("Removed the completed build files.");
      },
      plan,
      (feedback) => states.push(feedback),
    );

    expect(confirmedPlanId).toBe("cleanup-plan-fixture");
    expect(states).toEqual([
      {status: "pending", message: "", plan},
      {status: "cleaned", message: "Removed the completed build files.", plan},
    ]);
  });

  it("keeps the plan while reporting a retryable cleanup error", async () => {
    const states: CleanupFeedback[] = [];
    await runBuildCleanup(
      () => Promise.reject(new Error("file is busy")),
      plan,
      (feedback) => states.push(feedback),
    );

    expect(states).toEqual([
      {status: "pending", message: "", plan},
      {status: "error", message: "file is busy", plan},
    ]);
  });
});
