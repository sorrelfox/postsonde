import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { ConnectionFeed, type ConnDTO } from "../../lib/network";
import { IS_MAC } from "../../lib/shortcuts";
import { lastCopied, logExportService, resetFakeLogExport } from "../../test/fakeLogExport";
import { emit, fakeRuntime, resetFakeWails, settle } from "../../test/fakeWails";
import { ConnectionsPanel, matchesConnection } from "./ConnectionsPanel";
import { NetworkIndicator } from "./NetworkIndicator";

const networkService = vi.hoisted(() => ({
  List: vi.fn(),
  Clear: vi.fn(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/network", () => ({
  NetworkService: networkService,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/logexport", async () =>
  (await import("../../test/fakeLogExport")).fakeLogExportBindings,
);

function conn(id: number, patch: Partial<ConnDTO> = {}): ConnDTO {
  return {
    id,
    opKind: "http.send",
    opId: `op_${id}`,
    network: "tcp",
    address: `host${id}.example:443`,
    localAddr: "",
    remoteAddr: "",
    via: "",
    state: "connecting",
    startedAt: "2026-09-19T10:30:00.123Z",
    connectedAt: "",
    endedAt: "",
    connectMs: 0,
    durationMs: 0,
    bytesIn: 0,
    bytesOut: 0,
    errorCode: "",
    error: "",
    ...patch,
  };
}

const closed = conn(1, {
  state: "closed",
  remoteAddr: "93.184.215.14:443",
  localAddr: "192.168.1.20:53211",
  connectedAt: "2026-09-19T10:30:00.160Z",
  endedAt: "2026-09-19T10:30:02.123Z",
  connectMs: 37,
  durationMs: 2000,
  bytesIn: 5120,
  bytesOut: 812,
});
const refused = conn(2, { opKind: "", opId: "", state: "refused", errorCode: "netx.not_user_initiated", error: "blocked" });

const targets = () => screen.queryAllByText(/^host\d\.example:443$/).map((el) => el.textContent);

beforeEach(() => {
  resetFakeWails();
  resetFakeLogExport();
  networkService.List.mockReset().mockResolvedValue({ conns: [closed, refused], active: 0, capacity: 500 });
  networkService.Clear.mockReset().mockResolvedValue(undefined);
});

async function renderPanel(feed = new ConnectionFeed(), onClose = vi.fn(), onViewChange = vi.fn()) {
  render(<ConnectionsPanel feed={feed} onClose={onClose} onViewChange={onViewChange} />);
  await act(settle);
  return { feed, onClose, onViewChange };
}

describe("ConnectionsPanel", () => {
  it("copies a focused connection with its error and saves the shown ones (DBG-07)", async () => {
    await renderPanel();
    const rows = screen.getAllByRole("row").slice(1);
    expect(rows[0].getAttribute("tabindex")).toBe("0");
    rows[0].focus();
    fireEvent.keyDown(rows[0], { key: "ArrowDown" });
    expect(document.activeElement).toBe(rows[1]);
    fireEvent.keyDown(rows[1], IS_MAC ? { key: "c", metaKey: true } : { key: "c", ctrlKey: true });
    await act(settle);
    expect(lastCopied(fakeRuntime.Clipboard.SetText)).toBe(
      '2026-09-19T10:30:00.123Z CLOSED     tcp host1.example:443 (http.send op_1) remote=93.184.215.14:443 local=192.168.1.20:53211 connect=37 ms duration=2.00 s in=5120 out=812',
    );
    fireEvent.contextMenu(rows[0]);
    fireEvent.click(screen.getAllByRole("menuitem")[0]);
    await act(settle);
    expect(lastCopied(fakeRuntime.Clipboard.SetText)).toContain('error=netx.not_user_initiated message="blocked"');

    fireEvent.click(screen.getByRole("button", { name: "Save the shown connections…" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Save as JSON…" }));
    await act(settle);
    const doc = JSON.parse(logExportService.Save.mock.calls[0][0].content);
    expect(doc.entries.map((c: ConnDTO & { cause: string }) => [c.id, c.cause])).toEqual([
      [2, "no user action"],
      [1, "http.send op_1"],
    ]);
  });

  it("lists every connection newest first with its cause, then changes live", async () => {
    await renderPanel();
    expect(targets()).toEqual(["host2.example:443", "host1.example:443"]);
    const row = screen.getByText("host1.example:443").closest("tr")!;
    expect(within(row).getByText("http.send op_1")).toBeTruthy();
    expect(within(row).getByText("93.184.215.14:443")).toBeTruthy();
    expect(within(row).getByText("5.0 KB")).toBeTruthy();
    expect(within(row).getByText("2.00 s")).toBeTruthy();
    const refusedRow = screen.getByText("host2.example:443").closest("tr")!;
    expect(refusedRow.getAttribute("data-state")).toBe("refused");
    expect(within(refusedRow).getByText("no user action")).toBeTruthy();
    expect(within(refusedRow).getByText("netx.not_user_initiated")).toBeTruthy();

    act(() => emit("network:activity", { active: 1, conns: [conn(3, { state: "open", connectedAt: "2026-09-19T10:31:00Z", bytesOut: 10 })] }));
    expect(targets()).toEqual(["host3.example:443", "host2.example:443", "host1.example:443"]);
    expect(screen.getByRole("button", { name: /Connections/ }).textContent).toContain("1");

    fireEvent.click(screen.getByRole("button", { name: "Active only" }));
    expect(targets()).toEqual(["host3.example:443"]);
    expect(screen.getByText("2 hidden")).toBeTruthy();
  });

  it("filters by text", async () => {
    await renderPanel();
    fireEvent.change(screen.getByRole("textbox", { name: "Filter the connections" }), { target: { value: "REFUSED" } });
    expect(targets()).toEqual(["host2.example:443"]);
    fireEvent.change(screen.getByRole("textbox", { name: "Filter the connections" }), { target: { value: "nothing like it" } });
    expect(screen.getByText("No connections match the filter.")).toBeTruthy();
  });

  it("clears the ended connections here and in Go; open ones stay", async () => {
    await renderPanel();
    act(() => emit("network:activity", { active: 1, conns: [conn(3, { state: "open" })] }));
    fireEvent.click(screen.getByRole("button", { name: "Clear the ended connections" }));
    await act(settle);
    expect(networkService.Clear).toHaveBeenCalledTimes(1);
    expect(targets()).toEqual(["host3.example:443"]);
    expect((screen.getByRole("button", { name: "Clear the ended connections" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("switches views, and closes on Escape", async () => {
    const { onClose, onViewChange } = await renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Log" }));
    expect(onViewChange).toHaveBeenCalledWith("log");
    fireEvent.keyDown(screen.getByRole("region", { name: "Connections" }), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("says when the app has made no connection", async () => {
    networkService.List.mockResolvedValue({ conns: [], active: 0, capacity: 500 });
    await renderPanel();
    expect(screen.getByText(/^No connections\. PostSonde connects only when you/)).toBeTruthy();
  });

  it("marks connections that went through a proxy, with its kind (NET-06)", async () => {
    await renderPanel();
    act(() =>
      emit("network:activity", {
        active: 1,
        conns: [conn(3, { state: "open", remoteAddr: "10.0.0.1:1080", via: "socks5h://proxy.lan:1080", connectedAt: "2026-09-19T10:31:00Z" })],
      }),
    );
    const row = screen.getByText("host3.example:443").closest("tr")!;
    const mark = within(row).getByText("via SOCKS5");
    expect(mark.getAttribute("title")).toBe("Through the proxy socks5h://proxy.lan:1080; the remote address is the proxy's");
    expect(row.getAttribute("title")).toContain("via=socks5h://proxy.lan:1080");
    expect(within(screen.getByText("host1.example:443").closest("tr")!).queryByText(/^via /)).toBeNull();
    fireEvent.change(screen.getByRole("textbox", { name: "Filter the connections" }), { target: { value: "proxy.lan" } });
    expect(targets()).toEqual(["host3.example:443"]);
  });

  it("matches the target, addresses, cause, state and error code", () => {
    expect(matchesConnection(closed, "93.184")).toBe(true);
    expect(matchesConnection(closed, "op_1")).toBe(true);
    expect(matchesConnection(refused, "not_user")).toBe(true);
    expect(matchesConnection(closed, "grpc")).toBe(false);
    expect(matchesConnection(closed, "  ")).toBe(true);
  });
});

describe("NetworkIndicator", () => {
  it("shows idle, then the active connections, and toggles the log", async () => {
    const feed = new ConnectionFeed();
    const onToggle = vi.fn();
    render(<NetworkIndicator open={false} onToggle={onToggle} feed={feed} />);
    await act(settle);
    const button = screen.getByRole("button", { name: /^Network/ });
    expect(button.textContent).toBe("Idle");
    expect(button.getAttribute("data-active")).toBe("false");

    act(() => emit("network:activity", { active: 2, conns: [conn(3, { state: "open" }), conn(4)] }));
    expect(button.textContent).toBe("2 connections");
    expect(button.getAttribute("data-active")).toBe("true");
    expect(button.getAttribute("aria-label")).toBe("Network: 2 connections open. Show the connection log");

    act(() => emit("network:activity", { active: 0, conns: [conn(3, { state: "closed" }), conn(4, { state: "failed" })] }));
    expect(button.textContent).toBe("Idle");

    // NET-06: open connections through a proxy are marked with its kind.
    act(() =>
      emit("network:activity", {
        active: 2,
        conns: [conn(5, { state: "open", via: "http://proxy.lan:3128" }), conn(6, { state: "open", via: "socks4a://legacy.lan:1080" })],
      }),
    );
    expect(button.textContent).toBe("2 connectionsvia HTTP, SOCKS4a proxy");
    expect(button.querySelector("[data-proxy]")?.getAttribute("title")).toBe(
      "Open connections go through http://proxy.lan:3128, socks4a://legacy.lan:1080",
    );
    act(() => emit("network:activity", { active: 0, conns: [conn(5, { state: "closed", via: "http://proxy.lan:3128" }), conn(6, { state: "closed" })] }));
    expect(button.querySelector("[data-proxy]")).toBeNull();

    fireEvent.click(button);
    expect(onToggle).toHaveBeenCalledTimes(1);
  });
});
