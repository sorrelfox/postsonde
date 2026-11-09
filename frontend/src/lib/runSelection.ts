// The requests a folder run sends and their order (RUN-02): plain data
// over the folder's plan (RunnerService.Plan), for the Run sub-tab. The
// selection belongs to one folder's run slot for the session: it is never
// written to the folder's file (docs/runner.md#selection-and-order).
import type { RunItemDTO } from "./runner";

/** Which requests of a plan a run sends, and in which order. */
export interface RunSelection {
  /** Request IDs in the order of the run; plan items missing from it go at their tree place (mergeRunOrder). */
  readonly order: readonly string[];
  /** Requests left out of the run. New requests are in until left out. */
  readonly excluded: Readonly<Record<string, true>>;
}

export const EMPTY_RUN_SELECTION: RunSelection = { order: [], excluded: {} };

/**
 * The plan's IDs in the order of the run: those of `order` that are still
 * in the plan keep their order, and every other one (a request added to
 * the folder since) goes right after the item before it in the plan, or
 * first. Returns `order` itself when nothing changed.
 */
export function mergeRunOrder(planIds: readonly string[], order: readonly string[]): readonly string[] {
  const inPlan = new Set(planIds);
  const out = order.filter((id) => inPlan.has(id));
  const placed = new Set(out);
  for (let i = 0; i < planIds.length; i++) {
    const id = planIds[i] as string;
    if (placed.has(id)) continue;
    const before = i > 0 ? out.indexOf(planIds[i - 1] as string) : -1;
    out.splice(before + 1, 0, id);
    placed.add(id);
  }
  const same = out.length === order.length && out.every((id, i) => id === order[i]);
  return same ? order : out;
}

/** The plan's items in the order of the run. */
export function orderedRunItems(plan: readonly RunItemDTO[], order: readonly string[]): RunItemDTO[] {
  const byId = new Map(plan.map((it) => [it.requestId, it]));
  return mergeRunOrder(
    plan.map((it) => it.requestId),
    order,
  ).flatMap((id) => {
    const it = byId.get(id);
    return it === undefined ? [] : [it];
  });
}

/** Whether a run sends the item: it can be sent and is not left out. */
export function isRunIncluded(item: RunItemDTO, selection: RunSelection): boolean {
  return item.skip === "" && selection.excluded[item.requestId] !== true;
}

/** The IDs a run sends (RunInputDTO.requestIds): the included items, in the order of the run. */
export function selectedRunIds(plan: readonly RunItemDTO[], selection: RunSelection): string[] {
  return orderedRunItems(plan, selection.order)
    .filter((it) => isRunIncluded(it, selection))
    .map((it) => it.requestId);
}

/** `order` with `id` moved `delta` places (-1 up, 1 down); the same array at an edge or for an unknown ID. */
export function moveRunItem(order: readonly string[], id: string, delta: number): readonly string[] {
  const from = order.indexOf(id);
  const to = from + delta;
  if (from < 0 || to < 0 || to >= order.length || delta === 0) return order;
  const out = [...order];
  out.splice(from, 1);
  out.splice(to, 0, id);
  return out;
}

/** `order` with `id` moved just before (or after) `target`; the same array when that changes nothing. */
export function moveRunItemTo(order: readonly string[], id: string, target: string, after: boolean): readonly string[] {
  if (id === target || !order.includes(id) || !order.includes(target)) return order;
  const out = order.filter((x) => x !== id);
  const at = out.indexOf(target) + (after ? 1 : 0);
  out.splice(at, 0, id);
  const same = out.every((x, i) => x === order[i]);
  return same ? order : out;
}

/** Whether the selection is the folder's own: every item in, in the tree's order. */
export function isDefaultRunSelection(plan: readonly RunItemDTO[], selection: RunSelection): boolean {
  const ordered = orderedRunItems(plan, selection.order);
  return ordered.every((it, i) => it.requestId === plan[i]?.requestId) && plan.every((it) => selection.excluded[it.requestId] !== true);
}
