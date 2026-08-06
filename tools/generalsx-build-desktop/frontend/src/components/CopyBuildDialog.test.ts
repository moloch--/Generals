import {describe, expect, it} from "vitest";

import {copyProgressPresentation} from "./CopyBuildDialog";
import {beginDesktopCopy} from "../lib/copyFeedback";
import type {CopyFeedback} from "../lib/copyFeedback";

function feedback(overrides: Partial<CopyFeedback> = {}): CopyFeedback {
  return {
    ...beginDesktopCopy("job-1", "copy-1"),
    phase: "copying",
    bytesCopied: 50,
    totalBytes: 100,
    percent: 50,
    ...overrides,
  };
}

describe("CopyBuildDialog progress presentation", () => {
  it("shows determinate byte progress while copying", () => {
    expect(copyProgressPresentation(feedback())).toEqual({
      isIndeterminate: false,
      valueLabel: "50 B of 100 B",
    });
  });

  it.each(["verifying", "publishing"] as const)(
    "keeps copied bytes visible while %s remains indeterminate",
    (phase) => {
      expect(copyProgressPresentation(feedback({
        phase,
        bytesCopied: 100,
        percent: 100,
      }))).toEqual({
        isIndeterminate: true,
        valueLabel: "100 B of 100 B",
      });
    },
  );

  it("returns to a determinate completed bar after the terminal success", () => {
    expect(copyProgressPresentation(feedback({
      status: "copied",
      phase: "complete",
      bytesCopied: 100,
      percent: 100,
    }))).toEqual({
      isIndeterminate: false,
      valueLabel: "100 B of 100 B",
    });
  });
});
