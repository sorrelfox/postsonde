// The quick switcher's items and search (NAV-04): every request, folder and
// environment of the project as one flat list, and the fuzzy search over it
// with a boost for recently opened items. Pure functions; QuickSwitcher.tsx
// renders the results. See docs/frontend.md (Quick switcher).

import { fuzzyMatch, normalizeQuery } from "../../lib/fuzzy";
import type { Environment, NodeId, TreeNode } from "../../stores";
import { nodeBadge, type Badge } from "../tree/badges";

export type SwitchItemType = "request" | "folder" | "environment" | "other";

export interface SwitchItem {
  /** "node:<id>" or "env:<id>", unique across both. */
  readonly key: string;
  readonly type: SwitchItemType;
  /** The tree node id or the environment id. */
  readonly id: string;
  readonly name: string;
  readonly nameLower: string;
  /** The folders above a tree item, joined with " / "; "" at the top level and for environments. */
  readonly path: string;
  readonly pathLower: string;
  /** Path and name as searched together ("Users / Get user"), and in lower case. */
  readonly full: string;
  readonly fullLower: string;
  /** Kind or method badge of a tree item that is not a folder. */
  readonly badge: Badge | null;
  /** An environment's colour ("#rrggbb" or ""). */
  readonly color: string;
}

export const PATH_SEPARATOR = " / ";

export function nodeKey(id: NodeId): string {
  return `node:${id}`;
}

export function environmentKey(id: string): string {
  return `env:${id}`;
}

/** The items in tree order (depth first), then the environments in their order. */
export function buildSwitchItems(
  nodes: Readonly<Record<NodeId, TreeNode>>,
  rootIds: readonly NodeId[],
  environments: readonly Environment[],
): SwitchItem[] {
  const items: SwitchItem[] = [];
  const visit = (ids: readonly NodeId[], path: string, depth: number) => {
    if (depth > 1000) return;
    for (const id of ids) {
      const node = nodes[id];
      if (node === undefined) continue;
      const type: SwitchItemType = node.kind === "folder" || node.kind === "request" ? node.kind : "other";
      const full = path === "" ? node.name : path + PATH_SEPARATOR + node.name;
      items.push({
        key: nodeKey(id),
        type,
        id,
        name: node.name,
        nameLower: node.name.toLowerCase(),
        path,
        pathLower: path.toLowerCase(),
        full,
        fullLower: full.toLowerCase(),
        badge: nodeBadge(node),
        color: "",
      });
      if (node.kind === "folder") visit(node.childIds, full, depth + 1);
    }
  };
  visit(rootIds, "", 0);
  for (const env of environments) {
    items.push({
      key: environmentKey(env.id),
      type: "environment",
      id: env.id,
      name: env.name,
      nameLower: env.name.toLowerCase(),
      path: "",
      pathLower: "",
      full: env.name,
      fullLower: env.name.toLowerCase(),
      badge: null,
      color: env.color,
    });
  }
  return items;
}

export interface SwitchResult {
  readonly item: SwitchItem;
  readonly score: number;
  /** Matched character indices in the name. */
  readonly nameHits: readonly number[];
  /** Matched character indices in the path. */
  readonly pathHits: readonly number[];
}

/** A match in the name beats the same match in the folder path by this much. */
const NAME_BONUS = 24;
/** The most recent item gets this much; older ones less, down to nothing. */
const RECENT_BONUS = 30;
const RECENT_STEP = 3;

export const DEFAULT_RESULT_LIMIT = 50;

const NO_HITS: readonly number[] = [];

/**
 * Searches the items. `recent` lists item keys, the most recent first.
 *
 * - An empty query lists the recent items, then the rest in list order.
 * - Otherwise an item matches when the query matches its name, or its
 *   folder path and name together ("users get" finds Get in Users). Results
 *   are ordered by score, recent items boosted, then list order.
 */
export function searchSwitchItems(
  items: readonly SwitchItem[],
  query: string,
  recent: readonly string[] = [],
  limit: number = DEFAULT_RESULT_LIMIT,
): SwitchResult[] {
  const q = normalizeQuery(query);
  const recentRank = new Map<string, number>();
  recent.forEach((key, i) => {
    if (!recentRank.has(key)) recentRank.set(key, i);
  });

  if (q === "") {
    const byKey = new Map(items.map((item) => [item.key, item]));
    const results: SwitchResult[] = [];
    const taken = new Set<string>();
    for (const key of recentRank.keys()) {
      const item = byKey.get(key);
      if (item === undefined) continue;
      taken.add(key);
      results.push({ item, score: 0, nameHits: NO_HITS, pathHits: NO_HITS });
      if (results.length >= limit) return results;
    }
    for (const item of items) {
      if (taken.has(item.key)) continue;
      results.push({ item, score: 0, nameHits: NO_HITS, pathHits: NO_HITS });
      if (results.length >= limit) break;
    }
    return results;
  }

  const matched: { result: SwitchResult; index: number }[] = [];
  for (let index = 0; index < items.length; index++) {
    const item = items[index];
    let result: SwitchResult | null = null;
    const inName = fuzzyMatch(q, item.name, item.nameLower);
    if (inName !== null) {
      result = { item, score: inName.score + NAME_BONUS, nameHits: inName.positions, pathHits: NO_HITS };
    } else if (item.path !== "") {
      const inFull = fuzzyMatch(q, item.full, item.fullLower);
      if (inFull !== null) {
        const offset = item.path.length + PATH_SEPARATOR.length;
        const pathHits: number[] = [];
        const nameHits: number[] = [];
        for (const p of inFull.positions) {
          if (p < item.path.length) pathHits.push(p);
          else if (p >= offset) nameHits.push(p - offset);
        }
        result = { item, score: inFull.score, nameHits, pathHits };
      }
    }
    if (result === null) continue;
    const rank = recentRank.get(item.key);
    if (rank !== undefined) {
      result = { ...result, score: result.score + Math.max(0, RECENT_BONUS - rank * RECENT_STEP) };
    }
    matched.push({ result, index });
  }
  matched.sort((a, b) => b.result.score - a.result.score || a.index - b.index);
  const out: SwitchResult[] = [];
  for (let i = 0; i < matched.length && i < limit; i++) out.push(matched[i].result);
  return out;
}

/** Moves `key` to the front of a most-recent-first list of at most `max` keys. */
export function touchRecent(recent: readonly string[], key: string, max = 50): string[] {
  const next = [key];
  for (const k of recent) {
    if (k !== key && next.length < max) next.push(k);
  }
  return next;
}
