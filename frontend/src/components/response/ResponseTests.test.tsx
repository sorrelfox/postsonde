import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { TestResultDTO } from "../../lib/assertions";
import { httpResponse } from "../../test/httpResponse";
import { ResponseView } from "./ResponseView";

const result = (over: Partial<TestResultDTO>): TestResultDTO => ({
  index: 0,
  target: "status",
  expr: "",
  op: "equals",
  expected: "200",
  outcome: "passed",
  actual: "200",
  exists: true,
  message: "Status code equals 200: it was 200.",
  code: "",
  schemaErrors: null,
  ...over,
});

const TESTS: TestResultDTO[] = [
  result({}),
  result({
    index: 2,
    target: "jsonpath",
    expr: "$.id",
    op: "is_type",
    expected: "string",
    outcome: "failed",
    actual: "42",
    message: "Expected JSONPath $.id to be of type string, but it was number (42).",
  }),
  result({ index: 3, target: "header", expr: "X-Id", op: "exists", expected: "", outcome: "failed", actual: "", exists: false, message: "Expected header X-Id to exist, but it does not exist." }),
  result({ index: 4, target: "jsonpath", expr: "$[", op: "exists", expected: "", outcome: "invalid", actual: "", exists: false, message: "The JSONPath does not parse.", code: "jsonpath.invalid_query" }),
];

const badge = () => document.querySelector("[data-tests-badge]");

describe("ResponseView Tests tab (TEST-06)", () => {
  it("shows passed/total on the tab, and each result with expected vs actual", () => {
    const onJump = vi.fn();
    render(<ResponseView response={httpResponse({ tests: TESTS })} tests={{ onJump }} />);
    expect(badge()?.textContent).toBe("1/4");
    expect(badge()?.getAttribute("data-tone")).toBe("bad");
    expect(badge()?.getAttribute("aria-label")).toBe("1 of 4 passed");

    fireEvent.click(screen.getByRole("tab", { name: /^Tests/ }));
    expect(screen.getByRole("status").textContent).toBe("1 of 4 passed2 failed1 invalid");
    const items = within(screen.getByRole("list", { name: "Test results" })).getAllByRole("listitem");
    expect(items.map((li) => li.getAttribute("data-outcome"))).toEqual(["passed", "failed", "failed", "invalid"]);

    const failed = items[1] as HTMLElement;
    expect(within(failed).getByRole("img", { name: "Failed" })).toBeTruthy();
    expect(within(failed).getByText("Expected JSONPath $.id to be of type string, but it was number (42).")).toBeTruthy();
    expect(within(failed).getByText("Expected").nextElementSibling?.textContent).toBe("string");
    expect(within(failed).getByText("Actual").nextElementSibling?.textContent).toBe("42");
    // exists takes no expected value; a missing target says so.
    const missing = items[2] as HTMLElement;
    expect(within(missing).queryByText("Expected")).toBeNull();
    expect(within(missing).getByText("Actual").nextElementSibling?.textContent).toBe("does not exist");
    // An invalid assertion shows its code, not values.
    const invalid = items[3] as HTMLElement;
    expect(within(invalid).queryByText("Actual")).toBeNull();
    expect(within(invalid).getByText("jsonpath.invalid_query")).toBeTruthy();

    // The assertion jumps to its row in the request's Tests sub-tab.
    fireEvent.click(within(failed).getByRole("button", { name: "JSONPath $.id is type string" }));
    expect(onJump).toHaveBeenCalledWith(2);
  });

  it("is green when all pass, and says so when nothing was checked", () => {
    const onJump = vi.fn();
    const { rerender } = render(<ResponseView response={httpResponse({ tests: [result({})] })} tests={{ onJump }} />);
    expect(badge()?.getAttribute("data-tone")).toBe("ok");

    rerender(<ResponseView response={httpResponse({ tests: [] })} tests={{ onJump }} />);
    expect(badge()).toBeNull();
    fireEvent.click(screen.getByRole("tab", { name: "Tests" }));
    expect(screen.getByText(/No assertions were checked/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Add assertions in the request's Tests tab" }));
    expect(onJump).toHaveBeenCalledWith(null);
  });

  it("lists the schema errors of a failed matches schema assertion (TEST-07)", () => {
    const schemaResult = result({
      target: "body",
      op: "matches_schema",
      expected: `{"type":"object","properties":{"id":{"type":"integer"}},"description":"${"x".repeat(400)}"}`,
      outcome: "failed",
      actual: '{"id":"a"}',
      message: "Expected body to match the inline JSON schema, but $.id must be integer, not string.",
      schemaErrors: [
        { path: "$.id", instancePath: "/id", schemaPath: "#/properties/id/type", keyword: "type", message: "must be integer, not string" },
        { path: "$", instancePath: "", schemaPath: "#/required", keyword: "required", message: 'missing required property "name"' },
      ],
    });
    render(<ResponseView response={httpResponse({ tests: [schemaResult, result({ index: 1, op: "matches_schema", expected: "schemas/s.json", target: "body" })] })} tests={{}} />);
    fireEvent.click(screen.getByRole("tab", { name: /^Tests/ }));
    const items = within(screen.getByRole("list", { name: "Test results" })).getAllByRole("listitem", { hidden: false });
    const failed = items[0] as HTMLElement;
    expect(within(failed).getByText("Body matches schema (inline)")).toBeTruthy();
    const errors = within(failed).getByRole("list", { name: "Schema errors" });
    expect(within(errors).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "$.id must be integer, not string #/properties/id/type",
      '$ missing required property "name" #/required',
    ]);
    // A long inline schema is cut.
    const shown = within(failed).getByText("Expected").nextElementSibling?.textContent ?? "";
    expect(shown.endsWith("…")).toBe(true);
    expect(shown.length).toBe(301);
    const passed = screen.getAllByRole("listitem").find((li) => li.getAttribute("data-outcome") === "passed") as HTMLElement;
    expect(within(passed).getByText("Expected").nextElementSibling?.textContent).toBe("schemas/s.json");
    expect(within(passed).queryByRole("list", { name: "Schema errors" })).toBeNull();
  });

  it("has no Tests tab for a response that is not a request's own", () => {
    render(<ResponseView response={httpResponse({ tests: TESTS })} />);
    expect(screen.queryByRole("tab", { name: /^Tests/ })).toBeNull();
  });
});
