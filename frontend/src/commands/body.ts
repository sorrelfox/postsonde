// Body editor commands (HTTP-10…15): switching the body mode and
// the raw type, editing the text, the form fields and the multipart parts,
// choosing files, and the JSON tools. Every change is an auto-saved edit of
// the request's `body` (editRequest). See docs/frontend.md (Body editor)
// and docs/json-tools.md.

import type { BodyDTO, KeyValueDTO, PartDTO } from "../lib/collection";
import { isCancelled, toAppError } from "../lib/errors";
import { beautifyJson, minifyJson, type JSONErrorDTO, type JSONFormatDTO } from "../lib/format";
import { pickBodyFile, type BodyFileDTO } from "../lib/workspace";
import type { RequestId, Stores } from "../stores";
import { editRequest } from "./autosave";
import { rememberBodyFile } from "./useBodyFile";

/** Body modes this version edits. The others are shown and kept as they are. */
export const EDITABLE_BODY_MODES: readonly string[] = ["none", "raw", "urlencoded", "multipart", "file"];

/** Body modes of the file format (docs/file-format.md); a newer PostSonde may add more. */
export const KNOWN_BODY_MODES: readonly string[] = ["none", "raw", "urlencoded", "multipart", "file"];

/** Raw body types, in the order the editor offers them. */
export const RAW_BODY_TYPES: readonly string[] = ["json", "xml", "html", "text", "yaml"];

/** The raw type a new raw body starts with. */
export const DEFAULT_RAW_TYPE = "json";

/** The body of request `id`'s draft, or undefined without a draft. */
export function draftBody(stores: Stores, id: RequestId): BodyDTO | undefined {
  // Drafts hold RequestDTOs (loadRequestDraft).
  return stores.drafts.getState().byId[id]?.request.body as BodyDTO | undefined;
}

/** The mode a body is in; "" is written as "none". */
export function bodyMode(body: BodyDTO): string {
  return body.mode === "" ? "none" : body.mode;
}

/** An empty body of `mode`: no members but the mode's own. */
function emptyBody(mode: string, extra: string): BodyDTO {
  return {
    mode,
    type: mode === "raw" ? DEFAULT_RAW_TYPE : "",
    text: "",
    fields: null,
    parts: null,
    path: "",
    contentType: "",
    extra,
  };
}

/**
 * Switches request `id`'s body to `mode` ("none", "raw", "urlencoded",
 * "multipart" or "file").
 *
 * The body being left is stashed in the draft (drafts/stashBody), and a
 * body stashed earlier for `mode` comes back, so raw text survives a detour
 * through "none" while the tab is open. Content is never converted between
 * modes: raw JSON does not become form fields, nor fields raw text; each
 * mode keeps (or starts) its own. What is saved has only the members
 * of `mode`: a "none" body is written as `{"mode": "none"}`. Unknown body
 * members (`extra`) stay, except those of a mode this version does not
 * know, which belong to that mode and are stashed with it.
 */
export function setBodyMode(stores: Stores, id: RequestId, mode: string): void {
  const body = draftBody(stores, id);
  if (body === undefined) return;
  const from = bodyMode(body);
  if (from === mode) return;

  stores.drafts.dispatch({ type: "drafts/stashBody", id, mode: from, body });
  const stashed = stores.drafts.getState().bodyStash[id]?.[mode] as BodyDTO | undefined;
  const extra = KNOWN_BODY_MODES.includes(from) ? body.extra : "";
  const next: BodyDTO = stashed ?? emptyBody(mode, extra);
  editRequest(stores, id, { body: next }, { history: "separate" });
}

/** Sets the language of request `id`'s raw body; the text stays. */
export function setRawBodyType(stores: Stores, id: RequestId, type: string): void {
  const body = draftBody(stores, id);
  if (body === undefined || body.mode !== "raw" || body.type === type) return;
  editRequest(stores, id, { body: { ...body, type } }, { history: "separate" });
}

/** Sets the text of request `id`'s raw body. */
export function setBodyText(stores: Stores, id: RequestId, text: string): void {
  const body = draftBody(stores, id);
  if (body === undefined || body.text === text) return;
  editRequest(stores, id, { body: { ...body, text } });
}

/**
 * Sets the fields of request `id`'s URL-encoded form body: rows as typed
 * (plain text, encoded by Go when sending), disabled rows and descriptions
 * included.
 */
export function setFormFields(stores: Stores, id: RequestId, fields: KeyValueDTO[]): void {
  const body = draftBody(stores, id);
  if (body === undefined || body.mode !== "urlencoded" || body.fields === fields) return;
  editRequest(stores, id, { body: { ...body, fields } });
}

/**
 * Sets the parts of request `id`'s multipart body: text parts as typed
 * (Go renders their variables when sending) and file parts by path.
 */
export function setMultipartParts(stores: Stores, id: RequestId, parts: PartDTO[]): void {
  const body = draftBody(stores, id);
  if (body === undefined || body.mode !== "multipart" || body.parts === parts) return;
  editRequest(stores, id, { body: { ...body, parts } });
}

/** Changes part `index` of request `id`'s multipart body; does nothing when there is no such part. */
function updatePart(stores: Stores, id: RequestId, index: number, change: (part: PartDTO) => PartDTO): void {
  const body = draftBody(stores, id);
  const parts = body?.mode === "multipart" ? (body.parts ?? []) : [];
  if (index < 0 || index >= parts.length) return;
  const next = change(parts[index]);
  if (next === parts[index]) return;
  setMultipartParts(stores, id, parts.map((p, i) => (i === index ? next : p)));
}

