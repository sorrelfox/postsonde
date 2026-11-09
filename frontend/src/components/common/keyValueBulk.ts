// Bulk edit of a key/value table as text (HTTP-08): one `key: value` line
// per row, disabled rows prefixed with `//`. See docs/frontend.md
// (Key/value table).

import type { KeyValueRow } from "./KeyValueTable";

/** The prefix written before a disabled row. `#` is read as one too. */
export const DISABLED_PREFIX = "// ";

/**
 * The rows as bulk-edit text: `key: value` per row, `// key: value` for a
 * disabled one, a row without key or value as `:`. Descriptions are not
 * written; parseBulkText keeps them by key.
 */
export function formatBulkText(rows: readonly KeyValueRow[]): string {
  return rows
    .map((r) => {
      const line = r.key === "" && r.value === "" ? ":" : r.value === "" ? `${r.key}:` : `${r.key}: ${r.value}`;
      return r.enabled ? line : DISABLED_PREFIX + line;
    })
    .join("\n");
}

/** One parsed line. */
interface BulkLine {
  enabled: boolean;
  key: string;
  value: string;
}

/**
 * Reads one line: a leading `//` or `#` disables the row; the key runs to
 * the first `:` (trimmed), the value is the rest without the one space
 * after the colon. A line without a colon is a key with an empty value.
 * Blank lines are no rows (null).
 */
function parseLine(raw: string): BulkLine | null {
  let line = raw.replace(/\r$/, "");
  if (line.trim() === "") return null;
  let enabled = true;
  const start = line.trimStart();
  if (start.startsWith("//") || start.startsWith("#")) {
    enabled = false;
    line = start.slice(start.startsWith("//") ? 2 : 1);
    if (line.startsWith(" ")) line = line.slice(1);
    if (line.trim() === "") return null;
  }
  const colon = line.indexOf(":");
  if (colon < 0) return { enabled, key: line.trim(), value: "" };
  let value = line.slice(colon + 1);
  if (value.startsWith(" ")) value = value.slice(1);
  return { enabled, key: line.slice(0, colon).trim(), value };
}

/**
 * The rows `text` describes. Each row takes the id and description of a
 * row of `previous` with the same key (the first one not taken yet, in
 * order), so descriptions survive a round trip and inputs keep their
 * identity; other rows are new.
 */
export function parseBulkText(
  text: string,
  previous: readonly KeyValueRow[],
  createId: () => string,
): KeyValueRow[] {
  const unused = [...previous];
  const out: KeyValueRow[] = [];
  for (const raw of text.split("\n")) {
    const line = parseLine(raw);
    if (line === null) continue;
    const at = unused.findIndex((r) => r.key === line.key);
    const match = at >= 0 ? unused.splice(at, 1)[0] : undefined;
    out.push({ ...line, id: match?.id ?? createId(), description: match?.description ?? "" });
  }
  return out;
}

/** Whether two row lists are the same, ids included. */
export function sameRows(a: readonly KeyValueRow[], b: readonly KeyValueRow[]): boolean {
  return (
    a.length === b.length &&
    a.every((r, i) => {
      const o = b[i];
      return (
        o !== undefined &&
        r.id === o.id &&
        r.enabled === o.enabled &&
        r.key === o.key &&
        r.value === o.value &&
        r.description === o.description
      );
    })
  );
}
