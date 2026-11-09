import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);

import { jumpToExtraction, resetExtractionJumps } from "../../commands/extractionJump";
import { resetHttpSends } from "../../commands/httpSends";
import { newExtraction, type ExtractionDTO } from "../../lib/extractions";
import { createStores, StoreProvider, type RequestData, type Stores } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { settle } from "../../test/fakeWails";
import { ExtractionsEditor } from "./ExtractionsEditor";

const rule = (over: Partial<ExtractionDTO> = {}): ExtractionDTO => newExtraction(over);

/** The rules of each edit, in order. */
type Edits = ExtractionDTO[][];

/** The editor bound to its own state, as the request editor binds it; `edits` records every patch's rules. */
function Harness({ initial, readOnly = false, edits }: { initial: ExtractionDTO[]; readOnly?: boolean; edits: Edits }) {
  const [list, setList] = useState(initial);
  return (
    <ExtractionsEditor
      id="login"
      extractions={list}
      readOnly={readOnly}
      edit={(patch: RequestData) => {
        const next = patch.extractions as ExtractionDTO[];
        edits.push(next);
        setList(next);
      }}
    />
  );
}

function setup(initial: ExtractionDTO[] = [], readOnly = false): { stores: Stores; edits: Edits } {
  const stores = createStores();
  const edits: Edits = [];
  render(
    <StoreProvider stores={stores}>
      <Harness initial={initial} readOnly={readOnly} edits={edits} />
    </StoreProvider>,
  );
  return { stores, edits };
}

const table = () => screen.getByRole("table", { name: "Extraction rules" });
const last = (edits: Edits) => edits[edits.length - 1];

beforeEach(() => {
  resetHttpSends();
  resetExtractionJumps();
});

describe("ExtractionsEditor (TEST-05)", () => {
  it("adds a rule and edits its source, expression, variable, scope and flags", () => {
    const { edits } = setup();
    expect(screen.getByText(/No rules yet/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    expect(last(edits)).toEqual([rule()]);
    // The new row's source picker has focus.
    expect(document.activeElement).toBe(within(table()).getByRole("combobox", { name: "Source, rule 1" }));

    fireEvent.change(within(table()).getByRole("textbox", { name: "Expression, rule 1" }), { target: { value: "$.access_token" } });
    fireEvent.change(within(table()).getByRole("textbox", { name: "Variable name, rule 1" }), { target: { value: "token" } });
    fireEvent.click(within(table()).getByRole("button", { name: "Secret, rule 1" }));
    fireEvent.click(within(table()).getByRole("button", { name: "Keep after closing the project, rule 1" }));
    expect(last(edits)).toEqual([rule({ expr: "$.access_token", variable: "token", secret: true, persist: true })]);
    expect(within(table()).getByRole("button", { name: "Secret, rule 1" }).getAttribute("aria-pressed")).toBe("true");

    // A header takes its name.
    fireEvent.change(within(table()).getByRole("combobox", { name: "Source, rule 1" }), { target: { value: "header" } });
    expect(within(table()).getByRole("textbox", { name: "Header name, rule 1" })).toHaveProperty("value", "$.access_token");
    // The status code takes no expression: the one typed goes.
    fireEvent.change(within(table()).getByRole("combobox", { name: "Source, rule 1" }), { target: { value: "status" } });
    expect(within(table()).queryByRole("textbox", { name: "Expression, rule 1" })).toBeNull();
    expect(last(edits)?.[0]?.expr).toBe("");

    // Run only: a run variable is never kept.
    fireEvent.change(within(table()).getByRole("combobox", { name: "Scope, rule 1" }), { target: { value: "run" } });
    expect(last(edits)).toEqual([rule({ source: "status", variable: "token", scope: "run", secret: true, persist: false })]);
    const keep = within(table()).getByRole("button", { name: "Keep after closing the project, rule 1" });
    expect(keep).toHaveProperty("disabled", true);
    expect(keep.getAttribute("title")).toBe("A run variable lasts as long as the run");
  });

  it("reorders, disables and deletes rules, keeping unknown members", () => {
    const { edits } = setup([
      rule({ expr: "$.token", variable: "token", extra: '{"x-note":"kept"}' }),
      rule({ source: "cookie", expr: "session", variable: "session" }),
    ]);
    fireEvent.click(within(table()).getByRole("button", { name: "Move rule 2 up" }));
    expect(last(edits)?.map((e) => e.variable)).toEqual(["session", "token"]);
    fireEvent.keyDown(within(table()).getByRole("combobox", { name: "Source, rule 1" }), { key: "ArrowDown", altKey: true });
    expect(last(edits)?.map((e) => e.variable)).toEqual(["token", "session"]);
    expect(last(edits)?.[0]?.extra).toBe('{"x-note":"kept"}');

    fireEvent.click(within(table()).getByRole("checkbox", { name: "Enabled, rule 2" }));
    expect(last(edits)?.[1]?.enabled).toBe(false);

    fireEvent.click(within(table()).getByRole("button", { name: "Delete rule 1: JSONPath $.token into token" }));
    expect(last(edits)?.map((e) => e.variable)).toEqual(["session"]);
    // Focus goes to the next row's delete button.
    expect(document.activeElement).toBe(within(table()).getByRole("button", { name: "Delete rule 1: Cookie session into session" }));
    fireEvent.click(within(table()).getByRole("button", { name: /^Delete rule 1/ }));
    expect(last(edits)).toEqual([]);
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Add rule" }));
  });

  it("marks a rule without a variable name", () => {
    setup([rule({ expr: "$.a" })]);
    expect(within(table()).getByRole("textbox", { name: "Variable name, rule 1" }).getAttribute("aria-invalid")).toBe("true");
  });

  it("opens at a rule when a result jumps to it", async () => {
    const { stores } = setup([rule({ variable: "a" }), rule({ variable: "b" })]);
    act(() => jumpToExtraction(stores, "login", 1));
    await act(settle);
    expect(stores.drafts.getState().subTabs.login).toBe("extract");
    const source = within(table()).getByRole("combobox", { name: "Source, rule 2" });
    expect(document.activeElement).toBe(source);
    expect(source.closest("[data-highlight]")?.getAttribute("data-extraction-index")).toBe("1");
  });

  it("is read-only in a read-only request", () => {
    setup([rule({ variable: "token" })], true);
    expect(screen.queryByRole("button", { name: "Add rule" })).toBeNull();
    expect(within(table()).getByRole("combobox", { name: "Source, rule 1" })).toHaveProperty("disabled", true);
    expect(within(table()).getByRole("button", { name: "Secret, rule 1" })).toHaveProperty("disabled", true);
    expect(within(table()).queryByRole("button", { name: /^Delete rule/ })).toBeNull();
  });

  it("is accessible", () => {
    setup([
      rule({ expr: "$.token", variable: "token", secret: true }),
      rule({ source: "status", variable: "code", scope: "run" }),
      rule({ source: "later-source", variable: "x", scope: "suite" }),
    ]);
    expectAccessible();
  });
});
