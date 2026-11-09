// Maps the multipart body's parts (PartDTO: name, value, file,
// contentType, description, enabled, extra) to KeyValueTable rows and back
// (HTTP-14). Like keyValueRows.ts, but a row also keeps its part's file and
// Content-Type through table edits, which only change the name, value,
// description and enabled flag. See docs/frontend.md (Body editor).

import { useCallback, useRef } from "react";
import type { PartDTO } from "../../lib/collection";
import type { KeyValueRow } from "../common/KeyValueTable";
import { toTableRows } from "./keyValueRows";

export interface PartMapping {
  readonly dtos: readonly PartDTO[] | null;
  readonly rows: readonly KeyValueRow[];
}

/**
 * Parts for the table's next `rows`. A row that came from a part (same id
 * in `mapping.rows`) keeps that part's file, Content-Type and `extra`; a new
 * row is a text part with none.
 */
export function toParts(rows: readonly KeyValueRow[], mapping: PartMapping): PartDTO[] {
  const byId = new Map<string, PartDTO>();
  mapping.rows.forEach((row, i) => {
    const dto = mapping.dtos?.[i];
    if (dto !== undefined) byId.set(row.id, dto);
  });
  return rows.map((row) => {
    const prev = byId.get(row.id);
    return {
      name: row.key,
      value: row.value,
      file: prev?.file ?? "",
      contentType: prev?.contentType ?? "",
      description: row.description,
      enabled: row.enabled,
      extra: prev?.extra ?? "",
    };
  });
}

/**
 * Binds a KeyValueTable to the parts `dtos`: table rows by position (ids
 * kept while the same parts come back) and a change handler that calls
 * `onChange` with the next parts.
 */
export function usePartRows(
  dtos: readonly PartDTO[] | null,
  onChange: (next: PartDTO[]) => void,
): [readonly KeyValueRow[], (rows: KeyValueRow[]) => void] {
  const mapping = useRef<PartMapping | null>(null);
  if (mapping.current === null || mapping.current.dtos !== dtos) {
    mapping.current = { dtos, rows: toTableRows(dtos, mapping.current?.rows) };
  }
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  const handleChange = useCallback((rows: KeyValueRow[]) => {
    const current = mapping.current;
    if (current === null) return;
    const next = toParts(rows, current);
    mapping.current = { dtos: next, rows };
    onChangeRef.current(next);
  }, []);

  return [mapping.current.rows, handleChange];
}
