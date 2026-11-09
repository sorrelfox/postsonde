// Collection store: the request tree of the open project (folders and
// items of every protocol), which folders are expanded and which node is
// selected (COL-01, COL-02), and which folders are shown sorted by
// name (COL-05). commands/collection.ts fills it from the Go
// CollectionService (TreeDTO, docs/collection.md).
//
// The tree is normalized (nodes by id, children as id lists), so changing
// one node replaces one object and only the tree rows that read it
// re-render. Loading from disk is a side effect and lives in commands/;
// this reducer only receives the result.

import { insertAt, omitKeys } from "./util";

/** Stable node id: the `id` member of the file (docs/collection.md#ids). */
export type NodeId = string;

interface NodeBase {
  readonly id: NodeId;
  readonly parentId: NodeId | null;
  /** Display name. */
  readonly name: string;
  /** Slash path of the file or directory relative to the project folder. */
  readonly path: string;
  /** The file's kind member ("http", "graphql", "grpc", …), or "" when it could not be read. */
  readonly fileKind: string;
  /**
   * Why the file must not be written ("newer_version", "unknown_kind",
   * "invalid", "symlink"), or "". DELETED_READ_ONLY for a request whose file
   * was removed outside PostSonde while its tab was open (STO-05).
   */
  readonly readOnly: string;
}

export interface FolderNode extends NodeBase {
  readonly kind: "folder";
  readonly childIds: readonly NodeId[];
}

/** A request PostSonde can open and send (http, graphql). */
export interface RequestNode extends NodeBase {
  readonly kind: "request";
  /** HTTP method: the http request's method, "POST" or "GET" for graphql. */
  readonly method: string;
}

/** An item of a kind this version does not know (a gRPC request from a newer PostSonde). Read-only. */
export interface UnknownNode extends NodeBase {
  readonly kind: "unknown";
}

/** A project file that did not load. Read-only; the reason is in `problems`. */
export interface InvalidNode extends NodeBase {
  readonly kind: "invalid";
}

export type TreeNode = FolderNode | RequestNode | UnknownNode | InvalidNode;

/** Something wrong found while loading the tree. */
export interface CollectionProblem {
  readonly path: string;
  /** The node it concerns, or "". */
  readonly nodeId: NodeId;
  readonly code: string;
  readonly message: string;
}

export interface CollectionState {
  /** "idle" with no project, "loading" until the first tree arrives, then "ready" or "error". */
  readonly status: "idle" | "loading" | "ready" | "error";
  /** The workspace the tree belongs to, or null. */
  readonly workspaceId: string | null;
  /** Why no project file may be written ("newer_version", "invalid"), or "". */
  readonly readOnly: string;
  readonly nodes: Readonly<Record<NodeId, TreeNode>>;
  readonly rootIds: readonly NodeId[];
  /**
   * The project's scratch requests (COL-06), sorted by name. Their nodes
   * are in `nodes` (requests with no parent and an empty path) so tabs and
   * editors find them, but they are not part of the tree.
   */
  readonly scratchIds: readonly NodeId[];
  /**
   * Requests whose file was removed outside PostSonde while their tab was
   * open (STO-05). Their nodes stay in `nodes` (read-only, DELETED_READ_ONLY,
   * `parentId` the folder they were in) so the tab keeps its content until it
   * is closed or the file is restored, but they are not part of the tree.
   */
  readonly deletedIds: readonly NodeId[];
  /** Expanded folder ids (a record, not a Set, so it stays plain data). */
  readonly expandedIds: Readonly<Record<NodeId, true>>;
  /**
   * The folders shown sorted by name instead of in their manual order
   * (COL-05), by folder id; TOP_LEVEL (the empty string) is the top level.
   * A view preference: the manual `order` in the project stays as it is.
   */
  readonly sortedByName: Readonly<Record<string, true>>;
  readonly selectedId: NodeId | null;
  readonly problems: readonly CollectionProblem[];
  /** The load error message while status is "error". */
  readonly error: string | null;
}

