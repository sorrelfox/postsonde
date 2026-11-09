// Calls into the Go EnvironmentsService (internal/app/services/environments):
// the open project's environments, their variables and the active one
// (ENV-01), and the project's global variables (ENV-02). See
// docs/environments.md.
import { Events } from "@wailsio/runtime";
import {
  EnvironmentsService,
  type ChangedDTO,
  type EnvironmentDTO,
  type EnvironmentsDTO,
  type ExportResultDTO,
  type GlobalVariablesDTO,
  type ProblemDTO,
  type VariableDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments";
import { t, type MessageKey } from "../i18n";
import type { CancellablePromise } from "./calls";

export type { ChangedDTO, EnvironmentDTO, EnvironmentsDTO, ExportResultDTO, GlobalVariablesDTO, ProblemDTO, VariableDTO };

/**
 * The fixed precedence of {{variable}} scopes (ENV-06), lowest first: a
 * later scope wins. Collection variables are a top-level folder's; runtime
 * variables (ENV-15, lib/runtimeVariables.ts) are set while working. A
 * folder run's own variables win over them all, within the run.
 */
// i18n-ignore: scope ids (Go's scope names); variableScopeLabel() is the text
export const VARIABLE_PRECEDENCE = ["Global", "Environment", "Collection", "Folder", "Runtime"] as const;

export type VariableScope = (typeof VARIABLE_PRECEDENCE)[number];

const SCOPE_LABELS: Readonly<Record<VariableScope, MessageKey>> = {
  Global: "lib.environments.scope.global",
  Environment: "lib.environments.scope.environment",
  Collection: "lib.environments.scope.collection",
  Folder: "lib.environments.scope.folder",
  Runtime: "lib.environments.scope.runtime",
};

/** A scope of VARIABLE_PRECEDENCE as the UI names it, in the language in use. */
export function variableScopeLabel(scope: VariableScope): string {
  return t(SCOPE_LABELS[scope]);
}

/** The global variables of the open project (postsonde.json). */
export function loadGlobalVariables(): CancellablePromise<GlobalVariablesDTO> {
  return EnvironmentsService.GlobalVariables();
}

/** Replaces the global variables; secret values stay in local storage. */
export function saveGlobalVariables(variables: VariableDTO[]): CancellablePromise<GlobalVariablesDTO> {
  return EnvironmentsService.SaveGlobalVariables({ variables });
}

/** Sent by Go after a change to the environments or which one is active. */
export const ENVIRONMENTS_CHANGED_EVENT = "environments:changed";

/** Error codes the UI reacts to (internal/core/environments). */
export const ENVIRONMENT_CODES = {
  nameTaken: "environments.name_taken",
  invalidName: "environments.invalid_name",
  notFound: "environments.not_found",
  readOnly: "environments.read_only",
} as const;

/** Every environment of the open project, the active one and the files that could not be loaded. */
export function listEnvironments(): CancellablePromise<EnvironmentsDTO> {
  return EnvironmentsService.List();
}

/** Creates an empty environment. */
export function createEnvironment(name: string): CancellablePromise<EnvironmentDTO> {
  return EnvironmentsService.Create(name);
}

/** Renames an environment (and its file). */
export function renameEnvironment(id: string, name: string): CancellablePromise<EnvironmentDTO> {
  return EnvironmentsService.Rename({ id, name });
}

/** Deletes an environment's file and its local secret values. */
export function deleteEnvironment(id: string): CancellablePromise<void> {
  return EnvironmentsService.Delete(id);
}

/** Replaces an environment's variables; secret values stay in local storage. */
export function saveEnvironmentVariables(id: string, variables: VariableDTO[]): CancellablePromise<EnvironmentDTO> {
  return EnvironmentsService.SaveVariables({ id, variables });
}

/** Sets an environment's accent colour (ENV-08): "#rrggbb", or "" for none. Saved in its file. */
export function setEnvironmentColor(id: string, color: string): CancellablePromise<EnvironmentDTO> {
  return EnvironmentsService.SetColor({ id, color });
}

/** The formats an environment is exported in (EXP-05): a PostSonde environment file, or Postman's. */
export type EnvironmentExportFormat = "postsonde" | "postman";

/**
 * Exports an environment to a file the user picks in the native save
 * dialog (EXP-05), without secret or local values. Resolves with
 * `saved: false` when they cancel.
 */
export function exportEnvironment(id: string, format: EnvironmentExportFormat): CancellablePromise<ExportResultDTO> {
  return EnvironmentsService.Export({ id, format });
}

/** Makes an environment active; "" for none. */
export function setActiveEnvironment(id: string): CancellablePromise<void> {
  return EnvironmentsService.SetActive(id);
}

/**
 * Calls `callback` after every change Go made to the environments
 * (created, renamed, deleted, variables saved, another one active): what
 * {{variables}} resolve to may have changed. Returns the unsubscribe function.
 */
export function onEnvironmentsChanged(callback: (event: ChangedDTO) => void): () => void {
  return Events.On(ENVIRONMENTS_CHANGED_EVENT, (ev) => callback(ev.data));
}