/** Sets the Content-Type of multipart part `index` ("" for the default). */
export function setPartContentType(stores: Stores, id: RequestId, index: number, contentType: string): void {
  updatePart(stores, id, index, (p) => (p.contentType === contentType ? p : { ...p, contentType }));
}

/**
 * Makes multipart part `index` a text part: its file is dropped, its name,
 * Content-Type and description stay.
 */
export function setPartText(stores: Stores, id: RequestId, index: number): void {
  updatePart(stores, id, index, (p) => (p.file === "" ? p : { ...p, file: "" }));
}

/** Sets the file of request `id`'s binary file body ("" for none). */
export function setBodyFilePath(stores: Stores, id: RequestId, path: string): void {
  const body = draftBody(stores, id);
  if (body === undefined || body.mode !== "file" || body.path === path) return;
  editRequest(stores, id, { body: { ...body, path } });
}

/** Sets the Content-Type override of request `id`'s binary file body ("" to guess it from the file name). */
export function setBodyFileContentType(stores: Stores, id: RequestId, contentType: string): void {
  const body = draftBody(stores, id);
  if (body === undefined || body.mode !== "file" || body.contentType === contentType) return;
  editRequest(stores, id, { body: { ...body, contentType } });
}

export type PickFileOutcome =
  /** The file was chosen and saved in the body. */
  | { status: "picked"; file: BodyFileDTO }
  /** The dialog was cancelled, or the body changed while it was open: nothing changed. */
  | { status: "cancelled" }
  /** The dialog could not be shown. */
  | { status: "failed"; message: string };

/** Shows the native file dialog; the chosen file, or the outcome when none was chosen. */
export async function pickFile(): Promise<BodyFileDTO | PickFileOutcome> {
  try {
    const file = await pickBodyFile();
    if (file.path === "") return { status: "cancelled" };
    rememberBodyFile(file);
    return file;
  } catch (err) {
    if (isCancelled(err)) return { status: "cancelled" };
    return { status: "failed", message: toAppError(err).message };
  }
}

export const isPickOutcome = (v: BodyFileDTO | PickFileOutcome): v is PickFileOutcome => "status" in v;

/**
 * Shows the native file dialog and makes the chosen file the binary body of
 * request `id`. The path saved is the one Go returns: relative to the
 * project root when the file is inside the project, else absolute.
 */
export async function chooseBodyFile(stores: Stores, id: RequestId): Promise<PickFileOutcome> {
  const result = await pickFile();
  if (isPickOutcome(result)) return result;
  if (draftBody(stores, id)?.mode !== "file") return { status: "cancelled" };
  setBodyFilePath(stores, id, result.path);
  return { status: "picked", file: result };
}

/**
 * Shows the native file dialog and makes multipart part `index` of request
 * `id` a file part with the chosen file (its text value is dropped). When
 * the parts changed so that there is no part `index` any more, nothing is
 * saved.
 */
export async function choosePartFile(stores: Stores, id: RequestId, index: number): Promise<PickFileOutcome> {
  const result = await pickFile();
  if (isPickOutcome(result)) return result;
  const parts = draftBody(stores, id)?.parts ?? [];
  if (draftBody(stores, id)?.mode !== "multipart" || index >= parts.length) return { status: "cancelled" };
  updatePart(stores, id, index, (p) => ({ ...p, file: result.path, value: "" }));
  return { status: "picked", file: result };
}

/** True when request `id`'s body is a raw JSON body, the one the JSON tools work on. */
export function isJsonBody(body: BodyDTO | undefined): boolean {
  return body !== undefined && body.mode === "raw" && body.type === "json";
}

export type JsonFormat = "beautify" | "minify";

export type FormatOutcome =
  /** The body now holds the formatted text (or already did). */
  | { status: "formatted"; text: string }
  /** The JSON is invalid: nothing changed. */
  | { status: "invalid"; error: JSONErrorDTO }
  /** Nothing to do: no JSON body, blank text, or the text changed while Go worked. */
  | { status: "skipped" }
  /** The call failed (not for invalid JSON). */
  | { status: "failed"; message: string };

/**
 * Beautifies or minifies request `id`'s raw JSON body in Go and saves the
 * result as an edit (undoable in the editor). Invalid JSON is not changed;
 * the outcome carries the error's position. When the text changes while Go
 * works, the result is dropped.
 */
export async function formatRequestBody(stores: Stores, id: RequestId, format: JsonFormat): Promise<FormatOutcome> {
  const body = draftBody(stores, id);
  if (!isJsonBody(body) || body === undefined || body.text.trim() === "") return { status: "skipped" };
  const text = body.text;
  let result: JSONFormatDTO;
  try {
    result = await (format === "beautify" ? beautifyJson(text) : minifyJson(text));
  } catch (err) {
    if (isCancelled(err)) return { status: "skipped" };
    return { status: "failed", message: toAppError(err).message };
  }
  const now = draftBody(stores, id);
  if (!isJsonBody(now) || now?.text !== text) return { status: "skipped" };
  if (!result.valid || result.error !== null) {
    return result.error !== null ? { status: "invalid", error: result.error } : { status: "skipped" };
  }
  setBodyText(stores, id, result.text);
  return { status: "formatted", text: result.text };
}
