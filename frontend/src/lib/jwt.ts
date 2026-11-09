// Decoding a JWT for the Auth tab's token decoder (AUTH-16). Nothing is
// verified here: the decoder only shows what a token says. Tokens are made
// and signed in Go (internal/core/jwt, docs/auth.md).

import { t, type MessageKey } from "../i18n";

export interface DecodedJwt {
  /** The header and payload, pretty-printed JSON (or the raw text when not JSON). */
  header: string;
  payload: string;
  /** The "alg" of the header, "" when it has none. */
  algorithm: string;
  /** The registered time claims, as Unix seconds, when the payload has them. */
  issuedAt: number | null;
  notBefore: number | null;
  expiresAt: number | null;
  /** The signature's length in bytes. */
  signatureBytes: number;
}

export type DecodeJwtResult = { ok: true; jwt: DecodedJwt } | { ok: false; error: string };

/** Decodes base64url (padding optional) into UTF-8 text; null when it is not base64url. */
export function base64UrlDecode(part: string): Uint8Array | null {
  if (!/^[A-Za-z0-9_-]*={0,2}$/.test(part)) return null;
  const b64 = part.replace(/=+$/, "").replace(/-/g, "+").replace(/_/g, "/");
  if (b64.length % 4 === 1) return null;
  try {
    const bin = atob(b64 + "=".repeat((4 - (b64.length % 4)) % 4));
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}

/** The messages of a part of the token that cannot be read. */
const PART_ERRORS = {
  header: { utf8: "lib.jwt.header.notUtf8", json: "lib.jwt.header.notJson", object: "lib.jwt.header.notObject" },
  payload: { utf8: "lib.jwt.payload.notUtf8", json: "lib.jwt.payload.notJson", object: "lib.jwt.payload.notObject" },
} as const satisfies Record<string, Record<string, MessageKey>>;

function jsonPart(bytes: Uint8Array, part: keyof typeof PART_ERRORS): { text: string; value: Record<string, unknown> } | string {
  const errors = PART_ERRORS[part];
  let text: string;
  try {
    text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } catch {
    return t(errors.utf8);
  }
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    return t(errors.json);
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) return t(errors.object);
  return { text: JSON.stringify(value, null, 2), value: value as Record<string, unknown> };
}

const numberClaim = (v: unknown): number | null => (typeof v === "number" && Number.isFinite(v) ? v : null);

/**
 * Decodes a compact JWS (header.payload.signature), as a debugger does: a
 * "Bearer " prefix and surrounding spaces are ignored. The signature is not
 * checked.
 */
export function decodeJwt(input: string): DecodeJwtResult {
  let token = input.trim();
  if (/^bearer\s+/i.test(token)) token = token.replace(/^bearer\s+/i, "");
  if (token === "") return { ok: false, error: t("lib.jwt.empty") };
  const parts = token.split(".");
  if (parts.length !== 3) {
    return {
      ok: false,
      error:
        parts.length === 5 ? t("lib.jwt.encrypted") : t("lib.jwt.parts"),
    };
  }
  const [h, p, s] = parts.map(base64UrlDecode);
  if (h === null || p === null || s === null) return { ok: false, error: t("lib.jwt.notBase64url") };
  const header = jsonPart(h, "header");
  if (typeof header === "string") return { ok: false, error: header };
  const payload = jsonPart(p, "payload");
  if (typeof payload === "string") return { ok: false, error: payload };
  return {
    ok: true,
    jwt: {
      header: header.text,
      payload: payload.text,
      algorithm: typeof header.value.alg === "string" ? header.value.alg : "",
      issuedAt: numberClaim(payload.value.iat),
      notBefore: numberClaim(payload.value.nbf),
      expiresAt: numberClaim(payload.value.exp),
      signatureBytes: s.length,
    },
  };
}

/** A Unix time as an ISO-8601 UTC timestamp, for the decoder's claim list. */
export function formatClaimTime(seconds: number): string {
  const d = new Date(seconds * 1000);
  return Number.isNaN(d.getTime()) ? String(seconds) : d.toISOString().replace(".000Z", "Z");
}
