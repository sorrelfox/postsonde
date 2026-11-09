// The variable resolution view (DBG-08): every {{reference}} a request
// uses, the value it is sent with, the scope that wins and every scope
// that defines it, from Go's HTTPService.Resolution, which builds the
// request exactly as a send does (docs/templates.md#resolution-view).
import {
  HTTPService,
  type ResolutionDTO,
  type ResolutionScopeDTO,
  type ResolvedVariableDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import type { CancellablePromise } from "./calls";

export type { ResolutionDTO, ResolutionScopeDTO, ResolvedVariableDTO };

/** The resolution of HTTP, GraphQL or SSE request `id`, its pending edits included. */
export function loadResolution(id: string): CancellablePromise<ResolutionDTO> {
  return HTTPService.Resolution(id);
}

/** Asks the request editor around `from` to show the resolution of `{{name}}` (from a chip's hover). */
export const SHOW_RESOLUTION_EVENT = "postsonde:show-resolution";

export interface ShowResolutionDetail {
  readonly name: string;
}

/** The attribute that marks an element that shows the resolution view when asked (SHOW_RESOLUTION_EVENT). */
export const RESOLUTION_HOST_ATTRIBUTE = "data-resolution-host";

/** Whether `el` sits in an editor that has a resolution view. */
export function hasResolutionHost(el: Element): boolean {
  return el.closest(`[${RESOLUTION_HOST_ATTRIBUTE}]`) !== null;
}

/** Sends SHOW_RESOLUTION_EVENT for `{{name}}` from `from`; it bubbles to the editor. */
export function requestShowResolution(from: Element, name: string): void {
  from.dispatchEvent(new CustomEvent<ShowResolutionDetail>(SHOW_RESOLUTION_EVENT, { bubbles: true, detail: { name } }));
}