export const initialCollectionState: CollectionState = {
  status: "idle",
  workspaceId: null,
  readOnly: "",
  nodes: {},
  rootIds: [],
  scratchIds: [],
  deletedIds: [],
  expandedIds: {},
  sortedByName: {},
  selectedId: null,
  problems: [],
  error: null,
};

/** The `readOnly` reason of a request whose file was removed outside PostSonde (`deletedIds`). */
export const DELETED_READ_ONLY = "deleted";

/** The key of the top level in `sortedByName` (no node id is empty). */
export const TOP_LEVEL = "";

/** How a folder's items are shown (COL-05): their manual order, or by name. */
export type SortMode = "manual" | "name";

export type CollectionAction =
  /** A tree for `workspaceId` is being loaded. Another workspace than the current one clears the tree. */
  | { type: "collection/loading"; workspaceId: string }
  /**
   * Replaces the whole tree (project opened or reloaded from disk). Keeps
   * expansion and selection for ids that still exist, unless `expandedIds`
   * is given (another project: its remembered expansion).
   */
  | {
      type: "collection/load";
      nodes: readonly TreeNode[];
      rootIds: readonly NodeId[];
      /** The scratch requests among `nodes` (COL-06). */
      scratchIds?: readonly NodeId[];
      workspaceId?: string | null;
      readOnly?: string;
      problems?: readonly CollectionProblem[];
      expandedIds?: readonly NodeId[];
      /** Folder ids (TOP_LEVEL for the top level) sorted by name; like expandedIds, for another project. */
      sortedByName?: readonly string[];
      /**
       * Requests whose file was just removed outside PostSonde and whose tab
       * stays open (STO-05): kept as `deletedIds`, with DELETED_READ_ONLY.
       * Those kept before stay too while they are not back in the tree.
       */
      deleted?: readonly TreeNode[];
    }
  /** Forgets requests of `deletedIds` (their tabs closed). */
  | { type: "collection/forgetDeleted"; ids: readonly NodeId[] }
  | { type: "collection/failed"; workspaceId: string; error: string }
  /** No project open. */
  | { type: "collection/clear" }
  /** Adds a node under `node.parentId` (null: top level), at `index` or at the end. */
  | { type: "collection/add"; node: TreeNode; index?: number }
  | { type: "collection/rename"; id: NodeId; name: string }
  /** A request's method was edited: its badge follows before the tree reloads. Requests only. */
  | { type: "collection/setMethod"; id: NodeId; method: string }
  /** Removes a node and everything below it. */
  | { type: "collection/remove"; id: NodeId }
  | { type: "collection/setExpanded"; id: NodeId; expanded: boolean }
  | { type: "collection/toggleExpanded"; id: NodeId }
  | { type: "collection/select"; id: NodeId | null }
  /** Shows folder `parentId` (null: the top level) in its manual order or sorted by name (COL-05). */
  | { type: "collection/setSort"; parentId: NodeId | null; mode: SortMode };

function expandedFolders(nodes: Readonly<Record<NodeId, TreeNode>>, ids: Iterable<NodeId>): Record<NodeId, true> {
  const out: Record<NodeId, true> = {};
  for (const id of ids) {
    if (nodes[id]?.kind === "folder") out[id] = true;
  }
  return out;
}

function sortedFolders(nodes: Readonly<Record<NodeId, TreeNode>>, ids: Iterable<string>): Record<string, true> {
  const out: Record<string, true> = {};
  for (const id of ids) {
    if (id === TOP_LEVEL || nodes[id]?.kind === "folder") out[id] = true;
  }
  return out;
}

