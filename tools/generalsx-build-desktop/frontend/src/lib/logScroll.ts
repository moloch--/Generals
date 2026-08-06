export const DEFAULT_FOLLOW_LOG_TAIL = true;
export const LOG_TAIL_TOLERANCE = 8;

export interface LogScrollViewport {
  clientHeight: number;
  scrollHeight: number;
  scrollTop: number;
}

export function isAtLogTail(
  viewport: LogScrollViewport,
  tolerance = LOG_TAIL_TOLERANCE,
): boolean {
  return viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight <= tolerance;
}

export function scrollLogToTail(
  viewport: Pick<LogScrollViewport, "scrollHeight" | "scrollTop">,
  followTail: boolean,
): boolean {
  if (!followTail) {
    return false;
  }
  viewport.scrollTop = viewport.scrollHeight;
  return true;
}
