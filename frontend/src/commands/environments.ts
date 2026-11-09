// Environment commands (ENV-01): load the open project's environments
// (again whenever the open project changes), create, rename and delete them,
// save their variables and switch the active one, through the Go
// EnvironmentsService. See docs/environments.md.
//
// Failed calls reject with an AppError (lib/errors.ts). Results that arrive
// after the open project changed are dropped.

import * as api from "../lib/environments";
import { toAppError } from "../lib/errors";
import type { Environment, EnvironmentVariable, Stores, WorkspaceInfo } from "../stores";

/** Identifies an open project for the environments store: its id and folder. */
export function environmentsWorkspaceKey(workspace: WorkspaceInfo | null): string | null {
  return workspace === null ? null : `${workspace.id}\n${workspace.path}`;
}

function currentKey(stores: Stores): string | null {
  return environmentsWorkspaceKey(stores.workspace.getState().current);
}

/** A variable from Go: a local value only for a variable that is not secret (SEC-02). */
function toVariable({ name, value, description, secret, enabled, localValue }: api.VariableDTO): EnvironmentVariable {
  return { name, value, description, secret, enabled, localValue: secret ? null : (localValue ?? null) };
}

/** A variable for Go. The local value never goes to the project file: Go keeps it in local data. */
function toVariableDTO({ name, value, description, secret, enabled, localValue }: EnvironmentVariable): api.VariableDTO {
  return { name, value, description, secret, enabled, localValue: secret ? null : (localValue ?? null) };
}

export function toEnvironment(dto: api.EnvironmentDTO): Environment {
  return {
    id: dto.id,
    name: dto.name,
    color: dto.color,
    fileName: dto.fileName,
    readOnly: dto.readOnly,
    variables: (dto.variables ?? []).map(toVariable),
  };
}

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/**
 * Loads the environments of the open project; with none open, clears the
 * store. A failure is kept in the store (status "error") and logged.
 */
export async function loadEnvironments(stores: Stores): Promise<void> {
  const key = currentKey(stores);
  if (key === null) {
    stores.environments.dispatch({ type: "environments/clear" });
    return;
  }
  stores.environments.dispatch({ type: "environments/loading", workspaceKey: key });
  try {
    const dto = await api.listEnvironments();
    stores.environments.dispatch({
      type: "environments/loaded",
      workspaceKey: key,
      environments: (dto.environments ?? []).map(toEnvironment),
      activeId: dto.activeId === "" ? null : dto.activeId,
      problems: (dto.problems ?? []).map(({ fileName, message, code }) => ({ fileName, message, code })),
      readOnly: dto.readOnly,
    });
  } catch (err: unknown) {
    const appErr = toAppError(err);
    console.warn("environments: could not load the environments", appErr);
    stores.environments.dispatch({ type: "environments/failed", workspaceKey: key, error: appErr.message });
  }
}

/**
 * Loads the environments now and again whenever another project is opened;
 * clears them when the project is closed. Returns the unsubscribe function.
 */
export function followEnvironmentsWorkspace(stores: Stores): () => void {
  let last = currentKey(stores);
  void loadEnvironments(stores);
  return stores.workspace.subscribe(() => {
    const key = currentKey(stores);
    if (key === last) return;
    last = key;
    void loadEnvironments(stores);
  });
}

/** Applies a result unless the open project changed while the call ran. */
function applyIfSameProject(stores: Stores, key: string | null, apply: () => void): void {
  if (key !== null && key === currentKey(stores) && key === stores.environments.getState().workspaceKey) apply();
}

/** Creates an environment and selects it in the panel. Rejects with an AppError. */
export async function createEnvironment(stores: Stores, name: string): Promise<Environment> {
  const key = currentKey(stores);
  const env = toEnvironment(await run(() => api.createEnvironment(name.trim())));
  applyIfSameProject(stores, key, () => {
    stores.environments.dispatch({ type: "environments/put", environment: env });
    stores.environments.dispatch({ type: "environments/select", id: env.id });
  });
  return env;
}

/** Renames an environment. Rejects with an AppError. */
export async function renameEnvironment(stores: Stores, id: string, name: string): Promise<Environment> {
  const key = currentKey(stores);
  const env = toEnvironment(await run(() => api.renameEnvironment(id, name.trim())));
  applyIfSameProject(stores, key, () => stores.environments.dispatch({ type: "environments/put", environment: env }));
  return env;
}

/** Deletes an environment (and its local secret values). Rejects with an AppError. */
export async function deleteEnvironment(stores: Stores, id: string): Promise<void> {
  const key = currentKey(stores);
  await run(() => api.deleteEnvironment(id));
  applyIfSameProject(stores, key, () => stores.environments.dispatch({ type: "environments/remove", id }));
}

/** Saves an environment's variables. Rejects with an AppError. */
export async function saveEnvironmentVariables(
  stores: Stores,
  id: string,
  variables: readonly EnvironmentVariable[],
): Promise<Environment> {
  const key = currentKey(stores);
  const dtos = variables.map(toVariableDTO);
  const env = toEnvironment(await run(() => api.saveEnvironmentVariables(id, dtos)));
  applyIfSameProject(stores, key, () => stores.environments.dispatch({ type: "environments/put", environment: env }));
  return env;
}

/**
 * Sets an environment's accent colour (ENV-08): "#rrggbb", or "" for none.
 * It is saved in the environment file, shared with the project. Rejects
 * with an AppError (environments.invalid_color for anything else).
 */
export async function setEnvironmentColor(stores: Stores, id: string, color: string): Promise<Environment> {
  const key = currentKey(stores);
  const env = toEnvironment(await run(() => api.setEnvironmentColor(id, color)));
  applyIfSameProject(stores, key, () => stores.environments.dispatch({ type: "environments/put", environment: env }));
  return env;
}

/**
 * Makes an environment active (null: none). The switch shows at once and is
 * undone if Go cannot save it. Rejects with an AppError.
 */
export async function setActiveEnvironment(stores: Stores, id: string | null): Promise<void> {
  const key = currentKey(stores);
  const before = stores.environments.getState().activeId;
  stores.environments.dispatch({ type: "environments/setActive", id });
  try {
    await run(() => api.setActiveEnvironment(id ?? ""));
  } catch (err: unknown) {
    applyIfSameProject(stores, key, () => {
      if (stores.environments.getState().activeId === id) {
        stores.environments.dispatch({ type: "environments/setActive", id: before });
      }
    });
    throw err;
  }
}

/** The project's global variables (ENV-02), as the Environments view edits them. */
export interface GlobalVariables {
  readonly variables: readonly EnvironmentVariable[];
  /** postsonde.json cannot be written (a newer PostSonde's file, or one that did not load). */
  readonly readOnly: boolean;
}

function toGlobalVariables(dto: api.GlobalVariablesDTO): GlobalVariables {
  return {
    variables: (dto.variables ?? []).map(toVariable),
    readOnly: dto.readOnly,
  };
}

/** Loads the global variables of the open project. Rejects with an AppError. */
export async function loadGlobalVariables(): Promise<GlobalVariables> {
  return toGlobalVariables(await run(() => api.loadGlobalVariables()));
}

/**
 * Saves the global variables (postsonde.json; secret values stay on this
 * computer). Go then sends environments:changed, so the editors resolve
 * their {{variables}} again. Rejects with an AppError.
 */
export async function saveGlobalVariables(variables: readonly EnvironmentVariable[]): Promise<GlobalVariables> {
  const dtos = variables.map(toVariableDTO);
  return toGlobalVariables(await run(() => api.saveGlobalVariables(dtos)));
}
