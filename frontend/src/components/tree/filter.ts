// The sidebar filter of the collection tree (NAV-06): which items match a
// query by name or method, which rows stay visible around them and which
// folders open to show them. Pure; the matching is lib/fuzzy.ts, shared
// with the quick switcher. See docs/frontend.md (Sidebar filter).

import { fuzzyMatch, isWordStart, normalizeQuery, type FuzzyMatch } from "../../lib/fuzzy";
import type { FolderNode, NodeId, TreeNode } from "../../stores";
import { nodeBadge } from "./badges";

/** Words that name a method or a request kind: a query that starts with one filters by it. */
const METHOD_WORDS: ReadonlySet<string> = new Set([
  "get",
  "post",
  "put",
  "patch",
  "delete",
  "del",
  "head",
  "options",
  "opt",
  "trace",
  "connect",
  "http",
  "gql",
  "graphql",
  "grpc",
  "ws",
  "websocket",
  "sio",
  "socketio",
  "sse",
  "tcp",
  "udp",
]);

/** One item of the filter's index: a node's name and method words, lower case, computed once per tree. */
export interface FilterEntry {
  readonly id: NodeId;
  readonly name: string;
  readonly lower: string;
  /** Lower-case words the method part of a query matches: the method, the badge label and the file kind. Empty for folders. */
  readonly methods: readonly string[];
}

/**
 * The filter's index of a tree: every node below the top level, depth
 * first. Build it once per tree (memoize on `nodes` and `rootIds`), not
 * per key press.
 */
export function buildFilterIndex(nodes: Readonly<Record<NodeId, TreeNode>>, rootIds: readonly NodeId[]): FilterEntry[] {
  const out: FilterEntry[] = [];
  const visit = (ids: readonly NodeId[], depth: number) => {
    for (const id of ids) {
      const node: TreeNode | undefined = nodes[id];
      if (node === undefined) continue;
      const methods: string[] = [];
      if (node.kind !== "folder") {
        const badge = nodeBadge(node);
        if (badge !== null) methods.push(badge.label.toLowerCase());
        if (node.kind === "request" && node.method !== "") methods.push(node.method.toLowerCase());
        if (node.fileKind !== "") methods.push(node.fileKind.toLowerCase());
      }
      out.push({ id, name: node.name, lower: node.name.toLowerCase(), methods });
      if (node.kind === "folder" && depth < 1000) visit(node.childIds, depth + 1);
    }
  };
  visit(rootIds, 0);
  return out;
}

/** How one node matched. */
export interface FilterHit {
  /** Indices of the matched characters of the name, for highlighting. */
  readonly positions: readonly number[];
  /** The query's method word matched the item's method or kind. */
  readonly method: boolean;
}

export interface FilterResult {
  /** The query, trimmed. */
  readonly query: string;
  /** The nodes that match, with what matched. */
  readonly hits: ReadonlyMap<NodeId, FilterHit>;
  /** The number of matching items that are not folders. */
  readonly itemCount: number;
  /**
   * The nodes to show: every match, the folders above one, and everything
   * inside a folder that matches by name.
   */
  readonly visible: ReadonlySet<NodeId>;
  /** The folders to open while filtering: the ones above a match. */
  readonly expandedIds: Readonly<Record<NodeId, true>>;
}

/** Parses a query: its first word as a method word, and the rest (normalized) for the name. */
function parseQuery(query: string): { method: string | null; rest: string; whole: string; single: boolean } {
  const words = query.trim().split(/\s+/).filter((w) => w !== "");
  const first = (words[0] ?? "").toLowerCase();
  const method = METHOD_WORDS.has(first) ? first : null;
  return { method, rest: normalizeQuery(words.slice(1).join("")), whole: normalizeQuery(query), single: words.length === 1 };
}

/**
 * fuzzyMatch, held tighter for filtering: the query's letters in order,
 * where every run after the first starts a word: "user" in "List users",
 * "lprod" in "List products", "cu" in "createUser". The quick switcher
 * ranks loose matches low, but a filter would show them all, so "users"
 * must not match "Request of group orders" letter by letter.
 */
function tightMatch(query: string, text: string, lower: string): FuzzyMatch | null {
  const m = fuzzyMatch(query, text, lower);
  if (m === null) return null;
  const { positions } = m;
  for (let i = 1; i < positions.length; i++) {
    if (positions[i] !== positions[i - 1] + 1 && !isWordStart(text, positions[i])) return null;
  }
  return m;
}

/**
 * How an entry matches, or null. The name matches the query fuzzily
 * (tightMatch: its letters in order, jumping only to word starts). When
 * the query starts with a method word ("get", "post", "del", "gql", "ws",
 * …), an item of that method or
 * kind matches when the rest of the query matches its name (or there is
 * no rest), so "post user" finds the POST requests with "user" in the name.
 * A query that is just a method word matches other names only as a whole
 * word part, not letter by letter, so "put" does not find "Update settings".
 */
function matchEntry(entry: FilterEntry, parsed: ReturnType<typeof parseQuery>): FilterHit | null {
  const { method, rest, whole, single } = parsed;
  if (method !== null && entry.methods.includes(method)) {
    if (rest === "") return { positions: [], method: true };
    const m = tightMatch(rest, entry.name, entry.lower);
    if (m !== null) return { positions: m.positions, method: true };
  }
  if (method !== null && single) {
    const at = entry.lower.indexOf(whole);
    return at < 0 ? null : { positions: Array.from({ length: whole.length }, (_, k) => at + k), method: false };
  }
  const m = tightMatch(whole, entry.name, entry.lower);
  return m === null ? null : { positions: m.positions, method: false };
}

/**
 * Filters the tree by `query` (NAV-06). Null for a blank query (no
 * filter). Linear in the number of nodes: a tree of thousands of requests
 * filters in a few milliseconds.
 */
export function filterTree(
  nodes: Readonly<Record<NodeId, TreeNode>>,
  index: readonly FilterEntry[],
  query: string,
): FilterResult | null {
  const trimmed = query.trim();
  if (trimmed === "") return null;
  const parsed = parseQuery(trimmed);
  const hits = new Map<NodeId, FilterHit>();
  const visible = new Set<NodeId>();
  const expandedIds: Record<NodeId, true> = {};
  let itemCount = 0;
  // Folders that match by name, whose whole subtree is shown.
  const shownFolders = new Set<NodeId>();

  for (const entry of index) {
    const node = nodes[entry.id];
    if (node === undefined) continue;
    const hit = matchEntry(entry, parsed);
    const insideShown = node.parentId !== null && shownFolders.has(node.parentId);
    if (hit === null && !insideShown) continue;
    if (insideShown) {
      visible.add(entry.id);
      if (node.kind === "folder") shownFolders.add(entry.id);
    }
    if (hit === null) continue;
    hits.set(entry.id, hit);
    if (node.kind === "folder") shownFolders.add(entry.id);
    else itemCount++;
    visible.add(entry.id);
    // The folders above: shown and opened. Stop at one already done.
    let parentId = node.parentId;
    for (let depth = 0; parentId !== null && depth < 1000; depth++) {
      if (parentId in expandedIds) break;
      expandedIds[parentId] = true;
      visible.add(parentId);
      parentId = (nodes[parentId] as FolderNode | undefined)?.parentId ?? null;
    }
  }
  return { query: trimmed, hits, itemCount, visible, expandedIds };
}
