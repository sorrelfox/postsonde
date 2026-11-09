import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  Create: vi.fn<(input: unknown) => Promise<unknown>>(),
  Start: vi.fn<(id: string) => Promise<unknown>>(),
  Stop: vi.fn<(id: string) => Promise<unknown>>(),
  Remove: vi.fn<(id: string) => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
  ClearTraffic: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));

import type { LogMessage, MessageSource } from "../lib/messageSource";
import { SERVERS_CHANGED_EVENT, SERVERS_TRAFFIC_EVENT } from "../lib/servers";
import { TrafficSources } from "../lib/serverTraffic";
import { createStores } from "../stores";
import { deferred, emit, listenerCount, resetFakeWails, settle } from "../test/fakeWails";
import { manualFrames } from "../test/manualFrames";
import { runningDTO, serverDTO, trafficDTO } from "../test/servers";
import {
  clearServerTraffic,
  createAndStartServer,
  followServers,
  removeServer,
  stopServer,
  toLocalServer,
} from "./servers";

function texts(source: MessageSource): (string | undefined)[] {
  source.flush(); // one manual frame scheduler is shared by several sources
  const snap = source.getSnapshot();
  const out: (string | undefined)[] = [];
  for (let seq = snap.firstSeq; seq < snap.nextSeq; seq++) out.push((source.get(seq) as LogMessage).text);
  return out;
}

function rejection(code: string, message = "Nope", hint = ""): Error {
  const err = new Error(message);
  (err as Error & { cause: unknown }).cause = { kind: "network", code, message, hint, details: {}, causes: [] };
  return err;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
  service.Types.mockResolvedValue([{ name: "echo_http", label: "Echo HTTP" }]);
  service.List.mockResolvedValue([]);
  service.Traffic.mockResolvedValue([]);
});

describe("toLocalServer", () => {
  it("parses times and keeps the error and warning", () => {
    const s = toLocalServer(
      runningDTO({
        warning: { code: "listen.lan_exposed", message: "Exposed" },
        stoppedAt: "",
        error: { kind: "network", code: "c", message: "m", hint: "h", details: {}, causes: [] },
      }),
    );
    expect(s.startedAt).toBe(Date.parse("2026-09-16T10:00:00Z"));
    expect(s.stoppedAt).toBeNull();
    expect(s.warning).toEqual({ code: "listen.lan_exposed", message: "Exposed" });
    expect(s.error).toEqual({ code: "c", message: "m", hint: "h" });
    expect(toLocalServer(serverDTO({ state: "weird", addresses: null as unknown as string[] }))).toMatchObject({
      state: "failed",
      addresses: [],
    });
  });
});

describe("followServers", () => {
  it("loads servers, types and recent traffic, then applies live traffic queued during the load", async () => {
    const frames = manualFrames();
    const sources = new TrafficSources({ schedule: frames.schedule });
    const stores = createStores();
    const recent = deferred<unknown>();
    service.List.mockResolvedValue([runningDTO()]);
    service.Traffic.mockReturnValue(recent.promise);

    const stop = followServers(stores, sources);
    expect(stores.servers.getState().status).toBe("loading");
    await settle();
    // Live traffic while the recent traffic is still loading: overlaps seq 2.
    emit(SERVERS_TRAFFIC_EVENT, { events: [trafficDTO({ seq: 2, summary: "live 2" }), trafficDTO({ seq: 3, summary: "live 3" })] });
    recent.resolve([trafficDTO({ seq: 1, summary: "old 1" }), trafficDTO({ seq: 2, summary: "old 2" })]);
    await settle();
    frames.frame();

    expect(service.Traffic).toHaveBeenCalledWith("srv_1");
    expect(stores.servers.getState()).toMatchObject({ status: "ready", ids: ["srv_1"], types: [{ name: "echo_http", label: "Echo HTTP" }] });
    expect(texts(sources.get("srv_1"))).toEqual(["old 1", "old 2", "live 3"]);

    // Later traffic goes straight to the source; another server gets its own.
    emit(SERVERS_TRAFFIC_EVENT, { events: [trafficDTO({ seq: 4, summary: "live 4" }), trafficDTO({ serverId: "srv_2", seq: 1, summary: "other" })] });
    frames.frame();
    expect(texts(sources.get("srv_1"))).toEqual(["old 1", "old 2", "live 3", "live 4"]);
    expect(texts(sources.get("srv_2"))).toEqual(["other"]);

    // servers:changed replaces the list and drops the logs of removed servers.
    emit(SERVERS_CHANGED_EVENT, { servers: [serverDTO({ id: "srv_1", state: "stopping" })] });
    expect(stores.servers.getState().byId.srv_1.state).toBe("stopping");
    expect(sources.last("srv_2")).toBe(0);

    stop();
    expect(listenerCount()).toBe(0);
  });

  it("keeps a load failure in the store and still applies live traffic", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const frames = manualFrames();
    const sources = new TrafficSources({ schedule: frames.schedule });
    const stores = createStores();
    service.List.mockRejectedValue(rejection("x", "No servers"));
    const stop = followServers(stores, sources);
    emit(SERVERS_TRAFFIC_EVENT, { events: [trafficDTO({ summary: "queued" })] });
    await settle();
    frames.frame();
    expect(stores.servers.getState()).toMatchObject({ status: "error", error: "No servers" });
    expect(texts(sources.get("srv_1"))).toEqual(["queued"]);
    stop();
    warn.mockRestore();
  });
});

