import { act, fireEvent, render, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { undo } from "@codemirror/commands";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));

/** Go's answers, set per test. Calls return a promise with cancel(), like Wails. */
const format = vi.hoisted(() => ({
  ValidateJSON: vi.fn<(text: string) => unknown>(),
  BeautifyJSON: vi.fn<(text: string, indent: number) => unknown>(),
  MinifyJSON: vi.fn<(text: string) => unknown>(),
  SOAPEnvelope: vi.fn<(input: { version: string; action: string; text: string }) => unknown>(),
}));

/** WorkspaceService's body file calls. */
const workspace = vi.hoisted(() => ({
  PickBodyFile: vi.fn<() => unknown>(),
  BodyFile: vi.fn<(path: string) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: workspace,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));

import { installShortcuts } from "../../commands/shortcuts";
import { forgetBodyFiles } from "../../commands/useBodyFile";
import type { BodyFileDTO } from "../../lib/workspace";
import { IS_MAC } from "../../lib/shortcuts";
import { JSON_CHECK_DELAY_MS } from "../../commands/useJsonCheck";
import type { BodyDTO, KeyValueDTO, PartDTO, RequestDTO } from "../../lib/collection";
import type { JSONErrorDTO } from "../../lib/format";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { TabPanel } from "../tabs/TabPanel";

// jsdom has no layout: CodeMirror measures ranges when it draws the cursor
// after a selection change (the error position link).
for (const name of ["getClientRects", "getBoundingClientRect"] as const) {
  if (typeof Range.prototype[name] !== "function") {
    Object.defineProperty(Range.prototype, name, {
      configurable: true,
      value: name === "getClientRects" ? () => [] : () => ({ left: 0, right: 0, top: 0, bottom: 0, width: 0, height: 0 }),
    });
  }
}

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

const body = (over: Partial<BodyDTO> = {}): BodyDTO => ({
  mode: "raw",
  type: "json",
  text: '{"a":1}',
  fields: null,
  parts: null,
  path: "",
  contentType: "",
  extra: '{"x-body":true}',
  ...over,
});

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "req",
    kind: "http",
    name: "Create",
    description: "",
    method: "POST",
    url: "/items",
    params: null,
    pathParams: null,
    headers: null,
    body: body(),
    query: "",
    variables: "",
    operationName: "",
    connectionParams: "",
    schemaFile: "",
    address: "",
    sendMode: "",
    lineEnding: "",
    message: "",
    subprotocols: [],
    messageFormat: "",
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: { type: "inherit", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: '{"x-custom":1}',
    ...over,
  };
}

const node = (over: Partial<TreeNode> = {}): TreeNode =>
  ({ id: "req", kind: "request", parentId: null, name: "Create", path: "req.json", fileKind: "http", readOnly: "", method: "POST", ...over }) as TreeNode;

