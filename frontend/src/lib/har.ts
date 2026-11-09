// Calls into Go for the HAR export (EXP-04, docs/har.md): a folder's
// requests through HTTPService.ExportHAR, history entries through
// HistoryService.ExportHAR. Both show the native save dialog and resolve
// with `saved: false` when the user cancels it.
import {
  HistoryService,
  type ExportHARResultDTO as HistoryExportDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history";
import {
  HTTPService,
  type ExportHARResultDTO as RequestsExportDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import type { CancellablePromise } from "./calls";

export type { HistoryExportDTO, RequestsExportDTO };

/**
 * Writes requests `requestIds` (HTTP, GraphQL, SSE) as a HAR file under a
 * page named `name`, built as they would be sent; secrets masked unless
 * `includeSecrets`.
 */
export function exportRequestsHar(name: string, requestIds: string[], includeSecrets = false): CancellablePromise<RequestsExportDTO> {
  return HTTPService.ExportHAR({ name, requestIds, includeSecrets });
}

/**
 * Writes the history entries of request `requestId` (every entry for "")
 * as a HAR file titled `title` ("" for the default), masked as recorded.
 */
export function exportHistoryHar(requestId: string, title: string): CancellablePromise<HistoryExportDTO> {
  return HistoryService.ExportHAR({ ids: [], requestId, title });
}
