import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  FilterBody: vi.fn<(input: Record<string, unknown>) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));

import { SearchQuery, setSearchQuery } from "@codemirror/search";
import { EditorView } from "@codemirror/view";
import type { ResponseDTO } from "../../lib/http";
import { RESPONSE_FILTERS_UI_STATE_KEY } from "../../lib/responseFilter";
import { resetWorkspaceLocal, seedWorkspaceLocal, setWorkspaceLocalWriter, workspaceLocalIdle } from "../../lib/workspaceLocal";
import { httpResponse } from "../../test/httpResponse";
import { ResponseView } from "./ResponseView";

const BODY = '{"items":[{"id":1,"name":"a"},{"id":2,"name":"b"}]}';

function jsonResponse(over: Partial<ResponseDTO> = {}): ResponseDTO {
  return httpResponse({ body: BODY, bodyPretty: BODY, bodyId: "body_1", size: BODY.length, bodyShown: BODY.length, ...over });
}

function ok(text: string, count: number, over: Record<string, unknown> = {}) {
  return { valid: true, error: null, resultType: "nodes", count, text, clipped: false, language: "json", paths: [], ...over };
}

const filterBox = () => screen.getByRole("searchbox", { name: /Filter the body/ });
const editorText = () =>
  (EditorView.findFromDOM(document.querySelector("[data-filtered-body] .cm-editor") as HTMLElement) as EditorView).state.doc.toString();
const type = (text: string) => fireEvent.change(filterBox(), { target: { value: text } });

beforeEach(() => {
  http.FilterBody.mockReset();
});

afterEach(() => resetWorkspaceLocal());

