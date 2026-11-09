// Mock server route commands (MOCK-01, MOCK-02, MOCK-04, MOCK-05, MOCK-07): edits of a mock
// server's routes go into a draft in the servers store at once and reach Go
// (ServersService.UpdateConfig) once typing pauses. A running server applies
// them live. The routes of a server saved in a project folder are written
// to its _folder.json by Go with each update (SRV-04); others live in
// memory. See
// docs/servers.md and docs/frontend.md (Routes editor).

import { t } from "../i18n";
import { toAppError } from "../lib/errors";
import * as api from "../lib/servers";
import type { MockConfig, MockResponse, MockRoute, Stores } from "../stores";
import { toLocalServer, toMockConfigDTO } from "./servers";

/** How long edits must pause before the routes are sent, in ms. */
export const MOCK_UPDATE_DELAY_MS = 300;

/** The methods the route editor offers; any other token can be typed. */
export const MOCK_METHODS: readonly string[] = ["ANY", "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"];

/** The longest delay Go accepts (mockhttp.MaxDelay), in ms. */
export const MOCK_MAX_DELAY_MS = 300_000;

const timers = new Map<string, ReturnType<typeof setTimeout>>();
/** The last update sent per server; an older answer is ignored. */
const sent = new Map<string, number>();
let sendSeq = 0;

let idSeq = 0;

/** A new route id, like Go's (rt_ and hex digits). */
export function newRouteId(): string {
  const c = globalThis.crypto;
  if (c && typeof c.getRandomValues === "function") {
    const b = new Uint8Array(6);
    c.getRandomValues(b);
    return `rt_${Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("")}`;
  }
  idSeq += 1;
  return `rt_${Date.now().toString(16)}${idSeq}`;
}

/** A new route: GET on a path not used yet, 200 with an empty JSON object. */
export function newMockRoute(existing: readonly MockRoute[], id: string = newRouteId()): MockRoute {
  const paths = new Set(existing.map((r) => r.path));
  let path = "/new";
  for (let n = 2; paths.has(path); n++) path = `/new-${n}`;
  return {
    id,
    enabled: true,
    method: "GET",
    path,
    status: 200,
    headers: [],
    body: { mode: "raw", type: "json", text: "{}" },
    delay: { mode: "none", ms: 0, minMs: 0, maxMs: 0 },
    responses: [],
    templated: false,
  };
}

/** The request header that picks a route's response by name or status (Go's mockhttp.SelectHeader). */
export const MOCK_SELECT_HEADER = "X-PostSonde-Response";

/** The name that selects a route's own response with MOCK_SELECT_HEADER. */
export const MOCK_DEFAULT_RESPONSE = "default";

/** Where a response rule looks (mockhttp rule sources); labels and placeholders are looked up when read (UI-10). */
export const MOCK_RULE_SOURCES: readonly { readonly source: string; readonly label: string; readonly placeholder: string }[] = [
  {
    source: "query",
    get label() {
      return t("commands.mock.source.query");
    },
    get placeholder() {
      return t("commands.mock.source.queryPlaceholder");
    },
  },
  {
    source: "header",
    get label() {
      return t("commands.mock.source.header");
    },
    placeholder: "X-Header",
  },
  {
    source: "json",
    get label() {
      return t("commands.mock.source.json");
    },
    placeholder: "$.user.role",
  },
  {
    source: "body",
    get label() {
      return t("commands.mock.source.body");
    },
    placeholder: "",
  },
];

/** The rule operators (mockhttp rule operators, as in assertions), labels looked up when read. */
export const MOCK_RULE_OPS: readonly { readonly op: string; readonly label: string; readonly needsValue: boolean }[] = [
  {
    op: "equals",
    get label() {
      return t("commands.mock.op.equals");
    },
    needsValue: true,
  },
  {
    op: "not_equals",
    get label() {
      return t("commands.mock.op.notEquals");
    },
    needsValue: true,
  },
  {
    op: "contains",
    get label() {
      return t("commands.mock.op.contains");
    },
    needsValue: true,
  },
  {
    op: "not_contains",
    get label() {
      return t("commands.mock.op.notContains");
    },
    needsValue: true,
  },
  {
    op: "matches",
    get label() {
      return t("commands.mock.op.matches");
    },
    needsValue: true,
  },
  {
    op: "exists",
    get label() {
      return t("commands.mock.op.exists");
    },
    needsValue: false,
  },
  {
    op: "not_exists",
    get label() {
      return t("commands.mock.op.notExists");
    },
    needsValue: false,
  },
];

/** A new response for a route: a name not used yet, 200 with an empty JSON object, no rules (so only the header picks it). */
export function newMockResponse(route: MockRoute): MockResponse {
  const names = new Set(route.responses.map((r) => r.name.toLowerCase()));
  let name = "response-1";
  for (let n = 2; names.has(name); n++) name = `response-${n}`;
  return {
    name,
    status: 200,
    headers: [],
    body: { mode: "raw", type: "json", text: "{}" },
    rules: [],
    templated: false,
  };
}

/**
 * Sets a mock server's routes: the editor shows `config` at once, and Go
 * gets it after `delayMs` without further edits.
 */
export function editMockConfig(
  stores: Stores,
  id: string,
  config: MockConfig,
  delayMs: number = MOCK_UPDATE_DELAY_MS,
): void {
  stores.servers.dispatch({ type: "servers/mockDraft", id, config });
  const pending = timers.get(id);
  if (pending !== undefined) clearTimeout(pending);
  timers.set(
    id,
    setTimeout(() => {
      void flushMockConfig(stores, id);
    }, delayMs),
  );
}

/** Replaces one route of a mock server's routes (by id). */
export function editMockRoute(stores: Stores, id: string, route: MockRoute, delayMs?: number): void {
  const config = currentConfig(stores, id);
  if (config === null) return;
  editMockConfig(stores, id, { ...config, routes: config.routes.map((r) => (r.id === route.id ? route : r)) }, delayMs);
}

function currentConfig(stores: Stores, id: string): MockConfig | null {
  const state = stores.servers.getState();
  return state.mockDrafts[id] ?? state.byId[id]?.mock ?? null;
}

/**
 * Sends a server's draft routes to Go now. The answer updates the server
 * (normalized routes, diagnostics); the draft is dropped when nothing was
 * edited meanwhile. A failure is kept in `mockErrors`, with the draft.
 */
export async function flushMockConfig(stores: Stores, id: string): Promise<void> {
  const pending = timers.get(id);
  if (pending !== undefined) clearTimeout(pending);
  timers.delete(id);
  const config = stores.servers.getState().mockDrafts[id];
  if (config === undefined) return;
  const seq = ++sendSeq;
  sent.set(id, seq);
  try {
    const dto = await api.updateServerConfig(id, { mockHttp: toMockConfigDTO(config), webSocket: null, webhook: null });
    if (sent.get(id) !== seq) return;
    stores.servers.dispatch({ type: "servers/put", server: toLocalServer(dto) });
    stores.servers.dispatch({ type: "servers/mockSaved", id, config });
  } catch (err: unknown) {
    if (sent.get(id) !== seq) return;
    const error = toAppError(err);
    console.warn(`servers: ${error.message}`);
    stores.servers.dispatch({
      type: "servers/mockFailed",
      id,
      error: error.hint ? `${error.message} ${error.hint}` : error.message,
    });
  }
}

/** Sends every pending draft now (tests, and before the view closes). */
export async function flushAllMockConfigs(stores: Stores): Promise<void> {
  await Promise.all([...timers.keys()].map((id) => flushMockConfig(stores, id)));
}
