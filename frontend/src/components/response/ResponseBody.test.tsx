import { act, fireEvent, render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const format = vi.hoisted(() => ({
  BeautifyJSON: vi.fn<(text: string, indent: number) => Promise<unknown>>(),
  IndentXML: vi.fn<(text: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));

import { SearchQuery, getSearchQuery, setSearchQuery } from "@codemirror/search";
import { EditorView } from "@codemirror/view";
import type { ResponseDTO } from "../../lib/http";
import { httpResponse } from "../../test/httpResponse";
import { settle } from "../../test/fakeWails";
import { ResponseView } from "./ResponseView";

const XML = '<a><b id="1">x</b><b id="2">y</b></a>';
const XML_PRETTY = '<a>\n  <b id="1">x</b>\n  <b id="2">y</b>\n</a>';

function xmlResponse(over: Partial<ResponseDTO> = {}): ResponseDTO {
  return httpResponse({
    contentType: "application/xml",
    body: XML,
    bodyLanguage: "xml",
    bodyPretty: XML_PRETTY,
    size: XML.length,
    bodyShown: XML.length,
    ...over,
  });
}

const editorView = () => EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement) as EditorView;
const bodyText = () => editorView().state.doc.toString();

beforeEach(() => {
  format.BeautifyJSON.mockReset();
  format.IndentXML.mockReset();
});

describe("ResponseBody", () => {
  it("shows XML indented by Go in Pretty and as received in Raw (HTTP-31)", () => {
    render(<ResponseView response={xmlResponse()} />);
    expect(bodyText()).toBe(XML_PRETTY);
    expect(screen.getByRole("button", { name: "Pretty" }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "Raw" }));
    expect(bodyText()).toBe(XML);
    fireEvent.click(screen.getByRole("button", { name: "Pretty" }));
    expect(bodyText()).toBe(XML_PRETTY);
  });

  it("highlights HTML in Pretty and shows the same text plain in Raw", () => {
    const body = "<!DOCTYPE html><p class=x>Hi &amp; bye</p>";
    render(<ResponseView response={httpResponse({ body, bodyLanguage: "html", bodyPretty: "", prettyState: "" })} />);
    expect(bodyText()).toBe(body);
    expect(document.querySelectorAll(".cm-content span").length).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "Raw" }));
    expect(bodyText()).toBe(body);
    expect(document.querySelectorAll(".cm-content span").length).toBe(0);
  });

  it("shows the charset, and plain text without a Pretty / Raw switch", () => {
    render(
      <ResponseView
        response={httpResponse({ body: "café", bodyLanguage: "text", bodyPretty: "", prettyState: "", charset: "ISO-8859-1", charsetSource: "header" })}
      />,
    );
    expect(bodyText()).toBe("café");
    expect(screen.getByTitle("Decoded as ISO-8859-1 (from the Content-Type charset)").textContent).toBe("ISO-8859-1");
    expect(screen.queryByRole("button", { name: "Raw" })).toBeNull();
  });

  it("shows a hex dump with Go's note", () => {
    const dump = "00000000  ff d8                                             |..|";
    render(
      <ResponseView
        response={httpResponse({
          body: dump,
          bodyHex: true,
          bodyLanguage: "text",
          bodyPretty: "",
          prettyState: "",
          charset: "",
          bodyNote: "The body is binary data (image/jpeg), so its bytes are shown.",
        })}
      />,
    );
    expect(bodyText()).toBe(dump);
    expect(screen.getByText("HEX")).toBeTruthy();
    expect(screen.getByText(/binary data \(image\/jpeg\)/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Raw" })).toBeNull();
  });

  it("says why an invalid body is not pretty-printed", () => {
    render(<ResponseView response={httpResponse({ body: '{"a":', bodyPretty: "", prettyState: "invalid", prettyError: "Not valid JSON: Unexpected end of JSON input (line 1, column 6)." })} />);
    expect(bodyText()).toBe('{"a":');
    expect(screen.getByText(/Not valid JSON: Unexpected end.*It is shown as received\./)).toBeTruthy();
  });

  it("opens a large body raw and pretty-prints it on request", async () => {
    format.BeautifyJSON.mockResolvedValue({ valid: true, text: '[\n  1\n]', error: null });
    render(<ResponseView response={httpResponse({ body: "[1]", bodyPretty: "", prettyState: "too_large", bodyShown: 300 * 1024 })} />);
    expect(screen.getByRole("button", { name: "Raw" }).getAttribute("aria-pressed")).toBe("true");
    expect(bodyText()).toBe("[1]");

    fireEvent.click(screen.getByRole("button", { name: "Pretty" }));
    expect(screen.getByText(/The JSON body is 300.0 KB, too large to pretty-print on arrival/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Pretty-print" }));
    await act(settle);
    expect(format.BeautifyJSON).toHaveBeenCalledWith("[1]", 2);
    expect(bodyText()).toBe("[\n  1\n]");
    expect(screen.queryByRole("button", { name: "Pretty-print" })).toBeNull();
  });

  it("reports an XML body that cannot be indented on request", async () => {
    format.IndentXML.mockResolvedValue({ valid: false, text: "<a>", error: { line: 1, column: 4, message: "unexpected end of document" } });
    render(<ResponseView response={xmlResponse({ body: "<a>", bodyPretty: "", prettyState: "too_large" })} />);
    fireEvent.click(screen.getByRole("button", { name: "Pretty" }));
    fireEvent.click(screen.getByRole("button", { name: "Pretty-print" }));
    await act(settle);
    expect(format.IndentXML).toHaveBeenCalledWith("<a>");
    expect(screen.getByText("unexpected end of document (line 1, column 4). It is shown as received.")).toBeTruthy();
    expect(bodyText()).toBe("<a>");
  });

  it("opens the search panel from the status line and highlights text and regex matches (HTTP-33)", () => {
    render(<ResponseView response={xmlResponse()} />);
    fireEvent.click(screen.getByRole("tab", { name: /Headers/ }));
    fireEvent.click(screen.getByRole("button", { name: "Search in the body" }));
    expect(screen.getByRole("tab", { name: "Body" }).getAttribute("aria-selected")).toBe("true");
    expect(document.querySelector(".cm-search")).not.toBeNull();

    const view = editorView();
    act(() => view.dispatch({ effects: setSearchQuery.of(new SearchQuery({ search: "id=" })) }));
    expect(document.querySelectorAll(".cm-searchMatch").length).toBe(2);

    act(() => view.dispatch({ effects: setSearchQuery.of(new SearchQuery({ search: '"[12]"', regexp: true })) }));
    expect(getSearchQuery(view.state).regexp).toBe(true);
    expect([...document.querySelectorAll(".cm-searchMatch")].map((m) => m.textContent)).toEqual(['"1"', '"2"']);
  });

  it("says when there is no body, and offers no search", () => {
    render(<ResponseView response={httpResponse({ body: "", bodyPretty: "", prettyState: "", charset: "" })} />);
    expect(screen.getByText("The response has no body.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Search in the body" })).toBeNull();
  });
});
