// Calls into the Go SystemService (internal/app/services/system).
//
// The reference for lib/<domain>.ts modules: the only place that imports the
// generated bindings for this domain. Components and stores import from
// here. Calls return the CancellablePromise unchanged so callers can
// `.cancel()` them. See docs/bindings.md.
import {
  SystemService,
  type InfoDTO,
  type WaitResultDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/system";
import type { CancellablePromise } from "./calls";

export type { InfoDTO, WaitResultDTO };

/** Fetches the app version and platform. */
export function getInfo(): CancellablePromise<InfoDTO> {
  return SystemService.Info();
}

/**
 * Waits ms milliseconds in Go. Demonstrates cancellation: calling
 * `.cancel()` on the result cancels the Go context and the call returns early.
 */
export function wait(ms: number): CancellablePromise<WaitResultDTO> {
  return SystemService.Wait(ms);
}

/** Formats info as e.g. "PostSonde v0.1.0 on darwin/arm64". */
export function describeInfo(info: InfoDTO): string {
  return `${info.name} ${info.version} on ${info.os}/${info.arch}`;
}
