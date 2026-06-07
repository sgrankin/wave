// Opt-in, dependency-free debug tracing for the client. Off by default; the
// editor enables it for `?debug=1`. Kept tiny and runtime-agnostic so it works in
// both the browser and Node (the integration test can flip it on too).
//
// This is the permanent home of the ad-hoc console instrumentation that diagnosed
// the controlled-editor submit bug: the delta-flow trace (submitWith → cc.edit →
// sendDelta → ack) is the single most useful client-side signal, so it lives here
// behind a flag instead of being re-added by hand each time.

let enabled = false;

/** Turn debug tracing on/off (the editor calls this for `?debug=1`). */
export function setDebug(on: boolean): void {
  enabled = on;
}

/** Whether debug tracing is on (gate for exposing introspection hooks). */
export function debugEnabled(): boolean {
  return enabled;
}

/** Log a debug line if tracing is on. Prefixed so it is easy to filter. */
export function dlog(...args: unknown[]): void {
  if (enabled) {
    // eslint-disable-next-line no-console
    console.debug("[wave]", ...args);
  }
}
