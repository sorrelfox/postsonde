// GraphQL file uploads (GQL-07): the Files tab's rows, each a file bound
// to a variable path and sent with the GraphQL multipart request spec by
// Go (request.BuildGraphQL). The rows are stored like multipart parts
// (PartDTO: name is the variable path, file the file as stored, value
// unused), and every change is an auto-saved edit of the request's
// `files`. See docs/graphql.md (File uploads).

import type { PartDTO, RequestDTO, RequestSettingsDTO } from "../lib/collection";
import type { RequestId, Stores } from "../stores";
import { editRequest } from "./autosave";
import { isPickOutcome, pickFile, type PickFileOutcome } from "./body";

/** The header Go adds to a request with files unless a row sets it or it is turned off: request.GraphQLPreflightHeader. */
export const PREFLIGHT_HEADER = "Apollo-Require-Preflight";

/** The files of request `id`'s draft ([] without a draft or files). */
function draftFiles(stores: Stores, id: RequestId): PartDTO[] {
  const request = stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined;
  return request?.files ?? [];
}

/**
 * The rows Go sends: enabled, with a path or a file (a blank row is being
 * filled in and is skipped, as Go skips it).
 */
export function activeUploads(files: readonly PartDTO[] | null | undefined): PartDTO[] {
  return (files ?? []).filter((f) => f.enabled && (f.name.trim() !== "" || f.file !== ""));
}

/** Sets the file rows of request `id`. */
export function setGraphqlFiles(stores: Stores, id: RequestId, files: PartDTO[]): void {
  editRequest(stores, id, { files });
}

/** Changes file row `index` of request `id`; does nothing when there is no such row. */
function updateFile(stores: Stores, id: RequestId, index: number, change: (file: PartDTO) => PartDTO): void {
  const files = draftFiles(stores, id);
  if (index < 0 || index >= files.length) return;
  const next = change(files[index]);
  if (next === files[index]) return;
  setGraphqlFiles(
    stores,
    id,
    files.map((f, i) => (i === index ? next : f)),
  );
}

/** Sets the Content-Type of file row `index` ("" to guess it from the file name). */
export function setGraphqlFileContentType(stores: Stores, id: RequestId, index: number, contentType: string): void {
  updateFile(stores, id, index, (f) => (f.contentType === contentType ? f : { ...f, contentType }));
}

/**
 * Shows the native file dialog and puts the chosen file on row `index` of
 * request `id`. The path saved is the one Go returns: relative to the
 * project root when the file is inside the project, else absolute. When
 * the rows changed so that there is no row `index` any more, nothing is
 * saved.
 */
export async function chooseGraphqlFile(stores: Stores, id: RequestId, index: number): Promise<PickFileOutcome> {
  const result = await pickFile();
  if (isPickOutcome(result)) return result;
  if (index >= draftFiles(stores, id).length) return { status: "cancelled" };
  updateFile(stores, id, index, (f) => ({ ...f, file: result.path, value: "" }));
  return { status: "picked", file: result };
}

/** Whether `settings` send the preflight header (settings.omitHeaders does not name it). */
export function sendsPreflight(settings: RequestSettingsDTO): boolean {
  const lower = PREFLIGHT_HEADER.toLowerCase();
  return !(settings.omitHeaders ?? []).some((n) => n.trim().toLowerCase() === lower);
}

/** Turns the automatic preflight header on or off (settings.omitHeaders), as its own undo step. */
export function setSendPreflight(stores: Stores, id: RequestId, send: boolean): void {
  const request = stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined;
  if (request === undefined || sendsPreflight(request.settings) === send) return;
  const lower = PREFLIGHT_HEADER.toLowerCase();
  const rest = (request.settings.omitHeaders ?? []).filter((n) => n.trim().toLowerCase() !== lower);
  const omitHeaders = send ? (rest.length > 0 ? rest : null) : [...rest, PREFLIGHT_HEADER];
  editRequest(stores, id, { settings: { ...request.settings, omitHeaders } }, { history: "separate" });
}
