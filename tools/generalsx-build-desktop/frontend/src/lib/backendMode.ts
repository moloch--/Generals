export type DesktopBackendMode = "wails" | "preview" | "unavailable";

// GeneralsX @bugfix Codex 05/08/2026 Never let a packaged binding failure fall back to a simulated successful build.
export function selectDesktopBackendMode(
  previewEnabled: boolean,
  hasAppBinding: boolean,
  hasRuntimeBinding: boolean,
): DesktopBackendMode {
  if (hasAppBinding && hasRuntimeBinding) {
    return "wails";
  }
  if (previewEnabled && !hasAppBinding && !hasRuntimeBinding) {
    return "preview";
  }
  return "unavailable";
}
