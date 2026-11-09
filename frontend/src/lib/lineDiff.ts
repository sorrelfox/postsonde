// A line diff for small texts (the two versions of a request in conflict,
// STO-05): the longest common subsequence of lines, so unchanged lines
// line up and the changed ones show as removed and added. Quadratic in the
// number of lines; texts longer than MAX_DIFF_LINES are shown as all
// removed then all added.

export type DiffLineKind = "same" | "removed" | "added";

export interface DiffLine {
  readonly kind: DiffLineKind;
  readonly text: string;
}

/** Above this many lines on either side, no alignment is attempted. */
export const MAX_DIFF_LINES = 2000;

/** The lines of `before` and `after`, each marked same, removed (only in before) or added (only in after). */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = before.split("\n");
  const b = after.split("\n");
  if (a.length > MAX_DIFF_LINES || b.length > MAX_DIFF_LINES) {
    return [...a.map((text) => ({ kind: "removed" as const, text })), ...b.map((text) => ({ kind: "added" as const, text }))];
  }
  // lcs[i][j]: length of the LCS of a[i:] and b[j:].
  const lcs: number[][] = Array.from({ length: a.length + 1 }, () => new Array<number>(b.length + 1).fill(0));
  for (let i = a.length - 1; i >= 0; i--) {
    const row = lcs[i] as number[];
    const below = lcs[i + 1] as number[];
    for (let j = b.length - 1; j >= 0; j--) {
      row[j] = a[i] === b[j] ? (below[j + 1] as number) + 1 : Math.max(below[j] as number, row[j + 1] as number);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ kind: "same", text: a[i] as string });
      i++;
      j++;
    } else if ((lcs[i + 1]?.[j] ?? 0) >= (lcs[i]?.[j + 1] ?? 0)) {
      out.push({ kind: "removed", text: a[i] as string });
      i++;
    } else {
      out.push({ kind: "added", text: b[j] as string });
      j++;
    }
  }
  for (; i < a.length; i++) out.push({ kind: "removed", text: a[i] as string });
  for (; j < b.length; j++) out.push({ kind: "added", text: b[j] as string });
  return out;
}
