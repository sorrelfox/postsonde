// Environments store: the open project's environments, which one is active
// and which one the sidebar panel shows (ENV-01). commands/environments.ts
// fills it from the Go EnvironmentsService (docs/environments.md). This
// reducer does no I/O.

export interface EnvironmentVariable {
  readonly name: string;
  /** For a secret variable, the value from local storage. */
  readonly value: string;
  readonly description: string;
  readonly secret: boolean;
  readonly enabled: boolean;
  /**
   * The local value (SEC-02): kept on this computer only, it replaces
   * `value` here while the project file keeps `value` for everyone else.
   * null or absent: none. A secret variable never has one (its value is
   * local already).
   */
  readonly localValue?: string | null;
}

export interface Environment {
  readonly id: string;
  readonly name: string;
  /** "#rrggbb" or "". */
  readonly color: string;
  /** File name in the project's environments folder. */
  readonly fileName: string;
  /** Saved by a newer PostSonde: shown, not changed. */
  readonly readOnly: boolean;
  readonly variables: readonly EnvironmentVariable[];
}

/** An environment file that could not be loaded. */
export interface EnvironmentProblem {
  readonly fileName: string;
  readonly message: string;
  readonly code: string;
}

export interface EnvironmentsState {
  /** "idle" before the first load (and with no project), "error" when loading failed. */
  readonly status: "idle" | "loading" | "ready" | "error";
  /** The workspace the list belongs to; results for another one are ignored. */
  readonly workspaceKey: string | null;
  /** Environment ids, sorted by name. */
  readonly ids: readonly string[];
  readonly byId: Readonly<Record<string, Environment>>;
  readonly activeId: string | null;
  /** The environment shown in the sidebar panel. */
  readonly selectedId: string | null;
  readonly problems: readonly EnvironmentProblem[];
  /** The project is from a newer PostSonde: nothing can be changed. */
  readonly readOnly: boolean;
  /** Why loading failed. */
  readonly error: string | null;
}

export const initialEnvironmentsState: EnvironmentsState = {
  status: "idle",
  workspaceKey: null,
  ids: [],
  byId: {},
  activeId: null,
  selectedId: null,
  problems: [],
  readOnly: false,
  error: null,
};

export type EnvironmentsAction =
  | { type: "environments/loading"; workspaceKey: string }
  | {
      type: "environments/loaded";
      workspaceKey: string;
      environments: readonly Environment[];
      activeId: string | null;
      problems: readonly EnvironmentProblem[];
      readOnly: boolean;
    }
  | { type: "environments/failed"; workspaceKey: string; error: string }
  /** No project: back to the initial state. */
  | { type: "environments/clear" }
  /** Adds or replaces one environment (after create, rename, save). */
  | { type: "environments/put"; environment: Environment }
  | { type: "environments/remove"; id: string }
  | { type: "environments/setActive"; id: string | null }
  | { type: "environments/select"; id: string | null };

function sortIds(byId: Readonly<Record<string, Environment>>): string[] {
  return Object.keys(byId).sort((a, b) => {
    const na = byId[a].name.toLowerCase();
    const nb = byId[b].name.toLowerCase();
    if (na !== nb) return na < nb ? -1 : 1;
    return byId[a].fileName < byId[b].fileName ? -1 : byId[a].fileName > byId[b].fileName ? 1 : 0;
  });
}

function copyEnvironment(e: Environment): Environment {
  return { ...e, variables: e.variables.map((v) => ({ ...v })) };
}

export function environmentsReducer(state: EnvironmentsState, action: EnvironmentsAction): EnvironmentsState {
  switch (action.type) {
    case "environments/loading":
      if (state.status === "loading" && state.workspaceKey === action.workspaceKey) return state;
      // Another project: drop the old list at once.
      if (state.workspaceKey !== action.workspaceKey) {
        return { ...initialEnvironmentsState, status: "loading", workspaceKey: action.workspaceKey };
      }
      return { ...state, status: "loading", error: null };

    case "environments/loaded": {
      if (action.workspaceKey !== state.workspaceKey) return state;
      const byId: Record<string, Environment> = {};
      for (const e of action.environments) byId[e.id] = copyEnvironment(e);
      const ids = sortIds(byId);
      const activeId = action.activeId !== null && byId[action.activeId] ? action.activeId : null;
      const selectedId =
        state.selectedId !== null && byId[state.selectedId] ? state.selectedId : (activeId ?? ids[0] ?? null);
      return {
        ...state,
        status: "ready",
        ids,
        byId,
        activeId,
        selectedId,
        problems: action.problems.map((p) => ({ ...p })),
        readOnly: action.readOnly,
        error: null,
      };
    }

    case "environments/failed":
      if (action.workspaceKey !== state.workspaceKey) return state;
      return { ...state, status: "error", error: action.error };

    case "environments/clear":
      return state === initialEnvironmentsState ? state : initialEnvironmentsState;

    case "environments/put": {
      const byId = { ...state.byId, [action.environment.id]: copyEnvironment(action.environment) };
      return { ...state, byId, ids: sortIds(byId) };
    }

    case "environments/remove": {
      if (!state.byId[action.id]) return state;
      const byId = { ...state.byId };
      delete byId[action.id];
      const ids = sortIds(byId);
      const activeId = state.activeId === action.id ? null : state.activeId;
      let selectedId = state.selectedId;
      if (selectedId === action.id) {
        const index = state.ids.indexOf(action.id);
        selectedId = ids[Math.min(index, ids.length - 1)] ?? null;
      }
      return { ...state, byId, ids, activeId, selectedId };
    }

    case "environments/setActive": {
      const id = action.id !== null && state.byId[action.id] ? action.id : null;
      if (action.id !== null && id === null) return state;
      return id === state.activeId ? state : { ...state, activeId: id };
    }

    case "environments/select": {
      if (action.id !== null && !state.byId[action.id]) return state;
      return action.id === state.selectedId ? state : { ...state, selectedId: action.id };
    }

    default:
      return state;
  }
}

/** The active environment, or null. */
export function selectActiveEnvironment(state: EnvironmentsState): Environment | null {
  return state.activeId === null ? null : (state.byId[state.activeId] ?? null);
}

/** The environment shown in the panel, or null. */
export function selectSelectedEnvironment(state: EnvironmentsState): Environment | null {
  return state.selectedId === null ? null : (state.byId[state.selectedId] ?? null);
}

/** Whether `color` is a colour an environment may show: "#rrggbb". */
export function isEnvironmentColor(color: string): boolean {
  return /^#[0-9a-fA-F]{6}$/.test(color);
}
