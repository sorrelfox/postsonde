import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

type GrpcInput = { call: { address: string; metadata: unknown[] }; language: string; revealSecrets: boolean };
type WsInput = { connect: { url: string }; language: string; mode: string; revealSecrets: boolean };

const grpc = vi.hoisted(() => ({
  Snippet: vi.fn<(input: GrpcInput) => Promise<unknown>>(),
  SnippetLanguages: vi.fn<() => Promise<unknown>>(),
}));
const ws = vi.hoisted(() => ({
  Snippet: vi.fn<(input: WsInput) => Promise<unknown>>(),
  SnippetLanguages: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc", () => ({ GRPCService: grpc }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/websocket", () => ({ WebSocketService: ws }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({ CollectionService: {} }));

import { grpcSnippetSource, resetSnippetLanguages, wsSnippetSource } from "../../commands/snippets";
import type { InvokeInputDTO } from "../../lib/grpc";
import type { ConnectInputDTO } from "../../lib/websocket";
import { createStores, StoreProvider } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { SnippetPanel } from "./SnippetPanel";

const MASK = "••••••";
const cancellable = <T,>(p: Promise<T>) => Object.assign(p, { cancel: () => undefined });

const call = { address: "localhost:50051", service: "a.B", method: "C", metadata: [] } as unknown as InvokeInputDTO;
const connect = { url: "wss://echo.test/{{room}}", headers: [], subprotocols: [] } as unknown as ConnectInputDTO;

beforeEach(() => {
  resetFakeWails();
  resetSnippetLanguages();
  grpc.Snippet.mockReset().mockImplementation((input) =>
    cancellable(
      Promise.resolve({
        language: input.language,
        mode: "resolved",
        code: `grpcurl -plaintext \\\n  -H 'authorization: Bearer ${input.revealSecrets ? "tok" : MASK}' \\\n  ${input.call.address} a.B/C`,
        masked: !input.revealSecrets,
        revealed: input.revealSecrets,
        unresolved: [],
      }),
    ),
  );
  grpc.SnippetLanguages.mockReset().mockImplementation(() => cancellable(Promise.resolve([{ id: "grpcurl", name: "grpcurl", syntax: "shell" }])));
  ws.Snippet.mockReset().mockImplementation((input) =>
    cancellable(
      Promise.resolve({
        language: input.language,
        mode: input.mode,
        code: `${input.language} ${input.mode === "templated" ? input.connect.url : "wss://echo.test/lobby"}`,
        masked: false,
        revealed: false,
        unresolved: [],
      }),
    ),
  );
  ws.SnippetLanguages.mockReset().mockImplementation(() =>
    cancellable(
      Promise.resolve([
        { id: "websocat", name: "websocat", syntax: "shell" },
        { id: "wscat", name: "wscat", syntax: "shell" },
      ]),
    ),
  );
});

async function show(node: React.ReactNode) {
  render(<StoreProvider stores={createStores()}>{node}</StoreProvider>);
  await act(settle);
}

describe("SnippetPanel with a source", () => {
  it("writes a gRPC call as grpcurl, without the Resolved / Templated choice, and reveals on request", async () => {
    await show(<SnippetPanel id="req_1" request="k1" source={grpcSnippetSource(call)} />);
    expect(grpc.Snippet).toHaveBeenCalledWith({ call, language: "grpcurl", revealSecrets: false });
    expect(screen.getByLabelText("grpcurl code").textContent).toContain(`Bearer ${MASK}`);
    expect(screen.queryByRole("group", { name: "Values" })).toBeNull();
    expect(screen.getByRole("status").textContent).toContain("Secrets are masked.");
    fireEvent.click(screen.getByRole("button", { name: "Show secrets" }));
    await act(settle);
    expect(grpc.Snippet).toHaveBeenLastCalledWith({ call, language: "grpcurl", revealSecrets: true });
    expect(screen.getByLabelText("grpcurl code").textContent).toContain("Bearer tok");
    expectAccessible();
  });

  it("writes a WebSocket handshake as websocat or wscat, resolved or templated", async () => {
    await show(<SnippetPanel id="req_1" request="k1" source={wsSnippetSource(connect)} />);
    expect(ws.Snippet).toHaveBeenCalledWith({ connect, language: "websocat", mode: "resolved", revealSecrets: false });
    expect(screen.getByLabelText("websocat code").textContent).toBe("websocat wss://echo.test/lobby");
    expect(screen.getByRole("status").textContent).toBe("Variables are filled in as a send would fill them.");

    const select = screen.getByRole("combobox", { name: "Language" }) as HTMLSelectElement;
    expect([...select.options].map((o) => o.textContent)).toEqual(["websocat", "wscat"]);
    fireEvent.change(select, { target: { value: "wscat" } });
    await act(settle);
    fireEvent.click(within(screen.getByRole("group", { name: "Values" })).getByRole("button", { name: "Templated" }));
    await act(settle);
    expect(ws.Snippet).toHaveBeenLastCalledWith({ connect, language: "wscat", mode: "templated", revealSecrets: false });
    expect(screen.getByLabelText("wscat code").textContent).toBe("wscat wss://echo.test/{{room}}");
    expectAccessible();
  });

  it("asks again with the new input when the editor's input changes", async () => {
    const stores = createStores();
    const view = render(
      <StoreProvider stores={stores}>
        <SnippetPanel id="req_1" request="k1" source={grpcSnippetSource(call)} />
      </StoreProvider>,
    );
    await act(settle);
    const next = { ...call, address: "localhost:6000" } as InvokeInputDTO;
    vi.useFakeTimers();
    try {
      view.rerender(
        <StoreProvider stores={stores}>
          <SnippetPanel id="req_1" request="k2" source={grpcSnippetSource(next)} />
        </StoreProvider>,
      );
      await act(async () => {
        await vi.advanceTimersByTimeAsync(250);
      });
    } finally {
      vi.useRealTimers();
    }
    await act(settle);
    expect(grpc.Snippet).toHaveBeenLastCalledWith({ call: next, language: "grpcurl", revealSecrets: false });
    expect(screen.getByLabelText("grpcurl code").textContent).toContain("localhost:6000");
  });
});
