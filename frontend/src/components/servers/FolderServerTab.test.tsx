import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";

const service = vi.hoisted(() => ({
  CreateFolderServer: vi.fn<(input: unknown) => Promise<unknown>>(),
  UpdateServer: vi.fn<(id: string, input: unknown) => Promise<unknown>>(),
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  Start: vi.fn<(id: string) => Promise<unknown>>(),
  Stop: vi.fn<(id: string) => Promise<unknown>>(),
  Remove: vi.fn<(id: string) => Promise<unknown>>(),
  SyncProject: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));

import { syncProjectServers, toLocalServer } from "../../commands/servers";
import type { ServerDTO } from "../../lib/servers";
import {
  createStores,
  selectFolderServer,
  selectFolderServerBadge,
  serverKindBadge,
  StoreProvider,
  type Stores,
  type TreeNode,
} from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { runningDTO, serverDTO } from "../../test/servers";
import { FolderServerTab, isBeyondLocalhost } from "./FolderServerTab";

const FOLDER = "fld_chat";
const WS = "ws_1";

const NODES = [
  { id: FOLDER, kind: "folder", parentId: null, name: "Chat", path: "chat", fileKind: "", readOnly: "", childIds: [] },
] as unknown as TreeNode[];

/** A WebSocket server saved in the Chat folder. */
function savedDTO(over: Partial<ServerDTO> = {}): ServerDTO {
  return serverDTO({
    id: "srv_saved",
    type: "websocket",
    typeLabel: "WebSocket",
    name: "WebSocket",
    host: "localhost",
    port: 8081,
    config: { mockHttp: null, webSocket: { path: "/ws", subprotocols: [], echo: true, maxMessageSize: 0, rules: null }, webhook: null },
    folder: { workspaceId: WS, folderId: FOLDER, folderName: "Chat" },
    ...over,
  });
}

function setup(dtos: ServerDTO[] = [], opts: { readOnly?: boolean } = {}): Stores {
  const stores = createStores();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: WS });
  stores.collection.dispatch({ type: "collection/load", workspaceId: WS, nodes: NODES, rootIds: [FOLDER] });
  stores.servers.dispatch({ type: "servers/loaded", servers: dtos.map(toLocalServer), types: [] });
  render(
    <StoreProvider stores={stores}>
      <FolderServerTab folderId={FOLDER} readOnly={opts.readOnly ?? false} />
    </StoreProvider>,
  );
  return stores;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
});