export function collectionReducer(state: CollectionState, action: CollectionAction): CollectionState {
  switch (action.type) {
    case "collection/loading": {
      if (action.workspaceId === state.workspaceId) {
        return state.status === "loading" || state.status === "ready" ? state : { ...state, status: "loading", error: null };
      }
      return { ...initialCollectionState, status: "loading", workspaceId: action.workspaceId };
    }

    case "collection/load": {
      const nodes: Record<NodeId, TreeNode> = {};
      for (const node of action.nodes) nodes[node.id] = node;
      const workspaceId = action.workspaceId === undefined ? state.workspaceId : action.workspaceId;
      const sameWorkspace = workspaceId === state.workspaceId;
      const kept = sameWorkspace ? state.deletedIds.map((id) => state.nodes[id]).filter((n): n is TreeNode => n !== undefined) : [];
      const deletedIds: NodeId[] = [];
      for (const node of [...kept, ...(action.deleted ?? [])]) {
        // A file that came back (a checkout) is an item of the tree again.
        if (node.id in nodes) continue;
        nodes[node.id] = { ...node, readOnly: DELETED_READ_ONLY };
        deletedIds.push(node.id);
      }
      const expandedIds = expandedFolders(nodes, action.expandedIds ?? (sameWorkspace ? Object.keys(state.expandedIds) : []));
      const sortedByName = sortedFolders(nodes, action.sortedByName ?? (sameWorkspace ? Object.keys(state.sortedByName) : []));
      const selectedId = sameWorkspace && state.selectedId !== null && state.selectedId in nodes ? state.selectedId : null;
      return {
        status: "ready",
        workspaceId,
        readOnly: action.readOnly ?? "",
        nodes,
        rootIds: [...action.rootIds],
        scratchIds: (action.scratchIds ?? []).filter((id) => id in nodes),
        deletedIds,
        expandedIds,
        sortedByName,
        selectedId,
        problems: action.problems ?? [],
        error: null,
      };
    }

    case "collection/forgetDeleted": {
      const forget = action.ids.filter((id) => state.deletedIds.includes(id));
      if (forget.length === 0) return state;
      const selectedId = state.selectedId !== null && forget.includes(state.selectedId) ? null : state.selectedId;
      return {
        ...state,
        nodes: omitKeys(state.nodes, forget),
        deletedIds: state.deletedIds.filter((id) => !forget.includes(id)),
        selectedId,
      };
    }

    case "collection/failed":
      if (action.workspaceId !== state.workspaceId) return state;
      return { ...state, status: "error", error: action.error };

    case "collection/clear":
      return state === initialCollectionState ? state : initialCollectionState;

    case "collection/add": {
      const { node } = action;
      if (node.id in state.nodes) return state;
      if (node.parentId === null) {
        return {
          ...state,
          nodes: { ...state.nodes, [node.id]: node },
          rootIds: insertAt(state.rootIds, node.id, action.index),
        };
      }
      const parent: TreeNode | undefined = state.nodes[node.parentId];
      if (parent?.kind !== "folder") return state;
      return {
        ...state,
        nodes: {
          ...state.nodes,
          [node.id]: node,
          [parent.id]: { ...parent, childIds: insertAt(parent.childIds, node.id, action.index) },
        },
      };
    }

    case "collection/rename": {
      const node: TreeNode | undefined = state.nodes[action.id];
      if (node === undefined || node.name === action.name) return state;
      return { ...state, nodes: { ...state.nodes, [node.id]: { ...node, name: action.name } } };
    }

    case "collection/setMethod": {
      const node: TreeNode | undefined = state.nodes[action.id];
      if (node?.kind !== "request" || node.method === action.method) return state;
      return { ...state, nodes: { ...state.nodes, [node.id]: { ...node, method: action.method } } };
    }

    case "collection/remove": {
      const node: TreeNode | undefined = state.nodes[action.id];
      if (node === undefined) return state;
      const removed = selectSubtreeIds(state, node.id);
      let nodes = omitKeys(state.nodes, removed);
      let rootIds = state.rootIds;
      const scratchIds = state.scratchIds.includes(node.id) ? state.scratchIds.filter((id) => id !== node.id) : state.scratchIds;
      if (node.parentId === null) {
        rootIds = rootIds.filter((id) => id !== node.id);
      } else {
        const parent: TreeNode | undefined = nodes[node.parentId];
        if (parent?.kind === "folder") {
          nodes = { ...nodes, [parent.id]: { ...parent, childIds: parent.childIds.filter((id) => id !== node.id) } };
        }
      }
      const selectedId = state.selectedId !== null && removed.includes(state.selectedId) ? null : state.selectedId;
      return {
        ...state,
        nodes,
        rootIds,
        scratchIds,
        expandedIds: omitKeys(state.expandedIds, removed),
        sortedByName: omitKeys(state.sortedByName, removed),
        selectedId,
      };
    }

    case "collection/setExpanded": {
      const isExpanded = action.id in state.expandedIds;
      if (isExpanded === action.expanded) return state;
      if (!action.expanded) return { ...state, expandedIds: omitKeys(state.expandedIds, [action.id]) };
      if (state.nodes[action.id]?.kind !== "folder") return state;
      return { ...state, expandedIds: { ...state.expandedIds, [action.id]: true } };
    }

    case "collection/toggleExpanded":
      return collectionReducer(state, {
        type: "collection/setExpanded",
        id: action.id,
        expanded: !(action.id in state.expandedIds),
      });

    case "collection/select": {
      if (action.id === state.selectedId) return state;
      if (action.id !== null && !(action.id in state.nodes)) return state;
      return { ...state, selectedId: action.id };
    }

    case "collection/setSort": {
      const key = action.parentId ?? TOP_LEVEL;
      const isSorted = key in state.sortedByName;
      if (isSorted === (action.mode === "name")) return state;
      if (action.mode === "manual") return { ...state, sortedByName: omitKeys(state.sortedByName, [key]) };
      if (action.parentId !== null && state.nodes[action.parentId]?.kind !== "folder") return state;
      return { ...state, sortedByName: { ...state.sortedByName, [key]: true } };
    }

    default:
      return state;
  }
}

