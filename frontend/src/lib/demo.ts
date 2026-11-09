// Calls into the Go DemoService (internal/app/services/demo): the reference
// streaming operation. See docs/events.md.
import {
  DemoService,
  type ItemDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/demo";
import type { CancellablePromise } from "./calls";
import type { StartedDTO } from "./operations";

export type { ItemDTO };

/** The demo's domain: its events are `demo:op:data|progress|done`. */
export const DEMO_DOMAIN = "demo";

/** Limits of DemoService.Stream (demo.MaxRatePerSec, demo.MaxDurationMs). */
export const DEMO_MAX_RATE_PER_SEC = 100_000;
export const DEMO_MAX_DURATION_MS = 60_000;

/**
 * Starts a stream of ratePerSec items a second for durationMs. Resolves with
 * the operation ID at once; follow it with startOperation/useOperation.
 */
export function startDemoStream(ratePerSec: number, durationMs: number): CancellablePromise<StartedDTO> {
  return DemoService.Stream(ratePerSec, durationMs);
}
