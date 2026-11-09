import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import type { ExtractionResultDTO } from "../../lib/extractions";
import { expectAccessible } from "../../test/a11y";
import { httpResponse } from "../../test/httpResponse";
import { ResponseView } from "./ResponseView";

const result = (over: Partial<ExtractionResultDTO>): ExtractionResultDTO => ({
  index: 0,
  source: "jsonpath",
  expr: "$.access_token",
  variable: "token",
  scope: "runtime",
  secret: true,
  persist: false,
  outcome: "set",
  value: "••••••",
  valueClipped: false,
  message: "Set the runtime variable token from JSONPath $.access_token.",
  code: "",
  ...over,
});

const RESULTS: ExtractionResultDTO[] = [
  result({}),
  result({ index: 1, expr: "$.user.id", variable: "userId", secret: false, persist: true, value: "7" }),
  result({ index: 2, source: "header", expr: "X-Request-Id", variable: "rid", scope: "run", secret: false, outcome: "skipped", value: "", message: "Not set: rid is a run variable, set during a folder run only." }),
  result({ index: 3, source: "header", expr: "X-Missing", variable: "missing", secret: false, outcome: "failed", value: "", message: "Header X-Missing is not in the response.", code: "extract.not_found" }),
  result({ index: 5, source: "regex", expr: "(", variable: "bad", secret: false, outcome: "invalid", value: "", message: "The regular expression \"(\" is not valid.", code: "assert.invalid_regex" }),
  result({ index: 6, source: "status", expr: "", variable: "long", secret: false, value: "x".repeat(20), valueClipped: true }),
];

const badge = () => document.querySelector("[data-variables-badge]");

describe("ResponseView Variables tab (TEST-05)", () => {
  it("shows set/total on the tab, and what each rule set or why not", () => {
    const onExtractionJump = vi.fn();
    render(<ResponseView response={httpResponse({ extractions: RESULTS })} tests={{ onExtractionJump }} />);
    expect(badge()?.textContent).toBe("3/6");
    expect(badge()?.getAttribute("data-tone")).toBe("bad");
    expect(badge()?.getAttribute("aria-label")).toBe("3 of 6 variables set");

    fireEvent.click(screen.getByRole("tab", { name: /^Variables/ }));
    expect(screen.getByRole("status").textContent).toBe("3 of 6 variables set2 failed1 skipped");
    const items = within(screen.getByRole("list", { name: "Variables set from this response" })).getAllByRole("listitem");
    expect(items.map((li) => li.getAttribute("data-outcome"))).toEqual(["set", "set", "skipped", "failed", "invalid", "set"]);

    // A secret value is shown masked, as Go sent it; its tags say so.
    const token = items[0] as HTMLElement;
    expect(within(token).getByRole("img", { name: "Set" })).toBeTruthy();
    expect(within(token).getByText("From").nextElementSibling?.textContent).toBe("JSONPath $.access_token");
    expect(within(token).getByText("Value").nextElementSibling?.textContent).toBe("••••••");
    expect(within(token).getByText("Secret")).toBeTruthy();
    expect(within(token).getByText("Runtime")).toBeTruthy();
    const userId = items[1] as HTMLElement;
    expect(within(userId).getByText("Value").nextElementSibling?.textContent).toBe("7");
    expect(within(userId).getByText("Kept")).toBeTruthy();
    // Failed and skipped rules say why, with no value.
    const failed = items[3] as HTMLElement;
    expect(within(failed).queryByText("Value")).toBeNull();
    expect(within(failed).getByText("Header X-Missing is not in the response.")).toBeTruthy();
    expect(within(failed).getByText("extract.not_found")).toBeTruthy();
    expect(within(items[2] as HTMLElement).getByText("Run only")).toBeTruthy();
    expect(within(items[5] as HTMLElement).getByText(/the whole value was set/)).toBeTruthy();

    // The variable jumps to its rule.
    fireEvent.click(within(failed).getByRole("button", { name: "{{missing}}" }));
    expect(onExtractionJump).toHaveBeenCalledWith(3);
    expectAccessible();
  });

  it("is green when every rule set its variable, and absent without rules or for another response", () => {
    const { rerender } = render(<ResponseView response={httpResponse({ extractions: [result({})] })} tests={{}} />);
    expect(badge()?.getAttribute("data-tone")).toBe("ok");

    rerender(<ResponseView response={httpResponse({ extractions: [] })} tests={{}} />);
    expect(screen.queryByRole("tab", { name: /^Variables/ })).toBeNull();
    // A stored response or a body view is not a request's own send.
    rerender(<ResponseView response={httpResponse({ extractions: [result({})] })} />);
    expect(screen.queryByRole("tab", { name: /^Variables/ })).toBeNull();
  });
});