describe("FolderServerTab", () => {
  it("adds a server of the chosen kind to the folder", async () => {
    service.CreateFolderServer.mockResolvedValue(savedDTO());
    const stores = setup();
    expect(screen.getByText("No server in this folder")).toBeTruthy();
    fireEvent.click(screen.getByRole("radio", { name: "WebSocket" }));
    fireEvent.click(screen.getByRole("button", { name: "Add server" }));
    await act(settle);
    expect(service.CreateFolderServer).toHaveBeenCalledWith({
      workspaceId: WS,
      folderId: FOLDER,
      server: {
        type: "websocket",
        name: "",
        host: "localhost",
        port: 0,
        allowLan: false,
        tls: null,
        config: { mockHttp: null, webSocket: null, webhook: null },
      },
    });
    expect(selectFolderServer(stores.servers.getState(), WS, FOLDER)?.id).toBe("srv_saved");
    expect(screen.getByTestId("folder-server").textContent).toContain("Stopped");
    // The WebSocket settings: path and the auto-reply (echo) mode.
    expect(screen.getByRole("region", { name: "WebSocket settings" })).toBeTruthy();
  });

  it("shows an error when the folder cannot take the server", async () => {
    service.CreateFolderServer.mockRejectedValue({ kind: "unsupported", code: "collection.read_only", message: "The file cannot be changed.", hint: "" });
    setup();
    fireEvent.click(screen.getByRole("button", { name: "Add server" }));
    await act(settle);
    expect(screen.getByRole("alert").textContent).toContain("The file cannot be changed.");
  });

  it("starts the saved server, and locks its address while it runs", async () => {
    service.Start.mockResolvedValue(runningDTO({ ...savedDTO(), state: "running", boundPort: 8081, addresses: ["127.0.0.1:8081"] }));
    setup([savedDTO()]);
    const port = screen.getByRole("textbox", { name: "Port" }) as HTMLInputElement;
    expect(port.value).toBe("8081");
    expect(port.disabled).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: /Start/ }));
    await act(settle);
    expect(service.Start).toHaveBeenCalledWith("srv_saved");
    expect((screen.getByRole("textbox", { name: "Port" }) as HTMLInputElement).disabled).toBe(true);
    expect(screen.getByText("Stop the server to change where it listens.")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Stop/ })).toBeTruthy();
  });

  it("warns when binding beyond localhost and saves the address when the field is left", async () => {
    service.UpdateServer.mockResolvedValue(savedDTO({ host: "0.0.0.0" }));
    setup([savedDTO()]);
    const host = screen.getByRole("textbox", { name: "Bind address" });
    fireEvent.change(host, { target: { value: "0.0.0.0" } });
    expect(screen.getByRole("note").textContent).toContain("will not start until connections from other devices are allowed");
    fireEvent.blur(host);
    await act(settle);
    expect(service.UpdateServer).toHaveBeenCalledWith("srv_saved", { name: "", host: "0.0.0.0", port: 8081, allowLan: false, tls: null });
    // An unchanged field saves nothing.
    service.UpdateServer.mockClear();
    fireEvent.blur(screen.getByRole("textbox", { name: "Port" }));
    expect(service.UpdateServer).not.toHaveBeenCalled();
  });

  it("allows LAN access with its warning", async () => {
    service.UpdateServer.mockResolvedValue(savedDTO({ host: "0.0.0.0", allowLan: true }));
    setup([savedDTO({ host: "0.0.0.0" })]);
    fireEvent.click(screen.getByRole("checkbox", { name: "Allow connections from other devices (LAN)" }));
    await act(settle);
    expect(service.UpdateServer).toHaveBeenCalledWith("srv_saved", { name: "", host: "0.0.0.0", port: 8081, allowLan: true, tls: null });
    expect(screen.getByRole("note").textContent).toContain("Binds 0.0.0.0");
  });

  it("removes the server from the folder after asking", async () => {
    service.Remove.mockResolvedValue(undefined);
    setup([savedDTO()]);
    fireEvent.click(screen.getByRole("button", { name: "Remove server…" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await act(settle);
    expect(service.Remove).toHaveBeenCalledWith("srv_saved");
    expect(screen.getByText("No server in this folder")).toBeTruthy();
  });

  it("is read-only in a read-only folder", () => {
    setup([savedDTO()], { readOnly: true });
    expect((screen.getByRole("textbox", { name: "Bind address" }) as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Remove server…" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("reports a saved server that could not be loaded", async () => {
    service.SyncProject.mockResolvedValue({
      workspaceId: WS,
      problems: [
        {
          folderId: FOLDER,
          folderName: "Chat",
          error: { kind: "invalid_argument", code: "servers.unknown_type", message: '"udp" is not a kind of server PostSonde can run.', hint: "", details: {}, causes: [] },
        },
      ],
    });
    const stores = setup();
    await act(() => syncProjectServers(stores));
    expect(screen.getByRole("alert").textContent).toContain('"udp" is not a kind of server');
  });
});

describe("saved server helpers", () => {
  it("tells loopback from other bind addresses", () => {
    for (const h of ["", "localhost", "127.0.0.1", "::1", " LOCALHOST "]) expect(isBeyondLocalhost(h)).toBe(false);
    for (const h of ["0.0.0.0", "192.168.1.5", "::"]) expect(isBeyondLocalhost(h)).toBe(true);
  });

  it("badges a folder while its server listens", () => {
    const stores = createStores();
    stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(savedDTO())], types: [] });
    expect(selectFolderServerBadge(stores.servers.getState(), WS, FOLDER)).toBeNull();
    stores.servers.dispatch({
      type: "servers/set",
      servers: [toLocalServer(savedDTO({ state: "running", boundPort: 8081, addresses: ["127.0.0.1:8081"] }))],
    });
    expect(selectFolderServerBadge(stores.servers.getState(), WS, FOLDER)).toBe("WS :8081");
    // Another project's folder of the same ID has no badge.
    expect(selectFolderServerBadge(stores.servers.getState(), "ws_2", FOLDER)).toBeNull();
    expect(serverKindBadge("mock_http")).toBe("MOCK");
    expect(serverKindBadge("udp")).toBe("UDP");
  });
});
