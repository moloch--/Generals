import {describe, expect, it} from "vitest";

import {selectPostBuildActions} from "./postBuildActions";

describe("selectPostBuildActions", () => {
  it("offers Copy only after a real successful build", () => {
    expect(selectPostBuildActions("success", false, "idle", "idle")).toMatchObject({
      showCopy: true,
      showCleanup: false,
      canReset: true,
    });
    expect(selectPostBuildActions("success", true, "idle", "idle").showCopy).toBe(false);
    expect(selectPostBuildActions("error", false, "idle", "idle").showCopy).toBe(false);
  });

  it("blocks reset while Copy is pending", () => {
    expect(selectPostBuildActions("success", false, "pending", "idle")).toMatchObject({
      showCleanup: false,
      canReset: false,
      isBusy: true,
    });
  });

  it("offers Cleanup only after the Desktop copy succeeds", () => {
    expect(selectPostBuildActions("success", false, "copied", "idle")).toMatchObject({
      showCleanup: true,
      copyDisabled: true,
      cleanupDisabled: false,
      canReset: true,
    });
  });

  it.each(["planning", "pending"] as const)("blocks Copy and reset while Cleanup is %s", (status) => {
    expect(selectPostBuildActions("success", false, "copied", status)).toMatchObject({
      showCleanup: true,
      copyDisabled: true,
      canReset: false,
      isBusy: true,
    });
  });

  it("keeps completed cleanup visible but unavailable", () => {
    expect(selectPostBuildActions("success", false, "copied", "cleaned")).toMatchObject({
      showCleanup: true,
      copyDisabled: true,
      cleanupDisabled: true,
      canReset: true,
      isBusy: false,
    });
  });

  it("makes a cleanup error retryable", () => {
    expect(selectPostBuildActions("success", false, "copied", "error")).toMatchObject({
      showCleanup: true,
      cleanupDisabled: false,
      canReset: true,
      isBusy: false,
    });
  });
});
