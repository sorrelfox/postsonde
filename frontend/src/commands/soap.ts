// The body editor's SOAP helper (HTTP-18): turns request `id` into a SOAP
// call. Go (FormatService.SOAPEnvelope, internal/core/soap) makes the
// envelope and the headers; this puts the envelope in a raw XML body and
// sets the headers in the header table, in one auto-saved edit. Nothing
// SOAP-specific is stored: the result is an ordinary request. See
// docs/http-client.md#soap-helper.

import type { BodyDTO, KeyValueDTO, RequestDTO } from "../lib/collection";
import { isCancelled, toAppError } from "../lib/errors";
import { soapEnvelope, type SOAPEnvelopeDTO, type SOAPHeaderDTO } from "../lib/format";
import type { RequestId, Stores } from "../stores";
import { editRequest } from "./autosave";
import { bodyMode, KNOWN_BODY_MODES } from "./body";

export type SoapVersion = "1.1" | "1.2";

// i18n-ignore-start: protocol names
export const SOAP_VERSIONS: readonly { version: SoapVersion; label: string }[] = [
  { version: "1.1", label: "SOAP 1.1" },
  { version: "1.2", label: "SOAP 1.2" },
];
// i18n-ignore-end

export type SoapOutcome =
  /** The body and headers now make the SOAP call. */
  | { status: "applied" }
  /** Nothing changed: no draft, or the body or headers changed while Go worked. */
  | { status: "skipped" }
  /** The call failed. */
  | { status: "failed"; message: string };

const sameName = (a: string, b: string) => a.trim().toLowerCase() === b.trim().toLowerCase();

/**
 * The header rows with `set` applied and `remove` taken out: a header of
 * `set` replaces the value of the first row of its name (which is enabled)
 * and later rows of that name go, or is added at the end; rows named in
 * `remove` go. Names compare ignoring case. Other rows are kept as they are.
 */
export function withSoapHeaders(
  rows: readonly KeyValueDTO[],
  set: readonly SOAPHeaderDTO[],
  remove: readonly string[],
): KeyValueDTO[] {
  let out = rows.filter((r) => !remove.some((name) => sameName(r.name, name)));
  for (const h of set) {
    const at = out.findIndex((r) => sameName(r.name, h.name));
    if (at < 0) {
      out = [...out, { name: h.name, value: h.value, description: "", enabled: true, extra: "" }];
      continue;
    }
    out = out
      .map((r, i) => (i === at ? { ...r, value: h.value, enabled: true } : r))
      .filter((r, i) => i <= at || !sameName(r.name, h.name));
  }
  return out;
}

/**
 * The raw XML body holding `text`. A raw body keeps its other members; a
 * body of another mode is stashed for the mode picker (as setBodyMode
 * does) and its unknown members stay when its mode is a known one.
 */
function soapBody(stores: Stores, id: RequestId, body: BodyDTO, text: string): BodyDTO {
  const mode = bodyMode(body);
  if (mode === "raw") return { ...body, type: "xml", text };
  stores.drafts.dispatch({ type: "drafts/stashBody", id, mode, body });
  return {
    mode: "raw",
    type: "xml",
    text,
    fields: null,
    parts: null,
    path: "",
    contentType: "",
    extra: KNOWN_BODY_MODES.includes(mode) ? body.extra : "",
  };
}

/**
 * Makes request `id` a SOAP call of `version` with `action`: the raw body
 * (blank or any other mode: a new envelope; an envelope: switched to
 * `version`; other text: wrapped in the envelope's Body) becomes raw XML,
 * and Content-Type and SOAPAction are set (1.2 has no SOAPAction; the
 * action goes in Content-Type). One edit, so ⌘Z undoes it.
 */
export async function applySoapEnvelope(stores: Stores, id: RequestId, version: SoapVersion, action: string): Promise<SoapOutcome> {
  const before = stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined;
  if (before === undefined) return { status: "skipped" };
  const text = bodyMode(before.body) === "raw" ? before.body.text : "";
  let result: SOAPEnvelopeDTO;
  try {
    result = await soapEnvelope({ version, action, text });
  } catch (err) {
    if (isCancelled(err)) return { status: "skipped" };
    return { status: "failed", message: toAppError(err).message };
  }
  const now = stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined;
  if (now === undefined || now.body !== before.body || now.headers !== before.headers) return { status: "skipped" };
  const headers = withSoapHeaders(now.headers ?? [], result.headers ?? [], result.remove ?? []);
  editRequest(stores, id, { body: soapBody(stores, id, now.body, result.text), headers }, { history: "separate" });
  return { status: "applied" };
}
