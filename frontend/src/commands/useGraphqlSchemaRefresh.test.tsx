import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { SchemaInfoDTO } from "../lib/graphqlSchema";
import { ON_SEND_MIN_GAP_MS, type SchemaRefresh } from "../lib/graphqlSchemaRefresh";
import type { ResponseDTO } from "../lib/http";
import { httpResponse } from "../test/httpResponse";
import type { HttpSendState } from "./httpSends";
import type { GraphqlSchema } from "./useGraphqlSchema";
import { useGraphqlSchemaRefresh, type SchemaRefreshInput } from "./useGraphqlSchemaRefresh";

const info = (over: Partial<SchemaInfoDTO> = {}): SchemaInfoDTO =>
  ({ source: "introspection", cached: true, key: "https://api.test/graphql", ...over }) as SchemaInfoDTO;

function schemaOf(autoFetch: () => void, over: Partial<SchemaInfoDTO> = {}): GraphqlSchema {
  return { info: info(over), fetching: false, stage: "", error: null, lastFetch: null, fetch: vi.fn(), autoFetch, cancel: vi.fn() };
}

const idle: HttpSendState = { status: "idle", stage: "", received: 0, total: 0, response: null, error: null, startedAt: null, run: null };
const answered = (body: string, over: Partial<HttpSendState> = {}): HttpSendState => ({
  ...idle,
  status: "ok",
  response: httpResponse({ body, bodyPretty: body }) as ResponseDTO,
  ...over,
});
const REJECTED = '{"errors":[{"message":"Cannot query field \\"email\\" on type \\"User\\"."}]}';

describe("useGraphqlSchemaRefresh", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  const off: SchemaRefresh = { onSend: false, intervalMinutes: 0 };

  function mount(input: SchemaRefreshInput) {
    return renderHook((props: SchemaRefreshInput) => useGraphqlSchemaRefresh(props), { initialProps: input });
  }

  it("never fetches while both triggers are off", () => {
    const autoFetch = vi.fn();
    const hook = mount({ schema: schemaOf(autoFetch), refresh: off, send: idle, schemaAccepts: true });
    hook.rerender({ schema: schemaOf(autoFetch), refresh: off, send: answered(REJECTED), schemaAccepts: true });
    vi.advanceTimersByTime(24 * 60 * 60_000);
    expect(autoFetch).not.toHaveBeenCalled();
  });

  it("fetches every intervalMinutes while mounted, not while the window is hidden", () => {
    const autoFetch = vi.fn();
    const hook = mount({ schema: schemaOf(autoFetch), refresh: { onSend: false, intervalMinutes: 5 }, send: idle, schemaAccepts: true });
    vi.advanceTimersByTime(5 * 60_000 - 1);
    expect(autoFetch).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);
    expect(autoFetch).toHaveBeenCalledTimes(1);
    const hidden = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    vi.advanceTimersByTime(5 * 60_000);
    expect(autoFetch).toHaveBeenCalledTimes(1);
    hidden.mockRestore();
    vi.advanceTimersByTime(5 * 60_000);
    expect(autoFetch).toHaveBeenCalledTimes(2);
    hook.unmount();
    vi.advanceTimersByTime(60 * 60_000);
    expect(autoFetch).toHaveBeenCalledTimes(2);
  });

  it("does not refresh a schema from a file", () => {
    const autoFetch = vi.fn();
    const schema = schemaOf(autoFetch, { source: "file" });
    const hook = mount({ schema, refresh: { onSend: true, intervalMinutes: 5 }, send: idle, schemaAccepts: true });
    hook.rerender({ schema, refresh: { onSend: true, intervalMinutes: 5 }, send: answered(REJECTED), schemaAccepts: true });
    vi.advanceTimersByTime(60 * 60_000);
    expect(autoFetch).not.toHaveBeenCalled();
  });

  it("fetches after a send the server rejected in validation, once per gap", () => {
    const autoFetch = vi.fn();
    const schema = schemaOf(autoFetch);
    const refresh: SchemaRefresh = { onSend: true, intervalMinutes: 0 };
    // A response from before the editor mounted is not a new send.
    const hook = mount({ schema, refresh, send: answered(REJECTED), schemaAccepts: true });
    expect(autoFetch).not.toHaveBeenCalled();

    // Still running: not yet.
    const running = answered(REJECTED, { status: "running" });
    hook.rerender({ schema, refresh, send: running, schemaAccepts: true });
    expect(autoFetch).not.toHaveBeenCalled();
    hook.rerender({ schema, refresh, send: { ...running, status: "ok" }, schemaAccepts: true });
    expect(autoFetch).toHaveBeenCalledTimes(1);

    // Within the gap: no second fetch; after it, again.
    hook.rerender({ schema, refresh, send: answered(REJECTED), schemaAccepts: true });
    expect(autoFetch).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(ON_SEND_MIN_GAP_MS);
    hook.rerender({ schema, refresh, send: answered(REJECTED), schemaAccepts: true });
    expect(autoFetch).toHaveBeenCalledTimes(2);
  });

  it("ignores answers that do not signal a changed schema", () => {
    const autoFetch = vi.fn();
    const schema = schemaOf(autoFetch);
    const refresh: SchemaRefresh = { onSend: true, intervalMinutes: 0 };
    const hook = mount({ schema, refresh, send: idle, schemaAccepts: true });
    // Data: the query ran.
    hook.rerender({ schema, refresh, send: answered('{"data":{"user":null}}'), schemaAccepts: true });
    // The cached schema rejects the query too: a typo, not a change.
    hook.rerender({ schema, refresh, send: answered(REJECTED), schemaAccepts: false });
    // A repeat run's response.
    hook.rerender({
      schema,
      refresh,
      send: answered(REJECTED, { run: { count: 3, intervalMs: 0 } as unknown as HttpSendState["run"] }),
      schemaAccepts: true,
    });
    // A failed send.
    hook.rerender({ schema, refresh, send: answered(REJECTED, { status: "error" }), schemaAccepts: true });
    expect(autoFetch).not.toHaveBeenCalled();
  });
});