async function setup(dto: RequestDTO = request(), opts: { readOnly?: string } = {}): Promise<Stores> {
  service.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: [node({ readOnly: opts.readOnly ?? "" })], rootIds: ["req"] });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["req"], activeId: "req" });
  stores.drafts.dispatch({ type: "drafts/setSubTab", id: "req", subTab: "body" });
  render(
    <StoreProvider stores={stores}>
      <TabPanel />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

const section = () => document.querySelector("[data-body-mode]") as HTMLElement;
const radio = (name: string) => within(section()).getByRole("radio", { name }) as HTMLInputElement;
const language = () => within(section()).queryByRole("combobox", { name: "Raw body language" }) as HTMLSelectElement | null;
const button = (name: string) => within(section()).getByRole("button", { name }) as HTMLButtonElement;
const view = (): EditorView => {
  const found = EditorView.findFromDOM(section().querySelector(".cm-editor") as HTMLElement);
  if (!found) throw new Error("no editor view");
  return found;
};
/** ⇧⌘F on macOS, Ctrl+Shift+F elsewhere. */
const beautifyKey = () =>
  new KeyboardEvent("keydown", { key: "F", code: "KeyF", shiftKey: true, [IS_MAC ? "metaKey" : "ctrlKey"]: true, bubbles: true, cancelable: true });
/** The body of the last request sent to UpdateRequest. */
const saved = (): BodyDTO => {
  const calls = service.UpdateRequest.mock.calls;
  return (calls[calls.length - 1]?.[0].request as RequestDTO).body;
};
const waitForCheck = () => act(() => new Promise<void>((r) => setTimeout(r, JSON_CHECK_DELAY_MS + 30)));

const jsonError = (over: Partial<JSONErrorDTO> = {}): JSONErrorDTO => ({
  offset: 5,
  byteOffset: 5,
  line: 1,
  column: 6,
  message: "Invalid character '}' looking for beginning of value",
  ...over,
});

beforeEach(() => {
  resetFakeWails();
  service.Request.mockReset();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
  format.ValidateJSON.mockReset().mockImplementation(() => cancellable({ valid: true, error: null }));
  format.BeautifyJSON.mockReset();
  format.MinifyJSON.mockReset();
  forgetBodyFiles();
  workspace.PickBodyFile.mockReset();
  workspace.BodyFile.mockReset().mockImplementation((path: string) => cancellable(fileDTO(path, path.startsWith("gone") ? null : 2048)));
});

/** Go's description of the file stored as `path`; size null for a missing file. */
function fileDTO(path: string, size: number | null): BodyFileDTO {
  return { path, absolutePath: `/p/shop/${path}`, name: path.split("/").pop() ?? path, size: size ?? 0, exists: size !== null };
}

describe("BodyEditor", () => {
  it("shows a raw JSON body without saving anything", async () => {
    await setup();
    expect(radio("Raw").checked).toBe(true);
    expect(language()?.value).toBe("json");
    expect(section().querySelector(".cm-content")?.textContent).toBe('{"a":1}');
    expect(button("Beautify").disabled).toBe(false);
    expect(button("Beautify").getAttribute("aria-keyshortcuts")).toBe(IS_MAC ? "Meta+Shift+F" : "Control+Shift+F");
    await waitForCheck();
    expect(format.ValidateJSON).toHaveBeenCalledWith('{"a":1}');
    expect(section().querySelector(".cm-errorMarker")).toBeNull();
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("saves none as only the mode and restores the raw body when switching back", async () => {
    const stores = await setup();
    fireEvent.click(radio("None"));
    await act(settle);
    expect(saved()).toEqual({ mode: "none", type: "", text: "", fields: null, parts: null, path: "", contentType: "", extra: '{"x-body":true}' });
    const sent = service.UpdateRequest.mock.calls[service.UpdateRequest.mock.calls.length - 1]?.[0].request as RequestDTO;
    expect(sent.extra).toBe('{"x-custom":1}');
    expect(section().textContent).toContain("This request has no body.");
    expect(language()).toBeNull();

    fireEvent.click(radio("Raw"));
    await act(settle);
    expect(saved()).toEqual(body());
    expect(section().querySelector(".cm-content")?.textContent).toBe('{"a":1}');
    expect(stores.drafts.getState().bodyStash.req).toMatchObject({ raw: body(), none: { mode: "none" } });
  });

  it("starts an empty JSON body when a none body becomes raw", async () => {
    await setup(request({ body: body({ mode: "none", type: "", text: "", extra: "" }) }));
    expect(radio("None").checked).toBe(true);
    fireEvent.click(radio("Raw"));
    await act(settle);
    expect(saved()).toEqual(body({ text: "", extra: "" }));
  });

  it("changes the raw language and keeps the text; JSON tools only for JSON", async () => {
    await setup();
    fireEvent.change(language()!, { target: { value: "xml" } });
    await act(settle);
    expect(saved()).toEqual(body({ type: "xml" }));
    expect(within(section()).queryByRole("button", { name: "Beautify" })).toBeNull();
    expect([...language()!.options].map((o) => o.textContent)).toEqual(["JSON", "XML", "HTML", "Text", "YAML"]);
    for (const type of ["html", "text", "yaml"]) {
      fireEvent.change(language()!, { target: { value: type } });
      await act(settle);
      expect(saved()).toEqual(body({ type }));
    }
  });

  it("saves typed text as it is", async () => {
    await setup();
    act(() => view().dispatch({ changes: { from: 7, insert: " {{x}}" }, userEvent: "input.type" }));
    await act(settle);
    expect(saved()).toEqual(body({ text: '{"a":1} {{x}}' }));
  });

  it("marks invalid JSON at Go's position, with the message", async () => {
    format.ValidateJSON.mockImplementation(() => cancellable({ valid: false, error: jsonError() }));
    await setup(request({ body: body({ text: '{"a":}' }) }));
    await waitForCheck();
    const mark = section().querySelector(".cm-errorMarker");
    expect(mark?.textContent).toBe("}");
    expect(section().querySelector(".cm-errorMarkerLine")).not.toBeNull();
    const status = within(section()).getByRole("status");
    expect(status.textContent).toContain("Line 1, column 6");
    expect(status.textContent).toContain("looking for beginning of value");

    // The position link moves the cursor to the error.
    fireEvent.click(within(status).getByRole("button", { name: "Line 1, column 6" }));
    expect(view().state.selection.main.head).toBe(5);

    // Fixed: the next check clears it.
    format.ValidateJSON.mockImplementation(() => cancellable({ valid: true, error: null }));
    act(() => view().dispatch({ changes: { from: 5, insert: "2" }, userEvent: "input.type" }));
    await waitForCheck();
    expect(section().querySelector(".cm-errorMarker")).toBeNull();
    expect(within(section()).queryByRole("status")).toBeNull();
  });

  it("marks the last character when the JSON ends too early, after non-ASCII text", async () => {
    // "é😀" is 3 UTF-16 units: Go's offset is in UTF-16 units, like CodeMirror.
    const text = '{"é😀": [1';
    format.ValidateJSON.mockImplementation(() =>
      cancellable({ valid: false, error: jsonError({ offset: text.length, byteOffset: 13, column: 11, message: "Unexpected end of JSON input" }) }),
    );
    await setup(request({ body: body({ text }) }));
    await waitForCheck();
    expect(section().querySelector(".cm-errorMarker")?.textContent).toBe("1");
  });

  it("does not check other raw languages", async () => {
    await setup(request({ body: body({ type: "yaml", text: "a: [" }) }));
    await waitForCheck();
    expect(format.ValidateJSON).not.toHaveBeenCalled();
  });

  it("does not check blank JSON", async () => {
    await setup(request({ body: body({ text: "  " }) }));
    await waitForCheck();
    expect(format.ValidateJSON).not.toHaveBeenCalled();
    expect(button("Beautify").disabled).toBe(true);
  });

  it("beautifies and minifies through Go, undoably", async () => {
    await setup(request({ body: body({ text: '{ "a": 1 }' }) }));
    format.BeautifyJSON.mockImplementation(() => cancellable({ valid: true, text: '{\n  "a": 1\n}', error: null }));
    fireEvent.click(button("Beautify"));
    await act(settle);
    expect(format.BeautifyJSON).toHaveBeenCalledWith('{ "a": 1 }', 2);
    expect(saved()).toEqual(body({ text: '{\n  "a": 1\n}' }));
    expect(view().state.doc.toString()).toBe('{\n  "a": 1\n}');

    act(() => void undo(view()));
    await act(settle);
    expect(saved()).toEqual(body({ text: '{ "a": 1 }' }));

    format.MinifyJSON.mockImplementation(() => cancellable({ valid: true, text: '{"a":1}', error: null }));
    fireEvent.click(button("Minify"));
    await act(settle);
    expect(format.MinifyJSON).toHaveBeenCalledWith('{ "a": 1 }');
    expect(saved()).toEqual(body());
    expect(view().state.doc.toString()).toBe('{"a":1}');
  });

  it("refuses to format invalid JSON and shows where the error is", async () => {
    await setup(request({ body: body({ text: '{"a":}' }) }));
    format.BeautifyJSON.mockImplementation(() => cancellable({ valid: false, text: '{"a":}', error: jsonError() }));
    fireEvent.click(button("Beautify"));
    await act(settle);
    expect(service.UpdateRequest).not.toHaveBeenCalled();
    expect(section().querySelector(".cm-errorMarker")?.textContent).toBe("}");
    expect(view().state.selection.main.head).toBe(5);
  });

  it("drops a format result when the text changed meanwhile", async () => {
    await setup();
    let answer!: (v: unknown) => void;
    format.BeautifyJSON.mockImplementation(() => Object.assign(new Promise((r) => (answer = r)), { cancel: vi.fn() }));
    fireEvent.click(button("Beautify"));
    act(() => view().dispatch({ changes: { from: 0, insert: " " }, userEvent: "input.type" }));
    await act(async () => {
      answer({ valid: true, text: '{\n  "a": 1\n}', error: null });
      await settle();
    });
    expect(saved()).toEqual(body({ text: ' {"a":1}' }));
  });

  it("beautifies with the shortcut", async () => {
    const stores = await setup();
    const stop = installShortcuts(stores);
    try {
      format.BeautifyJSON.mockImplementation(() => cancellable({ valid: true, text: '{\n  "a": 1\n}', error: null }));
      const event = beautifyKey();
      act(() => void view().contentDOM.dispatchEvent(event));
      await act(settle);
      expect(event.defaultPrevented).toBe(true);
      expect(format.BeautifyJSON).toHaveBeenCalledWith('{"a":1}', 2);
      expect(saved()).toEqual(body({ text: '{\n  "a": 1\n}' }));
    } finally {
      stop();
    }
  });

  it("keeps a multipart body across a detour through raw", async () => {
    const multipart = body({
      mode: "multipart",
      type: "",
      text: "",
      parts: [{ name: "f", value: "", file: "a.png", contentType: "image/png", description: "", enabled: true, extra: "" }],
      extra: "",
    });
    await setup(request({ body: multipart }));
    expect(radio("Multipart form").checked).toBe(true);
    expect(radio("Form URL-encoded").disabled).toBe(false);
    expect(radio("Binary file").disabled).toBe(false);
    expect(section().textContent).not.toContain("not available yet");
    expect(section().querySelector(".cm-editor")).toBeNull();

    fireEvent.click(radio("Raw"));
    await act(settle);
    expect(saved()).toEqual(body({ text: "", extra: "" }));
    fireEvent.click(radio("Multipart form"));
    await act(settle);
    expect(saved()).toEqual(multipart);
  });

  it("names a body mode of a newer version and drops its members only when left", async () => {
    const future = body({ mode: "graphql2", type: "", text: "", extra: '{"query":"{a}"}' });
    await setup(request({ body: future }));
    expect(radio("“graphql2”").checked).toBe(true);
    expect(section().textContent).toContain("from a newer version of PostSonde");
    expect(service.UpdateRequest).not.toHaveBeenCalled();
    fireEvent.click(radio("None"));
    await act(settle);
    expect(saved()).toEqual(body({ mode: "none", type: "", text: "", extra: "" }));
  });

  it("is read-only for a read-only request", async () => {
    const stores = await setup(request(), { readOnly: "newer_version" });
    const stop = installShortcuts(stores);
    try {
      expect(radio("None").disabled).toBe(true);
      expect(radio("Raw").disabled).toBe(true);
      expect(language()?.disabled).toBe(true);
      expect(button("Beautify").disabled).toBe(true);
      expect(button("Minify").disabled).toBe(true);
      expect(view().state.readOnly).toBe(true);
      const event = beautifyKey();
      view().contentDOM.dispatchEvent(event);
      expect(event.defaultPrevented).toBe(false);
      await act(settle);
      expect(format.BeautifyJSON).not.toHaveBeenCalled();
      expect(service.UpdateRequest).not.toHaveBeenCalled();
    } finally {
      stop();
    }
  });
});

describe("BodyEditor: URL-encoded form", () => {
  const field = (name: string, value: string, over: Partial<KeyValueDTO> = {}): KeyValueDTO => ({
    name,
    value,
    description: "",
    enabled: true,
    extra: "",
    ...over,
  });
  const form = (fields: KeyValueDTO[] | null, over: Partial<BodyDTO> = {}) =>
    body({ mode: "urlencoded", type: "", text: "", fields, extra: "", ...over });
  const grid = () => within(section()).getByRole("grid", { name: "Form fields" });
  const cell = (label: string) => within(grid()).getByRole("textbox", { name: label }) as HTMLInputElement;

  it("shows the fields of a form body, with descriptions, without saving", async () => {
    await setup(request({ body: form([field("user", "mike", { description: "Login" }), field("debug", "1", { enabled: false })]) }));
    expect(radio("Form URL-encoded").checked).toBe(true);
    expect(cell("Key, row 1").value).toBe("user");
    expect(cell("Value, row 1").value).toBe("mike");
    expect(cell("Description, row 1").value).toBe("Login");
    expect((within(grid()).getAllByRole("checkbox")[1] as HTMLInputElement).checked).toBe(false);
    expect(language()).toBeNull();
    expect(section().querySelector(".cm-editor")).toBeNull();
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("saves edits, new rows and toggles as plain text, keeping unknown row members", async () => {
    await setup(request({ body: form([field("a b", "c&d", { extra: '{"x-row":1}' })]) }));
    fireEvent.change(cell("Value, row 1"), { target: { value: "100% & more" } });
    await act(settle);
    expect(saved()).toEqual(form([field("a b", "100% & more", { extra: '{"x-row":1}' })]));

    fireEvent.change(cell("Key, row 2"), { target: { value: "tag" } });
    await act(settle);
    expect(saved().fields).toEqual([field("a b", "100% & more", { extra: '{"x-row":1}' }), field("tag", "")]);

    fireEvent.click(within(grid()).getAllByRole("checkbox")[0] as HTMLElement);
    await act(settle);
    expect(saved().fields?.[0]).toEqual(field("a b", "100% & more", { enabled: false, extra: '{"x-row":1}' }));
  });

  it("switches raw to form and back without converting, restoring each mode's content", async () => {
    await setup();
    fireEvent.click(radio("Form URL-encoded"));
    await act(settle);
    expect(saved()).toEqual(form(null, { extra: '{"x-body":true}' }));
    expect(within(grid()).getAllByRole("row")).toHaveLength(2); // header and the empty row

    fireEvent.change(cell("Key, row 1"), { target: { value: "k" } });
    await act(settle);
    expect(saved().fields).toEqual([field("k", "")]);

    fireEvent.click(radio("Raw"));
    await act(settle);
    expect(saved()).toEqual(body());

    fireEvent.click(radio("Form URL-encoded"));
    await act(settle);
    expect(saved()).toEqual(form([field("k", "")], { extra: '{"x-body":true}' }));
    expect(cell("Key, row 1").value).toBe("k");
  });

  it("is read-only for a read-only request", async () => {
    await setup(request({ body: form([field("a", "1")]) }), { readOnly: "newer_version" });
    expect(cell("Key, row 1").readOnly).toBe(true);
    expect(within(grid()).queryByRole("textbox", { name: "Key, row 2" })).toBeNull();
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });
});

describe("BodyEditor multipart form", () => {
  const part = (name: string, value: string, over: Partial<PartDTO> = {}): PartDTO => ({
    name,
    value,
    file: "",
    contentType: "",
    description: "",
    enabled: true,
    extra: "",
    ...over,
  });
  const multipart = (parts: PartDTO[] | null, over: Partial<BodyDTO> = {}) =>
    body({ mode: "multipart", type: "", text: "", parts, extra: "", ...over });
  const grid = () => within(section()).getByRole("grid", { name: "Multipart parts" });
  const cell = (label: string) => within(grid()).getByRole("textbox", { name: label }) as HTMLInputElement;
  const kind = (row: number) => within(grid()).getByRole("combobox", { name: `Part kind, row ${row}` }) as HTMLSelectElement;

  it("shows text and file parts, the file's name and size, and the guessed type", async () => {
    await setup(
      request({
        body: multipart([
          part("caption", "Hi {{user}}", { description: "Shown under the photo" }),
          part("image", "", { file: "fixtures/avatar.png", extra: '{"x-part":1}' }),
          part("cv", "", { file: "gone.pdf" }),
        ]),
      }),
    );
    await act(settle);
    expect(cell("Value, row 1").value).toBe("Hi {{user}}");
    expect(kind(1).value).toBe("text");
    expect(kind(2).value).toBe("file");
    expect(within(grid()).queryByRole("textbox", { name: "Value, row 2" })).toBeNull();
    expect(grid().textContent).toContain("avatar.png");
    expect(grid().textContent).toContain("2.0 KB");
    expect(within(grid()).getByText("avatar.png").closest("[title]")?.getAttribute("title")).toBe("/p/shop/fixtures/avatar.png · 2.0 KB");
    expect(grid().textContent).toContain("missing");
    expect(cell("Content-Type, row 2").placeholder).toBe("image/png");
    expect(cell("Content-Type, row 3").placeholder).toBe("application/pdf");
    expect(cell("Content-Type, row 1").placeholder).toBe("Content-Type");
    expect(workspace.BodyFile).toHaveBeenCalledWith("fixtures/avatar.png");
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("saves text edits, new parts and Content-Types, keeping files and unknown members", async () => {
    const image = part("image", "", { file: "fixtures/avatar.png", extra: '{"x-part":1}' });
    await setup(request({ body: multipart([part("caption", "a"), image]) }));
    fireEvent.change(cell("Value, row 1"), { target: { value: "b & c" } });
    await act(settle);
    expect(saved()).toEqual(multipart([part("caption", "b & c"), image]));

    fireEvent.change(cell("Key, row 2"), { target: { value: "photo" } });
    await act(settle);
    expect(saved().parts?.[1]).toEqual({ ...image, name: "photo" });

    fireEvent.change(cell("Content-Type, row 2"), { target: { value: "image/webp" } });
    await act(settle);
    expect(saved().parts?.[1]).toEqual({ ...image, name: "photo", contentType: "image/webp" });

    fireEvent.change(cell("Key, row 3"), { target: { value: "extra" } });
    await act(settle);
    expect(saved().parts?.[2]).toEqual(part("extra", ""));
  });

  it("makes a part a file with the native dialog, and a text part again", async () => {
    await setup(request({ body: multipart([part("doc", "typed text")]) }));
    workspace.PickBodyFile.mockImplementation(() => cancellable(fileDTO("", null)));
    fireEvent.change(kind(1), { target: { value: "file" } });
    await act(settle);
    expect(workspace.PickBodyFile).toHaveBeenCalledTimes(1);
    expect(service.UpdateRequest).not.toHaveBeenCalled();
    expect(kind(1).value).toBe("text");

    workspace.PickBodyFile.mockImplementation(() => cancellable(fileDTO("/Users/me/cv.PDF", 2048)));
    fireEvent.change(kind(1), { target: { value: "file" } });
    await act(settle);
    expect(saved().parts).toEqual([part("doc", "", { file: "/Users/me/cv.PDF" })]);
    expect(kind(1).value).toBe("file");
    expect(grid().textContent).toContain("cv.PDF");
    expect(grid().textContent).toContain("2.0 KB");
    expect(cell("Content-Type, row 1").placeholder).toBe("application/pdf");

    workspace.PickBodyFile.mockImplementation(() => cancellable(fileDTO("fixtures/b.png", 10)));
    fireEvent.click(within(grid()).getByRole("button", { name: "Choose file, row 1" }));
    await act(settle);
    expect(saved().parts).toEqual([part("doc", "", { file: "fixtures/b.png" })]);

    fireEvent.change(kind(1), { target: { value: "text" } });
    await act(settle);
    expect(saved().parts).toEqual([part("doc", "")]);
    expect(cell("Value, row 1").value).toBe("");
  });

  it("reports a dialog that cannot be shown", async () => {
    await setup(request({ body: multipart([part("f", "", { file: "a.bin" })]) }));
    const failed = Promise.reject(new Error("no window"));
    failed.catch(() => {});
    workspace.PickBodyFile.mockImplementation(() => Object.assign(failed, { cancel: vi.fn() }));
    fireEvent.click(within(grid()).getByRole("button", { name: "Choose file, row 1" }));
    await act(settle);
    expect(within(section()).getByRole("alert").textContent).toContain("no window");
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("starts empty when a raw body becomes multipart", async () => {
    await setup();
    fireEvent.click(radio("Multipart form"));
    await act(settle);
    expect(saved()).toEqual(multipart(null, { extra: '{"x-body":true}' }));
    fireEvent.change(cell("Key, row 1"), { target: { value: "k" } });
    await act(settle);
    expect(saved().parts).toEqual([part("k", "")]);
  });

  it("is read-only for a read-only request", async () => {
    await setup(request({ body: multipart([part("a", "1"), part("f", "", { file: "a.png" })]) }), { readOnly: "newer_version" });
    expect(cell("Key, row 1").readOnly).toBe(true);
    expect(cell("Content-Type, row 1").readOnly).toBe(true);
    expect(kind(2).disabled).toBe(true);
    expect((within(grid()).getByRole("button", { name: "Choose file, row 2" }) as HTMLButtonElement).disabled).toBe(true);
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });
});

describe("BodyEditor binary file", () => {
  const fileBody = (over: Partial<BodyDTO> = {}) => body({ mode: "file", type: "", text: "", extra: "", ...over });
  const typeField = () => within(section()).getByRole("textbox", { name: "Content-Type" }) as HTMLInputElement;
  const choose = () => within(section()).getByRole("button", { name: "Choose body file" }) as HTMLButtonElement;

  it("chooses the file with the native dialog and saves its path", async () => {
    await setup();
    fireEvent.click(radio("Binary file"));
    await act(settle);
    expect(saved()).toEqual(fileBody({ extra: '{"x-body":true}' }));
    expect(section().textContent).toContain("No file chosen");
    expect(typeField().placeholder).toBe("application/octet-stream");

    workspace.PickBodyFile.mockImplementation(() => cancellable(fileDTO("fixtures/avatar.png", 2048)));
    fireEvent.click(choose());
    await act(settle);
    expect(saved()).toEqual(fileBody({ path: "fixtures/avatar.png", extra: '{"x-body":true}' }));
    expect(section().textContent).toContain("avatar.png");
    expect(section().textContent).toContain("2.0 KB");
    expect(typeField().placeholder).toBe("image/png");

    fireEvent.change(typeField(), { target: { value: "image/apng" } });
    await act(settle);
    expect(saved()).toEqual(fileBody({ path: "fixtures/avatar.png", contentType: "image/apng", extra: '{"x-body":true}' }));
  });

  it("keeps the file when the dialog is cancelled and marks a missing file", async () => {
    await setup(request({ body: fileBody({ path: "gone.bin" }) }));
    await act(settle);
    expect(section().textContent).toContain("gone.bin");
    expect(section().textContent).toContain("missing");
    workspace.PickBodyFile.mockImplementation(() => cancellable(fileDTO("", null)));
    fireEvent.click(choose());
    await act(settle);
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("is read-only for a read-only request", async () => {
    await setup(request({ body: fileBody({ path: "a.bin" }) }), { readOnly: "newer_version" });
    expect(choose().disabled).toBe(true);
    expect(typeField().readOnly).toBe(true);
  });
});

describe("BodyEditor body examples", () => {
  const examples = () => within(section()).queryByRole("combobox", { name: "Body example" }) as HTMLSelectElement | null;
  const nameField = () => within(section()).getByRole("textbox", { name: "Example name" }) as HTMLInputElement;
  const sent = () => {
    const calls = service.UpdateRequest.mock.calls;
    return calls[calls.length - 1]?.[0].request as RequestDTO;
  };
  const form = body({
    mode: "urlencoded",
    type: "",
    text: "",
    fields: [{ name: "size", value: "large", description: "", enabled: true, extra: "" }],
    extra: "",
  });

  it("adds the first example as a copy of the body, with the body as Default", async () => {
    await setup();
    expect(examples()).toBeNull();
    fireEvent.click(button("Add example"));
    await act(settle);
    const req = sent();
    expect(req.body).toEqual(body());
    expect(req.bodyExamples).toEqual([
      { name: "Default", active: false, body: body(), extra: "" },
      { name: "Example 2", active: true, body: expect.objectContaining({ mode: "" }), extra: "" },
    ]);
    expect(examples()?.value).toBe("1");
    expect(nameField().value).toBe("Example 2");
  });

  it("switches the body that is sent, renames and deletes the active example", async () => {
    await setup(
      request({
        bodyExamples: [
          { name: "Small", active: true, body: body({ mode: "" }), extra: "" },
          { name: "Large", active: false, body: form, extra: '{"x-ex":1}' },
        ],
      }),
    );
    expect(examples()?.value).toBe("0");
    expect(section().querySelector(".cm-content")?.textContent).toBe('{"a":1}');

    fireEvent.change(examples() as HTMLSelectElement, { target: { value: "1" } });
    await act(settle);
    let req = sent();
    expect(req.body).toEqual(form);
    expect(req.bodyExamples?.[0]).toEqual({ name: "Small", active: false, body: body(), extra: "" });
    expect(req.bodyExamples?.[1]).toMatchObject({ name: "Large", active: true, body: { mode: "" }, extra: '{"x-ex":1}' });
    expect(radio("Form URL-encoded").checked).toBe(true);

    fireEvent.change(nameField(), { target: { value: "Big" } });
    await act(settle);
    req = sent();
    expect(req.bodyExamples?.[1]?.name).toBe("Big");

    // Deleting the active example makes the one before it active again.
    fireEvent.click(button("Delete"));
    await act(settle);
    req = sent();
    expect(req.body).toEqual(body());
    expect(req.bodyExamples).toEqual([{ name: "Small", active: true, body: expect.objectContaining({ mode: "" }), extra: "" }]);
  });
});

describe("BodyEditor SOAP helper", () => {
  const field = (name: string) => within(section()).getByRole("textbox", { name }) as HTMLInputElement;
  const version = () => within(section()).getByRole("combobox", { name: "SOAP version" }) as HTMLSelectElement;
  const sent = () => {
    const calls = service.UpdateRequest.mock.calls;
    return calls[calls.length - 1]?.[0].request as RequestDTO;
  };
  const row = (name: string, value: string, enabled = true): KeyValueDTO => ({ name, value, description: "", enabled, extra: "" });

  it("puts the envelope in a raw XML body and sets the 1.2 headers", async () => {
    format.SOAPEnvelope.mockImplementation(() =>
      cancellable({
        text: "<soap:Envelope/>",
        headers: [{ name: "Content-Type", value: 'application/soap+xml; charset=utf-8; action="urn:a"' }],
        remove: ["SOAPAction"],
      }),
    );
    await setup(
      request({
        body: body({ mode: "raw", type: "xml", text: "<Ping/>" }),
        headers: [row("content-type", "application/json", false), row("X-Keep", "1"), row("SOAPAction", '"old"')],
      }),
    );
    fireEvent.click(button("SOAP…"));
    fireEvent.change(version(), { target: { value: "1.2" } });
    fireEvent.change(field("SOAP action"), { target: { value: " urn:a " } });
    fireEvent.click(button("Apply envelope"));
    await act(settle);
    expect(format.SOAPEnvelope).toHaveBeenCalledWith({ version: "1.2", action: "urn:a", text: "<Ping/>" });
    const req = sent();
    expect(req.body).toEqual(body({ mode: "raw", type: "xml", text: "<soap:Envelope/>" }));
    expect(req.headers).toEqual([row("content-type", 'application/soap+xml; charset=utf-8; action="urn:a"'), row("X-Keep", "1")]);
  });

  it("starts a new envelope from a body of another mode", async () => {
    format.SOAPEnvelope.mockImplementation(() =>
      cancellable({
        text: "<env/>",
        headers: [
          { name: "Content-Type", value: "text/xml; charset=utf-8" },
          { name: "SOAPAction", value: '""' },
        ],
        remove: [],
      }),
    );
    await setup(request({ body: body({ mode: "none", type: "", text: "" }) }));
    fireEvent.click(button("SOAP…"));
    fireEvent.click(button("Apply envelope"));
    await act(settle);
    expect(format.SOAPEnvelope).toHaveBeenCalledWith({ version: "1.1", action: "", text: "" });
    const req = sent();
    expect(req.body).toEqual(body({ mode: "raw", type: "xml", text: "<env/>" }));
    expect(req.headers).toEqual([row("Content-Type", "text/xml; charset=utf-8"), row("SOAPAction", '""')]);
  });
});
