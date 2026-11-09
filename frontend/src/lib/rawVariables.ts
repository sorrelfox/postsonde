// The raw JSON view of a variables table (ENV-07): an environment's
// variables, or the global ones (the base environment), as text to edit
// and back. The text is a JSON array, one variable per line:
//
//   [
//     { "name": "baseUrl", "value": "https://api.example" },
//     { "name": "token", "value": "••••••", "secret": true },
//     { "name": "user", "value": "team", "localValue": "me" },
//     { "name": "old", "value": "x", "enabled": false, "description": "…" }
//   ]
//
// Secret values never appear in the text: a secret variable with a value
// shows the mask, and the mask read back keeps the value this computer
// holds for that name. The mask is never taken as a value: where there is
// no secret value to keep, the text is refused. A plain object
// (`{"host": "localhost"}`) is read too, as variables that are not secret.
// formatRawVariables(parseRawVariables(text).variables) is the text as
// formatRawVariables wrote it, and parseRawVariables(formatRawVariables(v), v)
// is `v`: the table and the text round-trip. See docs/environments.md.

import { t } from "../i18n";
import { SECRET_MASK } from "./templateVariables";

/** A variable as the table edits it (the shape of EnvironmentVariable). */
export interface RawVariable {
  readonly name: string;
  /** For a secret variable, the value kept on this computer. */
  readonly value: string;
  readonly description: string;
  readonly secret: boolean;
  readonly enabled: boolean;
  /** The local value (SEC-02), null for none; never for a secret variable. */
  readonly localValue?: string | null;
}

export type RawParseResult =
  | { readonly ok: true; readonly variables: RawVariable[] }
  | { readonly ok: false; readonly error: string };

/** The members of a variable in the text, in the order they are written. */
const MEMBERS = ["name", "value", "localValue", "secret", "enabled", "description"] as const; // i18n-ignore: JSON member names

function member(key: string, value: unknown): string {
  return `${JSON.stringify(key)}: ${JSON.stringify(value)}`;
}

/** The variables as JSON text; secret values are masked. */
export function formatRawVariables(variables: readonly RawVariable[]): string {
  if (variables.length === 0) return "[]\n";
  const lines = variables.map((v) => {
    const parts = [member("name", v.name), member("value", v.secret && v.value !== "" ? SECRET_MASK : v.value)];
    if (!v.secret && v.localValue !== undefined && v.localValue !== null) parts.push(member("localValue", v.localValue));
    if (v.secret) parts.push(member("secret", true));
    if (!v.enabled) parts.push(member("enabled", false));
    if (v.description !== "") parts.push(member("description", v.description));
    return `  { ${parts.join(", ")} }`;
  });
  return `[\n${lines.join(",\n")}\n]\n`;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Reads the text back. `current` are the variables the text was made from
 * (or the table's latest): the secret values the mask stands for are taken
 * from them, by name. Returns why the text cannot be used otherwise.
 */
export function parseRawVariables(text: string, current: readonly RawVariable[]): RawParseResult {
  let data: unknown;
  try {
    data = JSON.parse(text);
  } catch (err: unknown) {
    return { ok: false, error: t("lib.rawVariables.notJson", { reason: err instanceof Error ? err.message : String(err) }) };
  }
  if (isObject(data)) return fromObject(data);
  if (!Array.isArray(data)) return { ok: false, error: t("lib.rawVariables.notList") };

  const secretValues = new Map<string, string>();
  for (const v of current) if (v.secret && v.value !== "" && !secretValues.has(v.name)) secretValues.set(v.name, v.value);

  const out: RawVariable[] = [];
  for (const [i, item] of data.entries()) {
    const n = i + 1;
    if (!isObject(item)) return { ok: false, error: t("lib.rawVariables.notObject", { n }) };
    for (const key of Object.keys(item)) {
      if (!(MEMBERS as readonly string[]).includes(key)) {
        return { ok: false, error: t("lib.rawVariables.unknownMember", { n, key, members: MEMBERS.join(", ") }) };
      }
    }
    const { name, value = "", localValue = null, secret = false, enabled = true, description = "" } = item;
    if (typeof name !== "string") return { ok: false, error: t("lib.rawVariables.noName", { n }) };
    const wrongType = (key: string, type: string) => ({ ok: false as const, error: t("lib.rawVariables.wrongType", { n, key, type }) });
    if (typeof value !== "string") return wrongType("value", "string");
    if (localValue !== null && typeof localValue !== "string") return wrongType("localValue", "string");
    if (typeof secret !== "boolean") return wrongType("secret", "boolean");
    if (typeof enabled !== "boolean") return wrongType("enabled", "boolean");
    if (typeof description !== "string") return wrongType("description", "string");
    if (secret && localValue !== null) return { ok: false, error: t("lib.rawVariables.secretLocal", { name }) };

    let kept = value;
    if (value === SECRET_MASK) {
      // The mask stands for the value this computer keeps; it is never a value itself.
      const known = secretValues.get(name);
      if (!secret || known === undefined) {
        return {
          ok: false,
          error: secret ? t("lib.rawVariables.maskNoValue", { name }) : t("lib.rawVariables.maskNotSecret", { name }),
        };
      }
      kept = known;
    }
    out.push({ name, value: kept, description, secret, enabled, localValue: secret ? null : localValue });
  }
  return { ok: true, variables: out };
}

/** `{"name": value}`: variables that are not secret; a value that is not a string is its JSON. */
function fromObject(data: Record<string, unknown>): RawParseResult {
  const out: RawVariable[] = [];
  for (const [name, raw] of Object.entries(data)) {
    const value = typeof raw === "string" ? raw : JSON.stringify(raw);
    if (value === SECRET_MASK) return { ok: false, error: t("lib.rawVariables.maskNotSecret", { name }) };
    out.push({ name, value, description: "", secret: false, enabled: true, localValue: null });
  }
  return { ok: true, variables: out };
}
