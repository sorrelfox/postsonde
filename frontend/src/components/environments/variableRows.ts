// Conversions between environment variables and the key/value table's rows.
// The table has no secret or local value column, so the secret flags and
// the local values (SEC-02) are kept next to the rows, by row id.

import type { EnvironmentVariable } from "../../stores";
import { createKeyValueRow, type KeyValueRow } from "../common/KeyValueTable";

export interface VariableRows {
  readonly rows: readonly KeyValueRow[];
  /** Row ids of the secret variables. */
  readonly secret: Readonly<Record<string, true>>;
  /**
   * The local values by row id: kept on this computer only, each replaces
   * its row's shared value here. Never for a secret row.
   */
  readonly local: Readonly<Record<string, string>>;
}

export function variablesToRows(variables: readonly EnvironmentVariable[], createId?: () => string): VariableRows {
  const rows: KeyValueRow[] = [];
  const secret: Record<string, true> = {};
  const local: Record<string, string> = {};
  for (const v of variables) {
    const row = createKeyValueRow(
      { enabled: v.enabled, key: v.name, value: v.value, description: v.description },
      createId,
    );
    rows.push(row);
    if (v.secret) secret[row.id] = true;
    else if (v.localValue !== undefined && v.localValue !== null) local[row.id] = v.localValue;
  }
  return { rows, secret, local };
}

/** The variables to save. Rows left completely empty are dropped. */
export function rowsToVariables({ rows, secret, local }: VariableRows): EnvironmentVariable[] {
  return rows
    .filter(
      (r) =>
        r.key !== "" || r.value !== "" || r.description !== "" || secret[r.id] === true || local[r.id] !== undefined,
    )
    .map((r) => {
      const isSecret = secret[r.id] === true;
      return {
        name: r.key,
        value: r.value,
        description: r.description,
        secret: isSecret,
        enabled: r.enabled,
        localValue: isSecret ? null : (local[r.id] ?? null),
      };
    });
}

/** A variable of the base environment (the global variables) as an environment sees it. */
export interface InheritedVariable {
  readonly variable: EnvironmentVariable;
  /** An enabled row of the environment defines the name, so its value is sent instead. */
  readonly overridden: boolean;
}

/**
 * The base variables an environment inherits (ENV-05), as Go's scope chain
 * uses them (docs/environments.md#base-environment): the enabled ones, a
 * secret one only when it has a value on this computer, the last of a name
 * that is defined twice. Each says whether the environment's rows override
 * it: an enabled row of that name, a secret one only with a value.
 */
export function inheritedVariables(base: readonly EnvironmentVariable[], state: VariableRows): InheritedVariable[] {
  const byName = new Map<string, EnvironmentVariable>();
  for (const v of base) {
    if (v.name === "" || !v.enabled || (v.secret && v.value === "")) continue;
    byName.delete(v.name);
    byName.set(v.name, v);
  }
  const own = new Set(
    state.rows.filter((r) => r.enabled && !(state.secret[r.id] === true && r.value === "")).map((r) => r.key),
  );
  return [...byName.values()].map((variable) => ({ variable, overridden: own.has(variable.name) }));
}

/** `state` without the flags and local values of rows that are not in `rows` any more. */
export function keepRows(state: VariableRows, rows: readonly KeyValueRow[]): VariableRows {
  const ids = new Set(rows.map((r) => r.id));
  const secret: Record<string, true> = {};
  for (const id of Object.keys(state.secret)) if (ids.has(id)) secret[id] = true;
  const local: Record<string, string> = {};
  for (const [id, value] of Object.entries(state.local)) if (ids.has(id)) local[id] = value;
  return { rows, secret, local };
}
