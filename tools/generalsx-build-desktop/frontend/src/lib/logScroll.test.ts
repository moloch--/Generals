import {describe, expect, it} from "vitest";

import {
  DEFAULT_FOLLOW_LOG_TAIL,
  isAtLogTail,
  scrollLogToTail,
} from "./logScroll";

describe("Build Activity log scrolling", () => {
  it("follows streamed output by default", () => {
    expect(DEFAULT_FOLLOW_LOG_TAIL).toBe(true);
  });

  it("treats non-overflowing and exact-bottom content as the tail", () => {
    expect(isAtLogTail({clientHeight: 300, scrollHeight: 200, scrollTop: 0})).toBe(true);
    expect(isAtLogTail({clientHeight: 300, scrollHeight: 500, scrollTop: 200})).toBe(true);
  });

  it("allows fractional layout distance at the tail", () => {
    expect(isAtLogTail({clientHeight: 300, scrollHeight: 500, scrollTop: 192.5})).toBe(true);
  });

  it("pauses following during deliberate scrollback and resumes at the tail", () => {
    expect(isAtLogTail({clientHeight: 300, scrollHeight: 500, scrollTop: 180})).toBe(false);
    expect(isAtLogTail({clientHeight: 300, scrollHeight: 500, scrollTop: 199})).toBe(true);
  });

  it("moves to the newest output only while following is enabled", () => {
    const paused = {scrollHeight: 640, scrollTop: 125};
    expect(scrollLogToTail(paused, false)).toBe(false);
    expect(paused.scrollTop).toBe(125);

    const following = {scrollHeight: 640, scrollTop: 125};
    expect(scrollLogToTail(following, true)).toBe(true);
    expect(following.scrollTop).toBe(640);
  });
});
