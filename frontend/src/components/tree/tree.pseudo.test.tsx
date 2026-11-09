// The collection tree, its context menu, the Import dialog, the
// Environments panel, the cookie manager and the console in the
// pseudo-locale (UI-10): every text they show must come from the
// catalogue, so no English text of their namespaces may be left
// (docs/frontend.md#localization).
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const collection = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  ImportFormats: vi.fn<() => Promise<unknown>>(),
  PickImportFile: vi.fn<() => Promise<string>>(),
  PreviewImport: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const environments = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
  SetActive: vi.fn<(id: string) => Promise<unknown>>(),
  GlobalVariables: vi.fn<() => Promise<unknown>>(),
}));
const cookies = vi.hoisted(() => ({ List: vi.fn<() => Promise<unknown>>() }));
const consoleService = vi.hoisted(() => ({ List: vi.fn<() => Promise<unknown>>(), Clear: vi.fn<() => Promise<unknown>>() }));
const network = vi.hoisted(() => ({ List: vi.fn<() => Promise<unknown>>(), Clear: vi.fn<() => Promise<unknown>>() }));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: collection,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: environments,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/cookies", () => ({
  CookiesService: cookies,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/console", () => ({
  ConsoleService: consoleService,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/network", () => ({
  NetworkService: network,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/logexport", async () =>
  (await import("../../test/fakeLogExport")).fakeLogExportBindings,
);

import { environmentsWorkspaceKey } from "../../commands/environments";
import type { Namespace } from "../../i18n";
import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale } from "../../i18n";
import { pseudoString } from "../../i18n/pseudo";
import type { ImportReportDTO, NodeDTO, TreeDTO } from "../../lib/collection";
import { ConsoleFeed } from "../../lib/console";
import type { CookieDTO } from "../../lib/cookies";
import { ConnectionFeed } from "../../lib/network";
import {
  createStores,
  initialEnvironmentsState,
  StoreProvider,
  type Environment,
  type Stores,
  type TreeNode,
  type WorkspaceInfo,
} from "../../stores";
import { englishLeftovers, isPseudo, visibleTexts } from "../../test/pseudoLocale";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { ConfirmHost } from "../common/Dialog";
import { ConnectionsPanel } from "../console/ConnectionsPanel";
import { ConsolePanel } from "../console/ConsolePanel";
import { CookiesButton } from "../cookies/CookiesButton";
import { EnvironmentSwitcher } from "../environments/EnvironmentSwitcher";
import { Sidebar } from "../shell/Sidebar";

/** The namespaces of the areas this test renders. */
const CONVERTED: readonly Namespace[] = ["tree", "environments", "console", "cookies", "tabs", "workspace", "app"];

/**
 * Text that is not the catalogue's: the level column of a console record,
 * which shows the record's level token as Go wrote it ("info"), like the
 * copied log line.
 */
const FOREIGN: readonly string[] = ["debug", "info", "warn", "error"];

function expectNoEnglish(roots: readonly Element[], minPseudo = 1) {
  expect(englishLeftovers(roots, CONVERTED, { foreign: FOREIGN })).toEqual([]);
  expect(visibleTexts(roots).filter(isPseudo).length).toBeGreaterThanOrEqual(minPseudo);
}

const shop: WorkspaceInfo = {
  id: "ws_1",
  name: "shop-api",
  path: "/p/shop",
  localDir: "/data/ws_1",
  localStatus: "existing",
  previousPath: "",
  newerFormat: false,
};

function dto(over: Partial<NodeDTO> & { id: string }): NodeDTO {
  return { parentId: null, kind: "request", name: over.id, path: `${over.id}.json`, fileKind: "http", method: "GET", childIds: [], readOnly: "", ...over };
}

const NODES: NodeDTO[] = [
  dto({ id: "orders", name: "Orders", kind: "folder", fileKind: "folder", method: "", path: "orders", childIds: ["get", "ship"] }),
  dto({ id: "get", parentId: "orders", name: "Get order", path: "orders/get-order.json" }),
  dto({ id: "ship", parentId: "orders", name: "Ship order", method: "POST", path: "orders/ship-order.json" }),
  dto({ id: "health", name: "Health", path: "health.json" }),
];

const TREE = {
  workspaceId: "ws_1",
  path: "/p/shop",
  readOnly: "",
  rootIds: ["orders", "health"],
  nodes: NODES,
  problems: [],
  scratch: [],
} as TreeDTO;

