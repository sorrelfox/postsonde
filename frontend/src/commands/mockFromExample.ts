// A mock route from a response (MOCK-03, docs/mock-routes.md#routes-from-a-response):
// the response shown in a request's response pane (the latest send or an
// earlier one, by its history entry) or one of its saved examples becomes
// a route of a mock server, or a named response of the route already
// answering its method and path. Go makes the route and masks it again
// (ServersService.MockRouteFromExample); applying goes through the routes
// editor's usual update (editMockConfig), or creates a mock server with
// the route and starts it.

import { t } from "../i18n";
import { toAppError } from "../lib/errors";
import { exampleFromHistory, type ResponseExampleDTO } from "../lib/responseHistory";
import { mockRouteFromExample, type MockExampleInputDTO, type MockExampleResultDTO } from "../lib/mockFromExample";
import * as api from "../lib/servers";
import type { LocalServer, MockConfig, Stores } from "../stores";
import { editMockConfig, flushMockConfig } from "./mockRoutes";
import { selectServer, startServer, toLocalServer, toMockConfigDTO } from "./servers";
import { showToast } from "./toasts";

/** The response a route is made from: a history entry (Go makes it an example, masked) or a saved example. */
export type MockSource = { readonly kind: "history"; readonly id: string } | { readonly kind: "example"; readonly example: ResponseExampleDTO };

/** The routes of a new mock server: none. */
export const EMPTY_MOCK_CONFIG: MockConfig = { routes: [], strictTrailingSlash: false, caseInsensitive: false };

/** The response of `source` as a response example: a saved one as it is, a history entry made into one by Go (secrets masked). */
export async function exampleOfSource(source: MockSource): Promise<ResponseExampleDTO> {
  if (source.kind === "example") return source.example;
  return exampleFromHistory(source.id);
}

/**
 * The mock servers a route can be added to: every Mock HTTP server but
 * those of another project still running (their routes cannot be changed
 * while their project is closed), in the order created.
 */
export function mockServerChoices(stores: Stores): LocalServer[] {
  const workspaceId = stores.collection.getState().workspaceId;
  const s = stores.servers.getState();
  return s.ids
    .map((id) => s.byId[id])
    .filter((x): x is LocalServer => x !== undefined && x.type === api.MOCK_HTTP_TYPE && (x.folder === null || x.folder.workspaceId === workspaceId));
}

/** A mock server's routes as its editor has them (a pending edit included). */
export function mockConfigOf(stores: Stores, serverId: string): MockConfig {
  const s = stores.servers.getState();
  return s.mockDrafts[serverId] ?? s.byId[serverId]?.mock ?? EMPTY_MOCK_CONFIG;
}

/** Asks Go for the route of `example` merged into `config`; a preview. */
export function previewMockRoute(
  input: Omit<MockExampleInputDTO, "config"> & { config: MockConfig },
): ReturnType<typeof mockRouteFromExample> {
  return mockRouteFromExample({ ...input, config: toMockConfigDTO(input.config) });
}

/** What was added, for the toast: "GET /users/:id". */
function routeLabel(result: MockExampleResultDTO): string {
  return `${result.method} ${result.path}`;
}

/**
 * Applies a preview: the merged routes go to server `serverId` (null: a
 * new mock server is created with them and started). A toast says what
 * was added and offers to show the server. Rejects when Go refuses the
 * routes or the server cannot be created.
 */
export async function applyMockRoute(stores: Stores, serverId: string | null, result: MockExampleResultDTO, config: MockConfig): Promise<void> {
  let target: LocalServer | undefined;
  let startError: string | null = null;
  if (serverId === null) {
    const created = toLocalServer(
      await api.createServer({
        type: api.MOCK_HTTP_TYPE,
        name: "",
        host: "localhost",
        port: 0,
        allowLan: false,
        tls: null,
        config: { mockHttp: toMockConfigDTO(config), webSocket: null, webhook: null },
      }),
    );
    stores.servers.dispatch({ type: "servers/put", server: created });
    target = created;
    try {
      target = await startServer(stores, created.id);
    } catch (err: unknown) {
      startError = toAppError(err).message;
    }
  } else {
    editMockConfig(stores, serverId, config);
    await flushMockConfig(stores, serverId);
    const failed = stores.servers.getState().mockErrors[serverId];
    if (failed !== undefined) throw new Error(failed);
    target = stores.servers.getState().byId[serverId];
  }
  const id = target?.id ?? serverId;
  const server = target?.name ?? "";
  const route = routeLabel(result);
  const message =
    result.status === "response_added"
      ? t("commands.mockFromExample.responseAdded", { name: result.responseName, route, server })
      : result.status === "replaced"
        ? t("commands.mockFromExample.replaced", { route, server })
        : t("commands.mockFromExample.added", { route, server });
  showToast({
    message: startError === null ? message : t("commands.mockFromExample.notStarted", { message, error: startError }),
    action: id === null ? undefined : { label: t("commands.mockFromExample.show"), run: () => selectServer(stores, id) },
  });
}
