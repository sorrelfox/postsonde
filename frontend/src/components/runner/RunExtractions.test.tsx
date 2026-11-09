// The folder runner's view of extraction rules (TEST-05): a result failed
// because a value the next requests need was not set says which, in the
// results and in the summary; the result's dialog has the Variables tab.
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const runner = vi.hoisted(() => ({
  Start: vi.fn<(input: unknown) => Promise<unknown>>(),
  Plan: vi.fn<(id: string) => Promise<unknown>>(),
  Response: vi.fn<(opId: string, index: number) => Promise<unknown>>(),
  Release: vi.fn<(opId: string) => Promise<void>>(() => Promise.resolve()),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/runner", () => ({
  RunnerService: runner,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { resetFolderRuns } from "../../commands/folderRuns";
import type { ExtractionResultDTO } from "../../lib/extractions";
import { createStores, StoreProvider } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { httpResponse } from "../../test/httpResponse";
import { runResult } from "../../test/runResult";
import { FolderRunPanel } from "./FolderRunPanel";

const missing: ExtractionResultDTO = {
  index: 0,
  source: "jsonpath",
  expr: "$.token",
  variable: "token",
  scope: "runtime",
  secret: true,
  persist: false,
  outcome: "failed",
  value: "",
  valueClipped: false,
  message: "JSONPath $.token selects nothing in the body.",
  code: "extract.not_found",
};

beforeEach(() => {
  resetFakeWails();
  resetFolderRuns();
  runner.Start.mockReset().mockResolvedValue({ opId: "op_1" });
  const item = (n: number, name: string) => ({ position: n, requestId: `req_${n}`, name, kind: "http", method: "GET", path: [], skip: "" });
  runner.Plan.mockReset().mockResolvedValue({ items: [item(1, "Log in"), item(2, "Me")], runnable: 2 });
  runner.Response.mockReset();
});

describe("FolderRunPanel extraction failures (TEST-05)", () => {
  it("says which variable was not set, in the results and the summary, and shows the Variables tab", async () => {
    render(
      <StoreProvider stores={createStores()}>
        <FolderRunPanel folderId="fld_auth" />
      </StoreProvider>,
    );
    await act(settle);
    fireEvent.click(screen.getByRole("button", { name: "Run folder" }));
    await act(settle);
    await act(settle);
    act(() => {
      emit("runner:op:data", {
        opId: "op_1",
        seq: 1,
        items: [runResult(1, { name: "Log in", outcome: "failed", hasResponse: true, extractions: [missing] }), runResult(2, { name: "Me" })],
      });
      emit("runner:op:done", { opId: "op_1", seq: 2, status: "ok", error: null, items: 2, batches: 1, elapsedMs: 20 });
    });

    const text = "Variable token was not set: JSONPath $.token selects nothing in the body.";
    // Under the result's row, and in the summary's failures.
    expect(screen.getAllByText(text)).toHaveLength(2);
    const summary = screen.getByRole("region", { name: "Summary" });
    expect(within(summary).getAllByRole("listitem").map((li) => li.textContent)).toEqual([`FailedLog in${text}`]);
    expectAccessible(document.body);

    runner.Response.mockResolvedValue(httpResponse({ extractions: [missing] }));
    fireEvent.click(within(summary).getByRole("button", { name: "Log in" }));
    await act(settle);
    const dialog = screen.getByRole("dialog", { name: "Result of Log in" });
    fireEvent.click(within(dialog).getByRole("tab", { name: /^Variables/ }));
    expect(within(dialog).getByText("JSONPath $.token selects nothing in the body.")).toBeTruthy();
    expectAccessible(dialog);
  });
});
