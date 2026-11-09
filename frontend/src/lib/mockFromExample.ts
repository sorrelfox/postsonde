// A mock route from a response (MOCK-03): Go makes the route of a response
// example, or of a history entry made into one (HistoryService.Example,
// secrets masked), and merges it into a mock server's routes for a
// preview (ServersService.MockRouteFromExample). Nothing changes on any
// server until the result's config is applied. See
// docs/mock-routes.md#routes-from-a-response.
import {
  ServersService,
  type MockDroppedHeaderDTO,
  type MockExampleInputDTO,
  type MockExampleNoteDTO,
  type MockExampleResultDTO,
  type MockExistingRouteDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers";
import type { CancellablePromise } from "./calls";

export type { MockDroppedHeaderDTO, MockExampleInputDTO, MockExampleNoteDTO, MockExampleResultDTO, MockExistingRouteDTO };

/** How the route goes into the server's routes: a new route, a named response of the route already there, or in place of its response. */
export type MockExampleMode = "route" | "response" | "replace";

/** Go's merge statuses. */
export type MockExampleStatus = "added" | "existing" | "response_added" | "replaced" | "over_limit";

/** The route of `input.example`, merged into `input.config` by `input.mode`; a preview. */
export function mockRouteFromExample(input: MockExampleInputDTO): CancellablePromise<MockExampleResultDTO> {
  return ServersService.MockRouteFromExample(input);
}
