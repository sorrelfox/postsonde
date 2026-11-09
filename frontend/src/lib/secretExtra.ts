// The secret flag of a folder variable travels in its key/value row's
// `extra` (the JSON text of the members the row has no column for), as
// `"secret": true`. Go keeps a secret variable's value on this computer
// only, in the folder's local side file, never in _folder.json (PRV-09,
// SEC-04; internal/core/requestsecrets). See docs/collection.md.

/** The members of a row's `extra` text; an empty object for "" or text that is not a JSON object. */
function members(extra: string): Record<string, unknown> {
  if (extra.trim() === "") return {};
  try {
    const parsed: unknown = JSON.parse(extra);
    if (parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) return parsed as Record<string, unknown>;
  } catch {
    // Not JSON: Go refuses it on save; treat it as no members here.
  }
  return {};
}

/** True when a variable row's `extra` marks it secret. */
export function isSecretExtra(extra: string): boolean {
  return members(extra).secret === true;
}

/**
 * `extra` with the secret flag set or removed; its other members are kept.
 * Returns "" when no member is left. Text that is not a JSON object is
 * returned unchanged, so nothing the file holds is lost.
 */
export function withSecretExtra(extra: string, secret: boolean): string {
  const m = members(extra);
  if (extra.trim() !== "" && Object.keys(m).length === 0 && extra.trim() !== "{}") return extra;
  if (secret) m.secret = true;
  else delete m.secret;
  return Object.keys(m).length === 0 ? "" : JSON.stringify(m);
}
