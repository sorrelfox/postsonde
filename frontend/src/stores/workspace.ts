// Workspace store: the open project (a folder with postsonde.json) and the
// recent-projects list shown on the welcome screen. commands/workspace.ts
// fills it from the Go WorkspaceService and its workspace:changed event
// (docs/workspace.md). This reducer does no I/O.

/** The open project. */
export interface WorkspaceInfo {
  readonly id: string;
  readonly name: string;
  /** Absolute path of the project folder. */
  readonly path: string;
  /** Local side-file directory in the app data directory (never inside `path`). */
  readonly localDir: string;
  /** How the local directory was found: "new", "existing", "moved" or "copy". */
  readonly localStatus: string;
  /** For "moved" and "copy": the other folder. Otherwise "". */
  readonly previousPath: string;
  /** postsonde.json is from a newer PostSonde; project files must not be written. */
  readonly newerFormat: boolean;
}

export interface RecentWorkspace {
  readonly id: string;
  readonly name: string;
  readonly path: string;
  /** RFC 3339. */
  readonly openedAt: string;
  /** False when the folder or its postsonde.json is gone. */
  readonly available: boolean;
}

export interface WorkspaceState {
  /** "loading" until Go has said whether a workspace is open. */
  readonly status: "loading" | "ready";
  readonly current: WorkspaceInfo | null;
  readonly recent: readonly RecentWorkspace[];
}

export const initialWorkspaceState: WorkspaceState = {
  status: "loading",
  current: null,
  recent: [],
};

export type WorkspaceAction =
  /** The open workspace (null: none). Marks the store ready. */
  | { type: "workspace/setCurrent"; workspace: WorkspaceInfo | null }
  | { type: "workspace/setRecent"; recent: readonly RecentWorkspace[] };

function sameWorkspace(a: WorkspaceInfo | null, b: WorkspaceInfo | null): boolean {
  if (a === null || b === null) return a === b;
  return (
    a.id === b.id &&
    a.name === b.name &&
    a.path === b.path &&
    a.localDir === b.localDir &&
    a.localStatus === b.localStatus &&
    a.previousPath === b.previousPath &&
    a.newerFormat === b.newerFormat
  );
}

function sameRecent(a: readonly RecentWorkspace[], b: readonly RecentWorkspace[]): boolean {
  return (
    a.length === b.length &&
    a.every(
      (x, i) =>
        x.id === b[i].id &&
        x.name === b[i].name &&
        x.path === b[i].path &&
        x.openedAt === b[i].openedAt &&
        x.available === b[i].available,
    )
  );
}

export function workspaceReducer(state: WorkspaceState, action: WorkspaceAction): WorkspaceState {
  switch (action.type) {
    case "workspace/setCurrent": {
      if (state.status === "ready" && sameWorkspace(state.current, action.workspace)) return state;
      const current = action.workspace === null ? null : { ...action.workspace };
      return { ...state, status: "ready", current };
    }

    case "workspace/setRecent":
      return sameRecent(state.recent, action.recent) ? state : { ...state, recent: action.recent.map((r) => ({ ...r })) };

    default:
      return state;
  }
}

/** The display name of the open workspace, or null. */
export function selectWorkspaceName(state: WorkspaceState): string | null {
  return state.current?.name ?? null;
}