// Selectors. Ones that return a new array need `shallowEqual` in useSelector.

export function selectNode(state: CollectionState, id: NodeId): TreeNode | undefined {
  return state.nodes[id];
}

export function selectIsExpanded(state: CollectionState, id: NodeId): boolean {
  return id in state.expandedIds;
}

/** How folder `parentId` (null: the top level) is shown (COL-05). */
export function selectSortMode(state: Pick<CollectionState, "sortedByName">, parentId: NodeId | null): SortMode {
  return (parentId ?? TOP_LEVEL) in state.sortedByName ? "name" : "manual";
}

/** Whether `id` is a request whose file was removed outside PostSonde (STO-05), kept for its open tab. */
export function selectIsDeleted(state: CollectionState, id: NodeId): boolean {
  return state.deletedIds.includes(id);
}

/** Whether `id` is a scratch request (COL-06), not an item of the tree. */
export function selectIsScratch(state: CollectionState, id: NodeId): boolean {
  return state.scratchIds.includes(id);
}

/** `id` and all its descendants, depth first. Empty if `id` is unknown. */
export function selectSubtreeIds(state: CollectionState, id: NodeId): NodeId[] {
  const out: NodeId[] = [];
  const stack: NodeId[] = [id];
  while (stack.length > 0) {
    const current = stack.pop() as NodeId;
    const node: TreeNode | undefined = state.nodes[current];
    if (node === undefined) continue;
    out.push(current);
    if (node.kind === "folder") stack.push(...[...node.childIds].reverse());
  }
  return out;
}

/** The request ids at or below `id` (e.g. the tabs to close when a folder goes). */
export function selectRequestIdsInSubtree(state: CollectionState, id: NodeId): NodeId[] {
  return selectSubtreeIds(state, id).filter((nodeId) => state.nodes[nodeId]?.kind === "request");
}

/** The first problem reported for a node, if any. */
export function selectNodeProblem(state: CollectionState, id: NodeId): CollectionProblem | undefined {
  return state.problems.find((p) => p.nodeId === id);
}

/**
 * The number of items in each folder's whole subtree, by folder id:
 * requests, unknown and invalid items at any depth, not folders. The tree
 * shows it next to the folder name. O(nodes); memoize on `nodes`.
 */
