// Comparing two responses (HTTP-40): Go compares two stored responses (the
// latest or an earlier one from the request history, or a response
// example) with internal/core/respdiff (HTTPService.CompareResponses): the
// status, the header fields by name and the body, JSON by path and other
// text by line, as rows for a side-by-side view. See docs/response-diff.md.
import {
  HTTPService,
  type BodyDiffDTO,
  type DiffLineDTO,
  type DiffNoticeDTO,
  type DiffRowDTO,
  type HeaderDiffDTO,
  type JSONChangeDTO,
  type ResponseDiffDTO,
  type StoredResponseInputDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import type { CancellablePromise } from "./calls";

export type { BodyDiffDTO, DiffLineDTO, DiffNoticeDTO, DiffRowDTO, HeaderDiffDTO, JSONChangeDTO, ResponseDiffDTO };

/** How a row, header or change differs. */
export type DiffKind = "same" | "added" | "removed" | "changed";

/** The differences between two stored responses, left and right. */
export function compareResponses(left: StoredResponseInputDTO, right: StoredResponseInputDTO): CancellablePromise<ResponseDiffDTO> {
  return HTTPService.CompareResponses({ left, right });
}

/** Unchanged lines kept around a change when the rest are folded. */
export const FOLD_CONTEXT = 3;

/** A row of the side-by-side view as shown: a row, or a fold of the unchanged rows `start` to `start + count - 1`. */
export type ShownRow =
  | { readonly kind: "row"; readonly index: number; readonly row: DiffRowDTO }
  | { readonly kind: "fold"; readonly start: number; readonly count: number };

/**
 * The rows to show: a run of unchanged rows is folded but for `context`
 * rows next to each change (none at the start or end of the text), when
 * that leaves at least two to fold; a fold whose start is in `expanded` is
 * shown in full.
 */
export function foldRows(
  rows: readonly DiffRowDTO[],
  context: number = FOLD_CONTEXT,
  expanded: ReadonlySet<number> = new Set(),
): ShownRow[] {
  const out: ShownRow[] = [];
  const push = (from: number, to: number) => {
    for (let k = from; k < to; k++) out.push({ kind: "row", index: k, row: rows[k] as DiffRowDTO });
  };
  let i = 0;
  while (i < rows.length) {
    if (rows[i]?.kind !== "same") {
      push(i, i + 1);
      i++;
      continue;
    }
    let end = i;
    while (end < rows.length && rows[end]?.kind === "same") end++;
    const start = i + (i === 0 ? 0 : context);
    const count = end - (end === rows.length ? 0 : context) - start;
    if (count > 1 && !expanded.has(start)) {
      push(i, start);
      out.push({ kind: "fold", start, count });
      push(start + count, end);
    } else {
      push(i, end);
    }
    i = end;
  }
  return out;
}