describe("response filter (HTTP-36)", () => {
  it("filters a JSON body with JSONPath in Go, on the bytes kept, and shows the count", async () => {
    http.FilterBody.mockResolvedValue(ok('[\n  "a",\n  "b"\n]', 2, { paths: ["$['items'][0]['name']", "$['items'][1]['name']"] }));
    render(<ResponseView response={jsonResponse()} />);
    expect(screen.getByRole("button", { name: "JSONPath" }).getAttribute("aria-pressed")).toBe("true");

    type("$.items[*].name");
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("2 matches"));
    expect(http.FilterBody).toHaveBeenCalledTimes(1);
    expect(http.FilterBody).toHaveBeenCalledWith({
      bodyId: "body_1",
      text: "",
      syntax: "jsonpath",
      expression: "$.items[*].name",
      charset: "UTF-8",
      html: false,
    });
    expect(editorText()).toBe('[\n  "a",\n  "b"\n]');
    // The Pretty / Raw switch does not apply to a filtered body.
    expect(screen.queryByRole("button", { name: "Raw" })).toBeNull();

    // Paths instead of values.
    fireEvent.click(screen.getByRole("button", { name: "Paths" }));
    expect(editorText()).toBe("$['items'][0]['name']\n$['items'][1]['name']");
  });

  it("debounces typing into one evaluation", async () => {
    http.FilterBody.mockResolvedValue(ok("[]", 0));
    render(<ResponseView response={jsonResponse()} />);
    type("$");
    type("$.");
    type("$.x");
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("No matches"));
    expect(http.FilterBody).toHaveBeenCalledTimes(1);
    expect(http.FilterBody.mock.calls[0][0].expression).toBe("$.x");
    expect(screen.getByText("Nothing in the body matches the filter.")).toBeTruthy();
  });

  it("says where a bad expression is wrong, and Esc clears the filter", async () => {
    http.FilterBody.mockResolvedValue({
      valid: false,
      error: { where: "expression", message: "Unexpected end of query: expected a selector.", offset: 8, line: 0, column: 0 },
      resultType: "",
      count: 0,
      text: "",
      clipped: false,
      language: "",
      paths: [],
    });
    render(<ResponseView response={jsonResponse()} />);
    type(" $.items[");
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Unexpected end of query: expected a selector at position 9.");
    expect(alert.querySelector("mark")?.textContent).toBe(" ");
    expect(filterBox().getAttribute("aria-invalid")).toBe("true");

    fireEvent.click(screen.getByRole("button", { name: "position 9" }));
    const box = filterBox() as HTMLInputElement;
    expect(box.selectionStart).toBe(9);

    fireEvent.keyDown(box, { key: "Escape" });
    expect(box.value).toBe("");
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByRole("button", { name: "Raw" })).toBeTruthy();
  });

  it("shows why the body is not JSON, with its line and column", async () => {
    http.FilterBody.mockResolvedValue({
      valid: false,
      error: { where: "body", message: "The body is not valid JSON: Invalid character '}'.", offset: -1, line: 1, column: 12 },
      resultType: "",
      count: 0,
      text: "",
      clipped: false,
      language: "",
      paths: [],
    });
    render(<ResponseView response={jsonResponse()} />);
    type("$.a");
    expect((await screen.findByRole("alert")).textContent).toContain("The body is not valid JSON: Invalid character '}' (line 1, column 12).");
  });

  it("filters the text shown once Go no longer keeps the body", async () => {
    http.FilterBody.mockRejectedValueOnce({ kind: "not_found", code: "http.body_gone", message: "gone", hint: "", details: {}, causes: [] });
    http.FilterBody.mockResolvedValueOnce(ok("[1]", 1));
    render(<ResponseView response={jsonResponse()} />);
    type("$.items[0].id");
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("1 match"));
    expect(http.FilterBody.mock.calls[1][0]).toMatchObject({ bodyId: "", text: BODY });
    expect(screen.getByText(/no longer kept, so the text shown was filtered/)).toBeTruthy();
  });

  it("picks XPath for XML, can be switched, and searches the filtered result", async () => {
    http.FilterBody.mockResolvedValue(ok('<b id="1">x</b>\n<b id="2">y</b>', 2, { language: "xml" }));
    const xml = '<a><b id="1">x</b><b id="2">y</b></a>';
    render(<ResponseView response={jsonResponse({ body: xml, bodyPretty: xml, bodyLanguage: "xml", contentType: "text/xml" })} />);
    expect(screen.getByRole("button", { name: "XPath" }).getAttribute("aria-pressed")).toBe("true");
    type("//b");
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("2 matches"));
    expect(http.FilterBody.mock.calls[0][0].syntax).toBe("xpath");

    fireEvent.click(screen.getByRole("button", { name: "Search in the body" }));
    expect(document.querySelector("[data-filtered-body] .cm-search")).not.toBeNull();
    const view = EditorView.findFromDOM(document.querySelector("[data-filtered-body] .cm-editor") as HTMLElement) as EditorView;
    act(() => view.dispatch({ effects: setSearchQuery.of(new SearchQuery({ search: "id=" })) }));
    expect(document.querySelectorAll(".cm-searchMatch").length).toBe(2);

    fireEvent.click(screen.getByRole("button", { name: "JSONPath" }));
    await waitFor(() => expect(http.FilterBody).toHaveBeenCalledTimes(2));
    expect(http.FilterBody.mock.calls[1][0].syntax).toBe("jsonpath");
  });

  it("offers no filter for a hex body", () => {
    render(<ResponseView response={jsonResponse({ bodyHex: true, bodyLanguage: "text" })} />);
    expect(screen.queryByRole("searchbox")).toBeNull();
  });

  it("remembers the filter and recent expressions per request in the workspace's UI state", async () => {
    const writes: [string, string][] = [];
    const previous = setWorkspaceLocalWriter(async (_id, _path, key, value) => {
      writes.push([key, value]);
    });
    try {
      seedWorkspaceLocal({
        id: "ws_1",
        path: "/p",
        uiState: { [RESPONSE_FILTERS_UI_STATE_KEY]: JSON.stringify({ req_1: { expression: "$.items", recent: [{ syntax: "jsonpath", expression: "$.old" }] } }) },
      });
      http.FilterBody.mockResolvedValue(ok("[]", 0));
      const persist = { workspaceId: "ws_1", requestId: "req_1" };
      const { unmount } = render(<ResponseView response={jsonResponse()} filter={persist} />);
      // Restored and applied to the new response.
      expect((filterBox() as HTMLInputElement).value).toBe("$.items");
      await waitFor(() => expect(http.FilterBody).toHaveBeenCalledTimes(1));
      expect(document.querySelectorAll("datalist option").length).toBe(1);

      type("$.items[0]");
      await waitFor(() => expect(http.FilterBody).toHaveBeenCalledTimes(2));
      await waitFor(() => expect(screen.getByRole("status").textContent).toBe("No matches"));
      fireEvent.keyDown(filterBox(), { key: "Enter" });
      unmount();
      await workspaceLocalIdle();
      expect(writes.length).toBe(1);
      expect(writes[0][0]).toBe(RESPONSE_FILTERS_UI_STATE_KEY);
      expect(JSON.parse(writes[0][1])).toEqual({
        req_1: {
          expression: "$.items[0]",
          recent: [
            { syntax: "jsonpath", expression: "$.items[0]" },
            { syntax: "jsonpath", expression: "$.old" },
          ],
        },
      });
    } finally {
      setWorkspaceLocalWriter(previous);
    }
  });
});