export function countItems(nodes: Readonly<Record<NodeId, TreeNode>>, rootIds: readonly NodeId[]): Record<NodeId, number> {
  const counts: Record<NodeId, number> = {};
  const count = (id: NodeId, depth: number): number => {
    const node: TreeNode | undefined = nodes[id];
    if (node === undefined) return 0;
    if (node.kind !== "folder") return 1;
    // Cycles cannot come from Go (links are not followed); guard anyway.
    if (id in counts || depth > 1000) return counts[id] ?? 0;
    counts[id] = 0;
    let n = 0;
    for (const childId of node.childIds) n += count(childId, depth + 1);
    counts[id] = n;
    return n;
  };
  for (const id of rootIds) count(id, 0);
  return counts;
}

/** One visible row of the tree: a node whose ancestors are all expanded. */
export interface VisibleRow {
  readonly id: NodeId;
  readonly kind: TreeNode["kind"];
  /** 1 for top-level items (aria-level). */
  readonly level: number;
  /** Number of siblings including itself, and its 1-based position (aria-setsize, aria-posinset). */
  readonly setSize: number;
  readonly posInSet: number;
  /** Index of the parent's row, or -1 at the top level. */
  readonly parentIndex: number;
  readonly expanded: boolean;
}

const nameCollator = new Intl.Collator(undefined, { sensitivity: "base", numeric: true });

/**
 * `ids` as a folder sorted by name shows them (COL-05): folders first,
 * then the other items, each by name ignoring case and accents, with
 * numbers in numeric order ("Step 2" before "Step 10"); ties by id, so
 * the order is stable. Unknown ids are dropped.
 */
export function sortIdsByName(nodes: Readonly<Record<NodeId, TreeNode>>, ids: readonly NodeId[]): NodeId[] {
  const present = ids.filter((id) => nodes[id] !== undefined);
  return present.sort((a, b) => {
    const na = nodes[a] as TreeNode;
    const nb = nodes[b] as TreeNode;
    const fa = na.kind === "folder" ? 0 : 1;
    const fb = nb.kind === "folder" ? 0 : 1;
    if (fa !== fb) return fa - fb;
    return nameCollator.compare(na.name, nb.name) || (a < b ? -1 : a > b ? 1 : 0);
  });
}

export interface FlattenOptions {
  /** Folders (TOP_LEVEL: the top level) whose items are shown sorted by name (COL-05). */
  readonly sortedByName?: Readonly<Record<string, true>>;
  /** Only these nodes are shown (the sidebar filter, NAV-06); every node when absent. */
  readonly visible?: ReadonlySet<NodeId>;
}

/**
 * The rows of the tree as shown: depth first, children of expanded folders
 * only, each folder in its manual order or sorted by name, and only the
 * `visible` nodes when given. O(visible rows), plus sorting the items of
 * the expanded sorted folders; memoize on the arguments.
 */
export function flattenVisibleRows(
  nodes: Readonly<Record<NodeId, TreeNode>>,
  rootIds: readonly NodeId[],
  expandedIds: Readonly<Record<NodeId, true>>,
  options: FlattenOptions = {},
): VisibleRow[] {
  const sorted = options.sortedByName ?? {};
  const { visible } = options;
  const rows: VisibleRow[] = [];
  const visit = (parentId: NodeId | null, ids: readonly NodeId[], level: number, parentIndex: number) => {
    const ordered = (parentId ?? TOP_LEVEL) in sorted ? sortIdsByName(nodes, ids) : ids;
    const present = ordered.filter((id) => nodes[id] !== undefined && (visible === undefined || visible.has(id)));
    present.forEach((id, i) => {
      const node = nodes[id] as TreeNode;
      const expanded = node.kind === "folder" && id in expandedIds && level < 1000;
      const index = rows.length;
      rows.push({ id, kind: node.kind, level, setSize: present.length, posInSet: i + 1, parentIndex, expanded });
      if (expanded) visit(id, (node as FolderNode).childIds, level + 1, index);
    });
  };
  visit(null, rootIds, 1, -1);
  return rows;
}
