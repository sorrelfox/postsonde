import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { beforeEach, describe, expect, it, vi } from "vitest";

const collection = vi.hoisted(() => ({
  UpdateRequest: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: collection,
}));

import { resetAssertionJumps, takeAssertionJump } from "../../commands/assertionJump";
import { addQuickAssertion, type QuickAssertion } from "../../commands/quickAssert";
import { toaster } from "../../commands/toasts";
import { newAssertion, type AssertionDTO } from "../../lib/assertions";
import { createStores, StoreProvider } from "../../stores";
import { httpResponse } from "../../test/httpResponse";
import { ResponseView } from "./ResponseView";

const BODY = '{"id":42,"user":{"name":"Ada","tags":["a","b"]}}';
const PRETTY = JSON.stringify(JSON.parse(BODY), null, 2);

beforeEach(() => {
  resetAssertionJumps();
  toaster.clear();
  collection.UpdateRequest.mockReset();
  collection.UpdateRequest.mockResolvedValue({ version: 2 });
});

function view(onQuickAssert?: (a: QuickAssertion) => void) {
  render(
    <ResponseView
      response={httpResponse({
        statusCode: 201,
        status: "201 Created",
        body: BODY,
        bodyPretty: PRETTY,
        bodyLanguage: "json",
        headers: [{ name: "Content-Type", value: "application/json" }],
      })}
      tests={{ onJump: () => {}, onQuickAssert }}
    />,
  );
}

const menu = () => screen.getByRole("menu", { name: "Add an assertion" });
const menuItems = () => within(menu()).getAllByRole("menuitem").map((m) => m.textContent);

describe("quick assertions (TEST-04)", () => {
  it("asserts the JSON value at the cursor, with its JSONPath", () => {
    const onQuickAssert = vi.fn<(a: QuickAssertion) => void>();
    view(onQuickAssert);
    const editor = EditorView.findFromDOM(screen.getByRole("textbox", { name: "Response body" })) as EditorView;
    const at = editor.state.doc.toString().indexOf('"Ada"') + 1;
    act(() => editor.dispatch({ selection: { anchor: at } }));
    // From the keyboard (the context-menu key): at the cursor.
    fireEvent.contextMenu(editor.contentDOM);
    expect(menuItems()).toEqual(["Assert $.user.name equals this value", "Assert $.user.name exists", "Assert $.user.name is string"]);
    fireEvent.click(within(menu()).getByRole("menuitem", { name: "Assert $.user.name equals this value" }));
    expect(onQuickAssert).toHaveBeenCalledWith({ target: "jsonpath", expr: "$.user.name", op: "equals", expected: "Ada" });

    const arr = editor.state.doc.toString().indexOf('"b"');
    act(() => editor.dispatch({ selection: { anchor: arr } }));
    fireEvent.contextMenu(editor.contentDOM);
    fireEvent.click(within(menu()).getByRole("menuitem", { name: "Assert $.user.tags[1] is string" }));
    expect(onQuickAssert).toHaveBeenLastCalledWith({ target: "jsonpath", expr: "$.user.tags[1]", op: "is_type", expected: "string" });
  });

  it("asserts a header or the status code", () => {
    const onQuickAssert = vi.fn<(a: QuickAssertion) => void>();
    view(onQuickAssert);
    fireEvent.contextMenu(screen.getByText("201 Created"));
    expect(menuItems()).toEqual(["Assert status code equals 201"]);
    fireEvent.click(within(menu()).getByRole("menuitem"));
    expect(onQuickAssert).toHaveBeenCalledWith({ target: "status", expr: "", op: "equals", expected: "201" });

    fireEvent.click(screen.getByRole("tab", { name: /^Headers/ }));
    fireEvent.contextMenu(screen.getByRole("row", { name: /Content-Type/ }));
    expect(menuItems()).toEqual(["Assert header Content-Type equals this value", "Assert header Content-Type exists"]);
    fireEvent.click(within(menu()).getByRole("menuitem", { name: "Assert header Content-Type equals this value" }));
    expect(onQuickAssert).toHaveBeenLastCalledWith({ target: "header", expr: "Content-Type", op: "equals", expected: "application/json" });
  });

  it("leaves the browser's menu alone without a request to add to", () => {
    view(undefined);
    fireEvent.contextMenu(screen.getByText("201 Created"));
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("adds the assertion to the request, opens its Tests sub-tab at the new row and says so", () => {
    const stores = createStores();
    const first: AssertionDTO = newAssertion();
    stores.drafts.dispatch({ type: "drafts/open", id: "req_1", request: { id: "req_1", kind: "http", assertions: [first] } });
    render(<StoreProvider stores={stores}>{null}</StoreProvider>);
    const index = addQuickAssertion(stores, "req_1", { target: "jsonpath", expr: "$.id", op: "equals", expected: "42" });
    expect(index).toBe(1);
    const list = stores.drafts.getState().byId.req_1?.request.assertions as AssertionDTO[];
    expect(list).toEqual([first, newAssertion({ target: "jsonpath", expr: "$.id", op: "equals", expected: "42" })]);
    expect(stores.drafts.getState().subTabs.req_1).toBe("tests");
    expect(takeAssertionJump("req_1")).toBe(1);
    expect(toaster.toasts().map((t) => t.message)).toEqual(["Assertion added: JSONPath $.id equals 42"]);
    // A request that is not open takes nothing.
    expect(addQuickAssertion(stores, "req_none", { target: "status", expr: "", op: "equals", expected: "200" })).toBeNull();
  });
});
