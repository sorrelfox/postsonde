import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  CompareResponses: vi.fn<(input: unknown) => unknown>(),
}));
const history = vi.hoisted(() => ({
  Get: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({ HTTPService: http }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history", () => ({ HistoryService: history }));

import type { RequestDTO } from "../../lib/collection";
import type { ResponseDiffDTO } from "../../lib/responseDiff";
import type { ResponseExampleDTO } from "../../lib/responseHistory";
import { createStores, StoreProvider } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { CompareResponsesDialog } from "./CompareResponsesDialog";

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

const example: ResponseExampleDTO = {
  name: "Found",
  savedAt: "",
  request: null,
  statusCode: 200,
  statusText: "OK",
  headers: [{ name: "Content-Type", value: "application/json", description: "", enabled: true, extra: "" }],
  body: `{"id":1}`,
  bodyEncoding: "",
  bodyClipped: false,
  bodySize: 0,
  extra: "",
};

function entry(id: string, body: string) {
  return {
    id,
    request: { method: "GET", url: "https://api.test/u", proto: "HTTP/1.1", headers: [], body: { text: "", base64: false, size: 0, clipped: false } },
    response: {
      proto: "HTTP/1.1", statusCode: 200, status: "200 OK", finalUrl: "", redirects: 0,
      headers: [{ name: "Content-Type", value: "application/json" }], contentType: "application/json",
      body: { text: body, base64: false, size: body.length, clipped: false }, truncated: false,
    },
    durationMs: 5,
  };
}

const same = (n: number, text: string) => ({ kind: "same", left: { n, text }, right: { n, text }, path: "$" });

function diff(): ResponseDiffDTO {
  const rows = [
    same(1, "{"),
    { kind: "changed", left: { n: 2, text: `  "id": 1,` }, right: { n: 2, text: `  "id": 2,` }, path: "$.id" },
    ...Array.from({ length: 12 }, (_, i) => same(i + 3, `  "k${i}": ${i},`)),
    { kind: "removed", left: { n: 15, text: `  "old": true` }, right: null, path: "$.old" },
    { kind: "added", left: null, right: { n: 15, text: `  "new": null` }, path: "$.new" },
    same(16, "}"),
  ];
  return {
    status: { left: "200 OK", right: "201 Created", changed: true },
    headers: [
      { name: "Content-Type", kind: "same", left: ["application/json"], right: ["application/json"] },
      { name: "ETag", kind: "changed", left: [`"a"`], right: [`"b"`] },
      { name: "X-Old", kind: "removed", left: ["1"], right: [] },
      { name: "Location", kind: "added", left: [], right: ["/u/2"] },
    ],
    headersAdded: 1,
    headersRemoved: 1,
    headersChanged: 1,
    body: {
      mode: "json",
      identical: false,
      rows,
      changes: [
        { path: "$.id", kind: "changed", left: "1", right: "2", leftType: "", rightType: "" },
        { path: "$.old", kind: "removed", left: "true", right: "", leftType: "boolean", rightType: "" },
        { path: "$.new", kind: "added", left: "", right: "null", leftType: "", rightType: "null" },
      ],
      added: 1,
      removed: 1,
      changed: 1,
      notices: [{ code: "respdiff.key_order", message: "Members are in another order on the right." }],
      leftSize: 100,
      rightSize: 100,
    },
  };
}

function renderDialog(initialLeft = "h:h_1", initialRight = "h:h_2") {
  const stores = createStores();
  const request = { id: "req_1", kind: "http", name: "Get user", responseExamples: [example] } as unknown as RequestDTO;
  stores.drafts.dispatch({ type: "drafts/open", id: "req_1", request: request as unknown as Record<string, unknown> });
  const onClose = vi.fn();
  render(
    <StoreProvider stores={stores}>
      <CompareResponsesDialog
        id="req_1"
        open
        options={[
          { key: "h:h_2", label: "Latest response" },
          { key: "h:h_1", label: "Earlier" },
          { key: "e:0", label: "Found" },
        ]}
        initialLeft={initialLeft}
        initialRight={initialRight}
        onClose={onClose}
      />
    </StoreProvider>,
  );
  return { onClose };
}

const lastCall = () => {
  const calls = http.CompareResponses.mock.calls;
  return calls[calls.length - 1][0] as { left: { body: string; statusCode: number }; right: { body: string } };
};

beforeEach(() => {
  resetFakeWails();
  http.CompareResponses.mockReset().mockImplementation(() => cancellable(diff()));
  history.Get.mockReset().mockImplementation((id) => Promise.resolve(entry(id, `{"from":"${id}"}`)));
});

describe("CompareResponsesDialog (HTTP-40)", () => {
  it("compares the chosen responses side by side", async () => {
    renderDialog();
    await act(settle);
    const dialog = screen.getByRole("dialog", { name: "Compare responses" });
    expect(history.Get).toHaveBeenCalledWith("h_1");
    expect(history.Get).toHaveBeenCalledWith("h_2");
    expect(lastCall().left.body).toBe(`{"from":"h_1"}`);
    expect(lastCall().right.body).toBe(`{"from":"h_2"}`);

    expect(within(dialog).getByText(/Status: 200 OK on the left, 201 Created on the right/)).toBeTruthy();

    // Headers: only the differences, each with its kind for screen readers.
    const headers = within(dialog).getByRole("table", { name: "Headers" });
    const hrows = within(headers).getAllByRole("row").slice(1);
    expect(hrows.map((r) => r.getAttribute("data-kind"))).toEqual(["changed", "removed", "added"]);
    expect(within(hrows[0]).getByRole("rowheader").textContent).toBe("~Changed: ETag");
    expect(within(hrows[2]).getByRole("rowheader").textContent).toBe("+Added: Location");
    expect(within(dialog).getByText("1 added, 1 removed, 1 changed")).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("checkbox", { name: "Show unchanged" }));
    expect(within(headers).getAllByRole("row")).toHaveLength(5);

    // Body: the summary, the notices, the changes by path and the rows.
    expect(within(dialog).getByText("1 added, 1 removed, 1 changed, by JSON path")).toBeTruthy();
    expect(within(dialog).getByRole("list", { name: "About this comparison" }).textContent).toContain("another order");
    expect(within(dialog).getByText("3 changes by path")).toBeTruthy();
    const changes = within(dialog).getByRole("list", { name: "Changes by path" });
    expect(changes.textContent).toContain("$.id 1 → 2");
    expect(changes.textContent).toContain("Removed: $.old removed: true");

    const body = within(dialog).getByRole("table", { name: "Bodies side by side" });
    const changed = body.querySelector('tr[data-kind="changed"]') as HTMLElement;
    expect(changed.textContent).toContain(`Changed:   "id": 1,`);
    expect(changed.textContent).toContain(`Changed:   "id": 2,`);
    const removed = body.querySelector('tr[data-kind="removed"]') as HTMLElement;
    expect(within(removed).getAllByText("−")).toHaveLength(1);
    expect(removed.textContent).toContain("Removed:");
    // 12 unchanged lines between the changes: 3 kept on each side, 6 folded.
    const unfold = within(body).getByRole("button", { name: "Show 6 unchanged lines" });
    expect(within(body).queryByText(`"k5": 5,`, { exact: false })).toBeNull();
    fireEvent.click(unfold);
    expect(within(body).getAllByText(`"k5": 5,`, { exact: false })).toHaveLength(2);
    expectAccessible(dialog);
  });

  it("asks again when a side changes, and swaps the sides", async () => {
    renderDialog();
    await act(settle);
    const dialog = screen.getByRole("dialog", { name: "Compare responses" });
    act(() => {
      fireEvent.change(within(dialog).getByLabelText("Left"), { target: { value: "e:0" } });
    });
    await act(settle);
    // An example is compared as the request keeps it.
    expect(lastCall().left.body).toBe(`{"id":1}`);
    expect(lastCall().left.statusCode).toBe(200);
    act(() => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Swap" }));
    });
    await act(settle);
    expect(lastCall().right.body).toBe(`{"id":1}`);
    expect((within(dialog).getByLabelText("Right") as HTMLSelectElement).value).toBe("e:0");
    act(() => {
      fireEvent.change(within(dialog).getByLabelText("Left"), { target: { value: "e:0" } });
    });
    await act(settle);
    expect(within(dialog).getByText("Both sides show the same response.")).toBeTruthy();
  });

  it("says when the bodies are the same, and why a comparison failed", async () => {
    const identical = diff();
    identical.status = { left: "200 OK", right: "200 OK", changed: false };
    identical.headers = [identical.headers?.[0] as never];
    identical.headersAdded = identical.headersRemoved = identical.headersChanged = 0;
    identical.body = { ...identical.body, identical: true, rows: [same(1, "{}")], changes: [], added: 0, removed: 0, changed: 0, notices: [] };
    http.CompareResponses.mockImplementation(() => cancellable(identical));
    renderDialog();
    await act(settle);
    const dialog = screen.getByRole("dialog", { name: "Compare responses" });
    expect(within(dialog).getByText("Status: 200 OK on both sides")).toBeTruthy();
    expect(within(dialog).getByText("The same header fields on both sides.")).toBeTruthy();
    expect(within(dialog).getByText("The bodies hold the same JSON.")).toBeTruthy();
    expect(within(dialog).queryByRole("table", { name: "Headers" })).toBeNull();

    history.Get.mockImplementation(() => Promise.reject(new Error("gone")));
    act(() => {
      fireEvent.change(within(dialog).getByLabelText("Right"), { target: { value: "h:h_1" } });
    });
    await act(settle);
    expect(within(dialog).getByRole("alert").textContent).toContain("gone");
  });
});
