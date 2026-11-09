// Calls into the Go LocalDataService (internal/app/services/localdata):
// how much history, run history, cookies, OAuth 2.0 tokens, cached GraphQL
// schemas and runtime variables PostSonde keeps on this computer, and the
// one action that clears them (PRV-13). See
// docs/privacy.md#clearing-local-data.
import {
  LocalDataService,
  type CategoryUsageDTO,
  type ClearFailureDTO,
  type ClearResultDTO,
  type UsageDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/localdata";
import type { CancellablePromise } from "./calls";

export type { CategoryUsageDTO, ClearFailureDTO, ClearResultDTO, UsageDTO };

/** Whose data: the open project's, or every project's. */
export type LocalDataScope = "project" | "all";

/** The kinds of local data, in the order the settings screen lists them (Go's localdata.All). */
export const LOCAL_DATA_CATEGORIES = ["history", "runs", "cookies", "tokens", "schemas", "runtimeVariables"] as const;

export type LocalDataCategory = (typeof LOCAL_DATA_CATEGORIES)[number];

/** Whether `name` is a kind of local data the UI knows. */
export function isLocalDataCategory(name: string): name is LocalDataCategory {
  return (LOCAL_DATA_CATEGORIES as readonly string[]).includes(name);
}

/** How much of each kind the projects of `scope` keep. */
export function localDataUsage(scope: LocalDataScope): CancellablePromise<UsageDTO> {
  return LocalDataService.Usage({ scope });
}

/** Removes the kinds `categories` from the projects of `scope`; the other kinds stay. */
export function clearLocalData(scope: LocalDataScope, categories: LocalDataCategory[]): CancellablePromise<ClearResultDTO> {
  return LocalDataService.Clear({ scope, categories });
}
