import { fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { describe, expect, it, vi } from "vitest";
import type { ResponseDTO } from "../../lib/http";
import { httpResponse } from "../../test/httpResponse";
import { ResponseView } from "./ResponseView";

function json(body: string, over: Partial<ResponseDTO> = {}): ResponseDTO {
  return httpResponse({
    contentType: "application/graphql-response+json",
    body,
    bodyPretty: JSON.stringify(JSON.parse(body), null, 2),
    size: body.length,
    bodyShown: body.length,
    ...over,
  });
}

const view = () => EditorView.findFromDOM(document.querySelector(".cm-editor") as HTMLElement) as EditorView;
const bodyText = () => view().state.doc.toString();
/** The 1-based line numbers carrying a region marker. */
const marked = (region: "data" | "errors") =>
  [...document.querySelectorAll(`.cm-line[data-gql="${region}"]`)].map((el) => view().state.doc.lineAt(view().posAtDOM(el)).number);
const option = (name: string) => screen.queryByRole("button", { name });
const errorItems = () => {
  const list = screen.queryByRole("list", { name: "GraphQL errors" });
  return list === null ? [] : within(list).getAllByRole("listitem");
};

const DATA = '{"data":{"user":{"name":"Ada"}}}';
const ERRORS = '{"errors":[{"message":"Cannot query field \\"nme\\" on type \\"User\\".","locations":[{"line":3,"column":5}],"extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}';
const BOTH =
  '{"data":{"user":{"name":"Ada","email":null}},"errors":[{"message":"Not allowed","path":["user","email"],"locations":[{"line":4,"column":5}]}],"extensions":{"cost":7}}';

describe("ResponseView GraphQL mode (GQL-05)", () => {
  it("opens data in the GraphQL view with the data lines marked", () => {
    render(<ResponseView response={json(DATA)} graphql={{}} />);
    expect(option("GraphQL")?.getAttribute("aria-pressed")).toBe("true");
    expect(document.querySelector("[data-graphql-view]")?.textContent).toContain("No errors");
    expect(errorItems()).toHaveLength(0);
    expect(bodyText()).toBe('{\n  "data": {\n    "user": {\n      "name": "Ada"\n    }\n  }\n}');
    expect(marked("data")).toEqual([2, 3, 4, 5, 6]);
    expect(marked("errors")).toEqual([]);
    expect(option("Hide extensions")).toBeNull();
  });

  it("lists errors with their locations and code and marks the errors lines", () => {
    const onLocation = vi.fn();
    render(<ResponseView response={json(ERRORS)} graphql={{ onLocation }} />);
    expect(document.querySelector("[data-graphql-view]")?.textContent).toContain("1 error, no data");
    const [item] = errorItems();
    expect(item?.textContent).toContain('Cannot query field "nme" on type "User".');
    expect(item?.textContent).toContain("GRAPHQL_VALIDATION_FAILED");
    expect(marked("data")).toEqual([]);
    expect(marked("errors")[0]).toBe(2);
    expect(Math.max(...marked("errors"))).toBe(bodyText().split("\n").length - 1);
    fireEvent.click(within(item as HTMLElement).getByRole("button", { name: "Line 3, column 5" }));
    expect(onLocation).toHaveBeenCalledWith({ line: 3, column: 5 });
  });

  it("shows partial data with errors, both marked, and hides extensions on demand", () => {
    render(<ResponseView response={json(BOTH)} graphql={{}} />);
    expect(document.querySelector("[data-graphql-view]")?.textContent).toContain("1 error, partial data");
    expect(errorItems()[0]?.textContent).toContain("user.email");
    expect(marked("data").length).toBeGreaterThan(0);
    expect(marked("errors").length).toBeGreaterThan(0);
    expect(bodyText()).toContain('"cost": 7');

    const toggle = option("Hide extensions") as HTMLElement;
    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-pressed")).toBe("true");
    expect(bodyText()).not.toContain("extensions");
    expect(JSON.parse(bodyText())).toEqual({ data: { user: { name: "Ada", email: null } }, errors: [JSON.parse(BOTH).errors[0]] });
    // The regions follow the shorter text.
    expect(Math.max(...marked("errors"))).toBe(bodyText().split("\n").length - 1);

    // Raw keeps what was received, extensions included.
    fireEvent.click(option("Raw") as HTMLElement);
    expect(bodyText()).toBe(BOTH);
    expect(option("Hide extensions")).toBeNull();
    fireEvent.click(option("Pretty") as HTMLElement);
    expect(bodyText()).toContain('"cost": 7');
  });

  it("says when data is null", () => {
    render(<ResponseView response={json('{"errors":[{"message":"boom"}],"data":null}')} graphql={{}} />);
    expect(document.querySelector("[data-graphql-view]")?.textContent).toContain("1 error, data is null");
    // A location-less error has no jump.
    expect(within(errorItems()[0] as HTMLElement).queryByRole("button")).toBeNull();
  });

  it("falls back to Pretty for JSON that is not a GraphQL response, and outside GraphQL requests", () => {
    const { unmount } = render(<ResponseView response={json('{"message":"Not Found"}', { statusCode: 404, status: "404 Not Found" })} graphql={{}} />);
    expect(option("GraphQL")).toBeNull();
    expect(option("Pretty")?.getAttribute("aria-pressed")).toBe("true");
    expect(document.querySelector("[data-graphql-view]")).toBeNull();
    unmount();

    render(<ResponseView response={json(DATA)} />);
    expect(option("GraphQL")).toBeNull();
    expect(bodyText()).toBe(JSON.stringify(JSON.parse(DATA), null, 2));
  });

  it("opens a new response in the GraphQL view again", () => {
    const { rerender } = render(<ResponseView response={json(DATA)} graphql={{}} />);
    fireEvent.click(option("Raw") as HTMLElement);
    expect(bodyText()).toBe(DATA);
    rerender(<ResponseView response={json(ERRORS)} graphql={{}} />);
    expect(option("GraphQL")?.getAttribute("aria-pressed")).toBe("true");
    expect(errorItems()).toHaveLength(1);
  });
});
