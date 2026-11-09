// The Network page's fields: the default request timeout (HTTP-21), kept in
// milliseconds by the settings file and shown in seconds, and the response
// body limit (HTTP-37). Pure helpers, tested on their own.

import { t } from "../../i18n";
import { MAX_DEFAULT_TIMEOUT_MS, MAX_MAX_RESPONSE_BODY_MB, MIN_DEFAULT_TIMEOUT_MS, MIN_MAX_RESPONSE_BODY_MB } from "../../stores";

export type BodyLimitParse = { ok: true; mb: number } | { ok: false; error: string };

/**
 * Reads the response body limit field (HTTP-37): a whole number of MiB,
 * with an optional "MiB" or "MB", in the bounds the settings file checks.
 */
export function parseMaxResponseBody(text: string): BodyLimitParse {
  const error = t("settings.bodyLimit.invalid", { min: MIN_MAX_RESPONSE_BODY_MB, max: MAX_MAX_RESPONSE_BODY_MB });
  const match = /^\s*(\d+)\s*(mib|mb)?\s*$/i.exec(text);
  if (!match) return { ok: false, error };
  const mb = Number(match[1]);
  if (mb < MIN_MAX_RESPONSE_BODY_MB || mb > MAX_MAX_RESPONSE_BODY_MB) return { ok: false, error };
  return { ok: true, mb };
}

/** The field's text for a stored timeout: empty for none, else seconds ("30", "2.5"). */
export function formatTimeoutSeconds(ms: number): string {
  return ms === 0 ? "" : String(ms / 1000);
}

export type TimeoutParse = { ok: true; ms: number } | { ok: false; error: string };

const MIN_SECONDS = MIN_DEFAULT_TIMEOUT_MS / 1000;
const MAX_SECONDS = MAX_DEFAULT_TIMEOUT_MS / 1000;

/**
 * Reads the field's text: empty is no timeout (0), otherwise a number of
 * seconds (fractions allowed, whole milliseconds kept) from 1 second to 24
 * hours. Anything else is an error message for the field.
 */
export function parseTimeoutSeconds(text: string): TimeoutParse {
  const trimmed = text.trim();
  if (trimmed === "") return { ok: true, ms: 0 };
  const seconds = /^[+-]?(\d+\.?\d*|\.\d+)$/.test(trimmed) ? Number(trimmed) : Number.NaN;
  if (!Number.isFinite(seconds)) {
    return { ok: false, error: t("settings.timeout.notANumber") };
  }
  if (seconds < MIN_SECONDS) {
    return { ok: false, error: t("settings.timeout.tooShort") };
  }
  if (seconds > MAX_SECONDS) {
    return { ok: false, error: t("settings.timeout.tooLong", { max: MAX_SECONDS }) };
  }
  return { ok: true, ms: Math.round(seconds * 1000) };
}
