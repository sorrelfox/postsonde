// Maps the request file's rows (KeyValueDTO: name, value, description,
// enabled, extra) to KeyValueTable rows (id, key, value, description,
// enabled) and back. Table rows need stable ids; file rows have none,
// and their `extra` (members this version does not know) must survive
// edits. See docs/frontend.md (Request editor).

import { useCallback, useRef } from "react";
import type { KeyValueDTO } from "../../lib/collection";
import { newKeyValueRowId, type KeyValueRow } from "../common/KeyValueTable";

export interface RowMapping {
  /** The file rows the table rows were made from (or sent as). */
  readonly dtos: readonly KeyValueDTO[] | null;
  readonly rows: readonly KeyValueRow[];
}

/**
 * Table rows for `dtos`. Row ids are taken from `previous` by position, so
 * inputs keep their identity (and focus) when the same rows come back.
 */
export function toTableRows(
  dtos: readonly KeyValueDTO[] | null,
  previous: readonly KeyValueRow[] = [],
  createId: () => string = newKeyValueRowId,
): KeyValueRow[] {
  return (dtos ?? []).map((dto, i) => ({
    id: previous[i]?.id ?? createId(),
    enabled: dto.enabled,
    key: dto.name,
    value: dto.value,
    description: dto.description,
  }));
}

/**
 * File rows for the table's next `rows`. A row that came from a file row
 * (same id in `mapping.rows`) keeps that row's `extra`; a new row has none.
 */
export function toFileRows(rows: readonly KeyValueRow[], mapping: RowMapping): KeyValueDTO[] {
  const extraById = new Map<string, string>();
  mapping.rows.forEach((row, i) => {
    const dto = mapping.dtos?.[i];
    if (dto !== undefined) extraById.set(row.id, dto.extra);
  });
  return rows.map((row) => ({
    name: row.key,
    value: row.value,
    description: row.description,
    enabled: row.enabled,
    extra: extraById.get(row.id) ?? "",
  }));
}

/**
 * Binds a KeyValueTable to the file rows `dtos`: returns the table rows and
 * the change handler, which calls `onChange` with the next file rows. When
 * the draft comes back with the rows just sent (same array), the table
 * keeps its row ids; rows from elsewhere (a reload) get ids by position.
 */
export function useKeyValueRows(
  dtos: readonly KeyValueDTO[] | null,
  onChange: (next: KeyValueDTO[]) => void,
): [readonly KeyValueRow[], (rows: KeyValueRow[]) => void] {
  const mapping = useRef<RowMapping | null>(null);
  if (mapping.current === null || mapping.current.dtos !== dtos) {
    mapping.current = { dtos, rows: toTableRows(dtos, mapping.current?.rows) };
  }
  const onChangeRef = useRef(onChange);
  onChangeRef.current = onChange;

  const handleChange = useCallback((rows: KeyValueRow[]) => {
    const current = mapping.current;
    if (current === null) return;
    const next = toFileRows(rows, current);
    mapping.current = { dtos: next, rows };
    onChangeRef.current(next);
  }, []);

  return [mapping.current.rows, handleChange];
}
