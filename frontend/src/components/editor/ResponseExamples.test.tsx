import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  StoredResponse: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const collection = vi.hoisted(() => ({
  UpdateRequest: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const servers = vi.hoisted(() => ({
  MockRouteFromExample: vi.fn<(input: unknown) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({ HTTPService: http }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({ CollectionService: collection }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({ ServersService: servers }));

import { resetResponseHistories, responseHistories } from "../../commands/responseHistory";
import type { RequestDTO } from "../../lib/collection";
import type { ResponseExampleDTO } from "../../lib/responseHistory";
import { createStores, StoreProvider, useDrafts, type Stores } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { httpResponse } from "../../test/httpResponse";
import { ResponseExamples } from "./ResponseExamples";

function example(name: string, over: Partial<ResponseExampleDTO> = {}): ResponseExampleDTO {
  return {
    name,
    savedAt: "2026-09-23T10:15:02Z",
    request: null,
    statusCode: 200,
    statusText: "OK",
    headers: [],
    body: "{}",
    bodyEncoding: "",
    bodyClipped: false,
    bodySize: 0,
    extra: "",
    ...over,
  };
}

/** The sub-tab bound to the draft, as the editor has it. */
function Bound({ readOnly }: { readOnly: boolean }) {
  const examples = useDrafts((s) => (s.byId.req_1?.request as unknown as RequestDTO).responseExamples) ?? [];
  return <ResponseExamples id="req_1" examples={examples} readOnly={readOnly} />;
}

function renderExamples(examples: ResponseExampleDTO[], readOnly = false): Stores {
  const stores = createStores();
  const request = { id: "req_1", kind: "http", name: "Get user", responseExamples: examples } as unknown as RequestDTO;
  stores.drafts.dispatch({ type: "drafts/open", id: "req_1", request: request as unknown as Record<string, unknown> });
  render(
    <StoreProvider stores={stores}>
      <Bound readOnly={readOnly} />
    </StoreProvider>,
  );
  return stores;
}

const names = (stores: Stores) =>
  ((stores.drafts.getState().byId.req_1?.request as unknown as RequestDTO).responseExamples ?? []).map((e) => e.name);

beforeEach(() => {
  resetFakeWails();
  resetResponseHistories();
  http.StoredResponse.mockReset().mockResolvedValue(httpResponse());
  collection.UpdateRequest.mockReset().mockResolvedValue({ version: 1 });
  servers.MockRouteFromExample.mockReset().mockImplementation(() => Object.assign(new Promise(() => {}), { cancel: vi.fn() }));
});

describe("ResponseExamples (HTTP-38)", () => {
  it("says how to save one when there are none", () => {
    renderExamples([]);
    expect(screen.getByText(/No examples yet/)).toBeTruthy();
  });

  it("lists, renames, views and deletes the examples", async () => {
    const stores = renderExamples([
      example("Found"),
      example("", { statusCode: 404, statusText: "Not Found", bodyClipped: true, bodySize: 90000, savedAt: "" }),
    ]);
    expectAccessible();
    const table = screen.getByRole("table", { name: "Response examples" });
    const rows = within(table).getAllByRole("row");
    expect(rows).toHaveLength(3);
    expect(rows[1]?.textContent).toContain("200 OK");
    expect(rows[2]?.textContent).toContain("404 Not Found");
    expect(rows[2]?.textContent).toContain("first 64 KB of");

    act(() => {
      fireEvent.change(screen.getByRole("textbox", { name: "Name of example 2" }), { target: { value: "Missing" } });
    });
    expect(names(stores)).toEqual(["Found", "Missing"]);

    act(() => {
      fireEvent.click(screen.getByRole("button", { name: "View Found" }));
    });
    await act(settle);
    expect(responseHistories().get("req_1").getState()).toMatchObject({ shown: { kind: "example", index: 0 }, status: "ok" });

    act(() => {
      fireEvent.click(screen.getByRole("button", { name: "Delete Found" }));
    });
    expect(names(stores)).toEqual(["Missing"]);
    expect(responseHistories().get("req_1").getState().shown).toEqual({ kind: "latest" });
  });

  it("is read-only in a read-only request", () => {
    renderExamples([example("Found")], true);
    expect((screen.getByRole("textbox", { name: "Name of example 1" }) as HTMLInputElement).readOnly).toBe(true);
    expect((screen.getByRole("button", { name: "Delete Found" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "View Found" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("makes a mock route of an example (MOCK-03)", async () => {
    const found = example("Found");
    renderExamples([example("Other"), found], true);
    // Mocking changes no request: it works in a read-only one too.
    const button = screen.getByRole("button", { name: "Create a mock route from Found" }) as HTMLButtonElement;
    expect(button.disabled).toBe(false);
    await act(async () => {
      fireEvent.click(button);
      await settle();
    });
    expect(screen.getByRole("dialog", { name: "Create mock route" })).toBeTruthy();
    expect(servers.MockRouteFromExample).toHaveBeenCalledWith(expect.objectContaining({ example: found }));
  });
});
