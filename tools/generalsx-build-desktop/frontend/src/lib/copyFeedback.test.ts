import {describe, expect, it} from "vitest";

import type {CopyFeedback} from "./copyFeedback";
import {canResetAfterCopy, runDesktopCopy} from "./copyFeedback";

describe("runDesktopCopy", () => {
  it("reports pending before the copied destination", async () => {
    const states: CopyFeedback[] = [];
    let finishCopy: (destination: string) => void = () => undefined;
    const copy = new Promise<string>((resolve) => {
      finishCopy = resolve;
    });

    const operation = runDesktopCopy(() => copy, (feedback) => states.push(feedback));
    expect(states).toEqual([{status: "pending", message: ""}]);
    finishCopy("/Users/commander/Desktop/GeneralsXZH-sfx");
    await operation;

    expect(states).toEqual([
      {status: "pending", message: ""},
      {status: "copied", message: "/Users/commander/Desktop/GeneralsXZH-sfx"},
    ]);
  });

  it("reports a retryable copy error", async () => {
    const states: CopyFeedback[] = [];

    await runDesktopCopy(
      () => Promise.reject(new Error("Desktop is unavailable")),
      (feedback) => states.push(feedback),
    );

    expect(states).toEqual([
      {status: "pending", message: ""},
      {status: "error", message: "Desktop is unavailable"},
    ]);
  });

  it("blocks reset only while a copy is pending", () => {
    expect(canResetAfterCopy("idle")).toBe(true);
    expect(canResetAfterCopy("pending")).toBe(false);
    expect(canResetAfterCopy("copied")).toBe(true);
    expect(canResetAfterCopy("error")).toBe(true);
  });
});
