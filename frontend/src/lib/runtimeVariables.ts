// Calls into the Go RuntimeVarsService (internal/app/services/runtimevars):
// the open project's runtime variables (ENV-15), kept for the session or
// persisted in PostSonde's local data, above every scope a project file
// defines. See docs/environments.md#runtime-variables.
import { Events } from "@wailsio/runtime";
import {
  RuntimeVarsService,
  type ChangedDTO,
  type RuntimeVariableDTO,
  type RuntimeVariablesDTO,
  type SetInputDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/runtimevars";
import type { CancellablePromise } from "./calls";

export type { RuntimeVariableDTO, RuntimeVariablesDTO, SetInputDTO };

/** Sent by Go after any change to the runtime variables, from the view or from a response. */
export const RUNTIME_VARIABLES_CHANGED_EVENT = "runtimevars:changed";

/** Error codes the UI reacts to (internal/core/runtimevars). */
export const RUNTIME_VARIABLE_CODES = {
  invalidName: "runtimevars.invalid_name",
  notFound: "runtimevars.not_found",
  localDataFailed: "runtimevars.local_data_failed",
} as const;

/** The runtime variables of the open project, sorted by name. */
export function listRuntimeVariables(): CancellablePromise<RuntimeVariablesDTO> {
  return RuntimeVarsService.List();
}

/**
 * Sets a runtime variable; with `previousName` another name, it replaces
 * that one (a rename). Answers with the whole list.
 */
export function setRuntimeVariable(input: SetInputDTO): CancellablePromise<RuntimeVariablesDTO> {
  return RuntimeVarsService.Set(input);
}

/** Deletes one runtime variable. Answers with the whole list. */
export function deleteRuntimeVariable(name: string): CancellablePromise<RuntimeVariablesDTO> {
  return RuntimeVarsService.Delete(name);
}

/** Deletes every runtime variable, session and persisted. */
export function clearRuntimeVariables(): CancellablePromise<RuntimeVariablesDTO> {
  return RuntimeVarsService.Clear();
}

/**
 * Calls `callback` after every change Go made to the runtime variables:
 * what {{variables}} resolve to may have changed. Returns the unsubscribe
 * function.
 */
export function onRuntimeVariablesChanged(callback: (event: ChangedDTO) => void): () => void {
  return Events.On(RUNTIME_VARIABLES_CHANGED_EVENT, (ev) => callback(ev.data));
}