function toNodes(tree: TreeDTO): TreeNode[] {
  return (tree.nodes ?? []).map((n) =>
    n.kind === "folder"
      ? { kind: "folder", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, childIds: n.childIds ?? [] }
      : { kind: "request", id: n.id, parentId: n.parentId ?? null, name: n.name, path: n.path, fileKind: n.fileKind, readOnly: n.readOnly, method: n.method },
  );
}

const loc = (line = 0, path = "") => ({ line, column: 0, path });

const PREVIEW: ImportReportDTO = {
  format: { id: "curl", name: "cURL commands", extensions: [".sh"] },
  path: "/home/ada/api.sh",
  fileName: "api.sh",
  digest: "d1",
  intoParent: false,
  items: [
    { kind: "folder", name: "api", parent: -1, fileKind: "", skippedKind: "", method: "", detail: "", status: "new", nodeId: "", source: loc(), diagnostics: [] },
    {
      kind: "request",
      name: "List users",
      parent: 0,
      fileKind: "http",
      skippedKind: "",
      method: "GET",
      detail: "https://api.test/users",
      status: "new",
      nodeId: "",
      source: loc(5, "command 1"),
      diagnostics: [],
    },
    {
      kind: "skipped",
      name: "Imported request",
      parent: 0,
      fileKind: "",
      skippedKind: "curl command",
      method: "",
      detail: "",
      status: "skipped",
      nodeId: "",
      source: loc(14, "command 3"),
      diagnostics: [],
    },
  ],
  diagnostics: [],
  counts: { folders: 1, requests: 1, environments: 0, skipped: 1, failed: 0, lossy: 0, errors: 0, warnings: 0, infos: 0 },
} as ImportReportDTO;

function renderSidebar(): Stores {
  const stores = createStores();
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: toNodes(TREE), rootIds: TREE.rootIds ?? [], readOnly: "", expandedIds: ["orders"] });
  render(
    <StoreProvider stores={stores}>
      <Sidebar id="sidebar" view="collections" hidden={false} />
    </StoreProvider>,
  );
  return stores;
}

const row = (id: string) => document.querySelector(`[data-node-id="${id}"]`) as HTMLElement;