describe("server commands", () => {
  it("creates, selects and starts a server", async () => {
    const stores = createStores();
    service.Create.mockResolvedValue(serverDTO());
    service.Start.mockResolvedValue(runningDTO());
    const { server, startError } = await createAndStartServer(stores, { type: "echo_http", host: "localhost", port: 8080, allowLan: true });
    expect(service.Create).toHaveBeenCalledWith({ type: "echo_http", name: "", host: "localhost", port: 8080, allowLan: true, tls: null, config: { mockHttp: null, webSocket: null, webhook: null } });
    expect(service.Start).toHaveBeenCalledWith("srv_1");
    expect(startError).toBeNull();
    expect(server.state).toBe("running");
    expect(stores.servers.getState()).toMatchObject({ ids: ["srv_1"], selectedId: "srv_1" });
    expect(stores.servers.getState().byId.srv_1.state).toBe("running");
  });

  it("keeps a server that failed to start and reports why", async () => {
    const stores = createStores();
    service.Create.mockResolvedValue(serverDTO());
    service.Start.mockImplementation(async () => {
      emit(SERVERS_CHANGED_EVENT, {});
      throw rejection("listen.address_in_use", "127.0.0.1:8080 is already in use.", "Pick another port.");
    });
    const { server, startError } = await createAndStartServer(stores, { type: "echo_http", host: "", port: 8080, allowLan: false });
    expect(startError).toMatchObject({ code: "listen.address_in_use", hint: "Pick another port." });
    expect(server.id).toBe("srv_1");
    expect(stores.servers.getState().selectedId).toBe("srv_1");
  });

  it("rejects when the server cannot be created", async () => {
    const stores = createStores();
    service.Create.mockRejectedValue(rejection("listen.invalid_host", "Bad host"));
    await expect(createAndStartServer(stores, { type: "echo_http", host: "x.y", port: 0, allowLan: false })).rejects.toMatchObject({
      code: "listen.invalid_host",
    });
    expect(service.Start).not.toHaveBeenCalled();
    expect(stores.servers.getState().ids).toEqual([]);
  });

  it("stops, clears and removes", async () => {
    const frames = manualFrames();
    const sources = new TrafficSources({ schedule: frames.schedule });
    const stores = createStores();
    stores.servers.dispatch({ type: "servers/set", servers: [toLocalServer(runningDTO()), toLocalServer(serverDTO({ id: "srv_2" }))] });
    stores.servers.dispatch({ type: "servers/select", id: "srv_1" });

    service.Stop.mockResolvedValue(serverDTO({ state: "stopped" }));
    expect((await stopServer(stores, "srv_1")).state).toBe("stopped");
    expect(stores.servers.getState().byId.srv_1.state).toBe("stopped");

    sources.append([trafficDTO()]);
    frames.frame();
    service.ClearTraffic.mockResolvedValue(undefined);
    await clearServerTraffic("srv_1", sources);
    expect(service.ClearTraffic).toHaveBeenCalledWith("srv_1");
    expect(sources.get("srv_1").getSnapshot().count).toBe(0);

    service.Remove.mockResolvedValue(undefined);
    await removeServer(stores, "srv_1", sources);
    expect(stores.servers.getState()).toMatchObject({ ids: ["srv_2"], selectedId: null });
    expect(sources.last("srv_1")).toBe(0);
  });
});
