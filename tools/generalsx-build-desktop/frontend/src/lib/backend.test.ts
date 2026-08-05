import {describe, expect, it} from "vitest";

import {selectDesktopBackendMode} from "./backendMode";

describe("selectDesktopBackendMode", () => {
  it("uses Wails only when both generated bindings are ready", () => {
    expect(selectDesktopBackendMode(false, true, true)).toBe("wails");
    expect(selectDesktopBackendMode(true, true, true)).toBe("wails");
  });

  it("allows preview data only when explicit preview mode has no partial bridge", () => {
    expect(selectDesktopBackendMode(true, false, false)).toBe("preview");
    expect(selectDesktopBackendMode(true, true, false)).toBe("unavailable");
    expect(selectDesktopBackendMode(true, false, true)).toBe("unavailable");
  });

  it("makes a missing production bridge fatal", () => {
    expect(selectDesktopBackendMode(false, false, false)).toBe("unavailable");
    expect(selectDesktopBackendMode(false, true, false)).toBe("unavailable");
    expect(selectDesktopBackendMode(false, false, true)).toBe("unavailable");
  });
});