beforeEach(() => {
  resetFakeWails();
  localStorage.clear();
  collection.Tree.mockReset().mockResolvedValue(TREE);
  collection.ImportFormats.mockReset().mockResolvedValue([{ id: "curl", name: "cURL commands", extensions: [".sh", ".txt"] }]);
  collection.PickImportFile.mockReset().mockResolvedValue("/home/ada/api.sh");
  collection.PreviewImport.mockReset().mockResolvedValue(PREVIEW);
  environments.List.mockReset().mockResolvedValue({ environments: [], activeId: "", problems: [], readOnly: false });
  environments.SetActive.mockReset().mockResolvedValue(undefined);
  environments.GlobalVariables.mockReset().mockResolvedValue({ variables: [], problems: [] });
  setLocale(PSEUDO_LOCALE);
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("the pseudo-locale", () => {
  it("shows the collection tree and its context menus from the catalogue", () => {
    renderSidebar();
    const tree = screen.getByRole("tree");
    expectNoEnglish([tree]);

    for (const target of [row("get"), row("orders"), tree]) {
      fireEvent.contextMenu(target, { clientX: 10, clientY: 10 });
      const menu = screen.getByRole("menu");
      expect(within(menu).getAllByRole("menuitem").length).toBeGreaterThan(2);
      expectNoEnglish([menu], 3);
      fireEvent.keyDown(menu, { key: "Escape" });
    }
  });

  it("shows the collection tree's empty state from the catalogue", () => {
    const stores = createStores();
    render(
      <StoreProvider stores={stores}>
        <Sidebar id="sidebar" view="collections" hidden={false} />
      </StoreProvider>,
    );
    const empty = screen.getByText(pseudoString("No project open")).parentElement!;
    expect(englishLeftovers([empty], CONVERTED)).toEqual([]);
  });

  it("shows the Import dialog and its preview from the catalogue", async () => {
    renderSidebar();
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: pseudoString("Import file") }));
      await settle();
    });
    const dialog = screen.getByRole("dialog");
    expectNoEnglish([dialog], 3);

    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: pseudoString("Choose file…") }));
      await settle();
    });
    expect(collection.PreviewImport).toHaveBeenCalled();
    expect(within(dialog).getByText("api.sh")).toBeTruthy();
    expectNoEnglish([dialog], 5);
  });

  it("shows the Environments panel and the switcher from the catalogue", () => {
    const staging: Environment = {
      id: "env_s",
      name: "Staging",
      color: "",
      fileName: "staging.json",
      readOnly: false,
      variables: [
        { name: "baseUrl", value: "https://api.stg.shop.test", description: "", secret: false, enabled: true },
        { name: "token", value: "stg-token", description: "", secret: true, enabled: true },
      ],
    };
    const stores = createStores({
      environments: {
        ...initialEnvironmentsState,
        status: "ready",
        workspaceKey: environmentsWorkspaceKey(shop) as string,
        ids: ["env_s"],
        byId: { env_s: staging },
        activeId: "env_s",
      },
    });
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
    render(
      <StoreProvider stores={stores}>
        <EnvironmentSwitcher />
        <Sidebar id="sidebar" view="environments" hidden={false} />
        <ConfirmHost />
      </StoreProvider>,
    );
    const switcher = screen.getByRole("combobox", { name: pseudoString("Environment") });
    const panel = screen.getByRole("complementary");
    expect(within(panel).getByText("Staging")).toBeTruthy();
    expectNoEnglish([switcher.parentElement!, panel], 5);
  });

  it("shows the cookie manager from the catalogue", async () => {
    const cookie = (over: Partial<CookieDTO>): CookieDTO => ({
      key: `key-${over.name ?? "n"}`,
      name: "n",
      value: "v",
      domain: "example.com",
      hostOnly: true,
      path: "/",
      secure: false,
      httpOnly: false,
      sameSite: "",
      expires: "",
      created: "2030-01-01T00:00:00Z",
      lastAccess: "2030-01-01T00:00:00Z",
      binary: false,
      ...over,
    });
    cookies.List.mockReset().mockResolvedValue({
      cookies: [
        cookie({ name: "session", value: "s1", httpOnly: true, secure: true, sameSite: "Lax" }),
        cookie({ name: "theme", value: "dark", domain: "shop.test", hostOnly: false, path: "/app", expires: "2031-01-01T00:00:00Z" }),
      ],
      problem: null,
      jar: { workspace: "Shop API", perEnvironment: false, environmentId: "", environment: "" },
    });
    render(
      <>
        <CookiesButton />
        <ConfirmHost />
      </>,
    );
    const button = screen.getByRole("button", { name: pseudoString("Cookies") });
    expectNoEnglish([button]);
    await act(async () => {
      fireEvent.click(button);
      await settle();
    });
    const dialog = screen.getByRole("dialog", { name: pseudoString("Cookies") });
    expect(within(dialog).getByText("session")).toBeTruthy();
    expectNoEnglish([dialog], 5);
  });

  it("shows the console and the connection log from the catalogue", async () => {
    consoleService.List.mockReset().mockResolvedValue({
      entries: [
        { seq: 1, at: "2026-09-19T10:30:00.123Z", level: "info", message: "message 1", source: "", opId: "", opKind: "", fields: [] },
        { seq: 2, at: "2026-09-19T10:30:00.200Z", level: "warn", message: "message 2", source: "netx", opId: "op_1", opKind: "http.send", fields: [] },
      ],
      capacity: 100,
    });
    network.List.mockReset().mockResolvedValue({
      conns: [
        {
          id: 1,
          opKind: "http.send",
          opId: "op_1",
          network: "tcp",
          address: "host1.example:443",
          localAddr: "192.168.1.20:53211",
          remoteAddr: "93.184.215.14:443",
          state: "closed",
          startedAt: "2026-09-19T10:30:00.123Z",
          connectedAt: "2026-09-19T10:30:00.160Z",
          endedAt: "2026-09-19T10:30:02.123Z",
          connectMs: 37,
          durationMs: 2000,
          bytesIn: 5120,
          bytesOut: 812,
          errorCode: "",
          error: "",
        },
      ],
      active: 0,
      capacity: 500,
    });
    const { unmount } = render(<ConsolePanel feed={new ConsoleFeed()} onClose={() => undefined} onViewChange={() => undefined} />);
    await act(settle);
    expect(screen.getByText("message 2")).toBeTruthy();
    expectNoEnglish([document.body], 5);
    unmount();

    render(<ConnectionsPanel feed={new ConnectionFeed()} onClose={() => undefined} onViewChange={() => undefined} />);
    await act(settle);
    expect(screen.getByText("host1.example:443")).toBeTruthy();
    expectNoEnglish([document.body], 5);
  });
});
