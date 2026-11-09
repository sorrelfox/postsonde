// Helpers shared by every Go call. See docs/bindings.md; errors are in
// ./errors (docs/errors.md).
import type { CancellablePromise } from "@wailsio/runtime";
import { isCancelled, toAppError } from "./errors";

export type { CancellablePromise };
export { isCancelled };

/**
 * Returns the user-facing message of a failed call. For the kind, hint and
 * causes use toAppError from ./errors.
 */
export function errorMessage(err: unknown): string {
  return toAppError(err).message;
}
