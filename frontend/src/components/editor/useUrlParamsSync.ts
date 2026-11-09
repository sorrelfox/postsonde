import { useCallback } from "react";
import type { KeyValueDTO, RequestDTO } from "../../lib/collection";
import { syncPathParams, type PathParamStash } from "../../lib/pathParams";
import { paramsToUrl, urlToParams } from "../../lib/urlParams";
import { useStores, type RequestData, type RequestId } from "../../stores";

export interface UrlParamsSync {
  /** For the URL field: saves the URL, and the query and path rows that follow from it. */
  onUrlChange: (url: string) => void;
  /** For the Params table: saves the rows and the URL rewritten from them. */
  onParamsChange: (params: KeyValueDTO[]) => void;
  /** For the Path variables table: saves the rows (their names follow the URL). */
  onPathParamsChange: (pathParams: KeyValueDTO[]) => void;
}

/**
 * Keeps request `id`'s `url`, `params` (lib/urlParams.ts) and
 * `pathParams` (HTTP-05, lib/pathParams.ts) in step. Each user edit becomes
 * one `edit` patch holding the fields that changed, so auto-save sends once.
 * Path rows whose names leave the URL go to the drafts store's
 * `pathParamStash` and come back with their names. The handlers read the
 * draft from the store, not from the last render, so quick successive edits
 * build on each other.
 */
export function useUrlParamsSync(id: RequestId, edit: (patch: RequestData) => void): UrlParamsSync {
  const stores = useStores();

  const onUrlChange = useCallback(
    (url: string) => {
      const state = stores.drafts.getState();
      const request = state.byId[id]?.request as unknown as RequestDTO | undefined;
      if (request === undefined) return;
      const patch: Record<string, unknown> = { url };
      const params = urlToParams(url, request.params);
      if (params !== request.params) patch.params = params;
      const stash = state.pathParamStash[id] as PathParamStash | undefined;
      const path = syncPathParams(url, request.pathParams, stash);
      if (path.rows !== request.pathParams) patch.pathParams = path.rows;
      if (path.removed.length > 0) stores.drafts.dispatch({ type: "drafts/stashPathParams", id, rows: path.removed });
      edit(patch);
    },
    [stores, id, edit],
  );

  const onParamsChange = useCallback(
    (params: KeyValueDTO[]) => {
      const request = stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined;
      if (request === undefined) return;
      const url = paramsToUrl(request.url, params);
      edit(url === request.url ? { params } : { params, url });
    },
    [stores, id, edit],
  );

  const onPathParamsChange = useCallback((pathParams: KeyValueDTO[]) => edit({ pathParams }), [edit]);

  return { onUrlChange, onParamsChange, onPathParamsChange };
}
