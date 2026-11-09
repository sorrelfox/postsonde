import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { SECRET_MASK } from "../../lib/templateVariables";
import type { EnvironmentVariable } from "../../stores";
import { auditA11y } from "../../test/a11y";
import { SAVE_DELAY_MS, VariablesEditor } from "./VariablesEditor";

const variable = (name: string, over: Partial<EnvironmentVariable> = {}): EnvironmentVariable => ({
  name,
  value: `${name}-value`,
  description: "",
  secret: false,
  enabled: true,
  localValue: null,
  ...over,
});

const VARIABLES = [
  variable("baseUrl", { value: "https://api.stg.test" }),
  variable("token", { value: "stg-s3cret", secret: true }),
  variable("user", { value: "team", localValue: "me" }),
];

function setup(variables: readonly EnvironmentVariable[] = VARIABLES, base?: readonly EnvironmentVariable[] | null) {
  const onSave = vi.fn<(v: EnvironmentVariable[]) => Promise<unknown>>().mockResolvedValue(undefined);
  render(<VariablesEditor label="Variables of Staging" variables={variables} onSave={onSave} readOnly={false} base={base} />);
  return onSave;
}

const jsonView = () => {
  const editor = document.querySelector(".cm-editor");
  if (editor === null) throw new Error("no JSON editor");
  const view = EditorView.findFromDOM(editor as HTMLElement);
  if (view === null) throw new Error("no view");
  return view;
};

const replaceJson = (text: string) =>
  act(() => {
    const view = jsonView();
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, userEvent: "input.type" });
  });

const modes = () => screen.getByRole("group", { name: "Show Variables of Staging as" });

afterEach(() => {
  vi.useRealTimers();
});

describe("VariablesEditor raw JSON (ENV-07)", () => {
  it("shows the variables as JSON without secret values, and round-trips with the table", async () => {
    vi.useFakeTimers();
    const onSave = setup();
    fireEvent.click(within(modes()).getByRole("button", { name: "JSON" }));
    const text = jsonView().state.doc.toString();
    expect(text).toContain('{ "name": "baseUrl", "value": "https://api.stg.test" }');
    expect(text).toContain(`{ "name": "token", "value": "${SECRET_MASK}", "secret": true }`);
    expect(text).toContain('{ "name": "user", "value": "team", "localValue": "me" }');
    expect(text).not.toContain("stg-s3cret");
    expect(document.body.textContent).not.toContain("stg-s3cret");
    expect(auditA11y()).toEqual([]);

    // Back to the table and to JSON again: the same text, nothing saved.
    fireEvent.click(within(modes()).getByRole("button", { name: "Table" }));
    fireEvent.click(within(modes()).getByRole("button", { name: "JSON" }));
    expect(jsonView().state.doc.toString()).toBe(text);
    await act(() => vi.advanceTimersByTimeAsync(SAVE_DELAY_MS * 2));
    expect(onSave).not.toHaveBeenCalled();

    // An edit that parses is saved: the mask keeps the secret value.
    replaceJson(text.replace("https://api.stg.test", "https://api.prod.test"));
    await act(() => vi.advanceTimersByTimeAsync(SAVE_DELAY_MS));
    expect(onSave).toHaveBeenCalledTimes(1);
    expect(onSave.mock.calls[0][0]).toEqual([
      variable("baseUrl", { value: "https://api.prod.test" }),
      variable("token", { value: "stg-s3cret", secret: true }),
      variable("user", { value: "team", localValue: "me" }),
    ]);

    // The table shows the edit.
    fireEvent.click(within(modes()).getByRole("button", { name: "Table" }));
    expect(screen.getByDisplayValue("https://api.prod.test")).toBeTruthy();
  });

  it("refuses text it cannot use, and keeps the table until it is fixed", async () => {
    vi.useFakeTimers();
    const onSave = setup();
    fireEvent.click(within(modes()).getByRole("button", { name: "JSON" }));
    const text = jsonView().state.doc.toString();

    replaceJson("[{");
    expect(screen.getByRole("status").textContent).toMatch(/^Not valid JSON/);
    expect(within(modes()).getByRole("button", { name: "Table" }).hasAttribute("disabled")).toBe(true);

    // The mask made into a plain value is refused, never saved as the value.
    replaceJson(text.replace(`"secret": true`, `"secret": false`));
    expect(screen.getByRole("status").textContent).toContain("is the mask of a secret value");
    await act(() => vi.advanceTimersByTimeAsync(SAVE_DELAY_MS * 2));
    expect(onSave).not.toHaveBeenCalled();

    replaceJson(text);
    expect(screen.queryByRole("status")).toBeNull();
    expect(within(modes()).getByRole("button", { name: "Table" }).hasAttribute("disabled")).toBe(false);
  });
});

describe("VariablesEditor base environment (ENV-05)", () => {
  const base = [
    variable("baseUrl", { value: "https://api.base.test" }),
    variable("timeout", { value: "30" }),
    variable("apiKey", { value: "base-k3y", secret: true }),
    variable("noValue", { value: "", secret: true }),
    variable("off", { value: "x", enabled: false }),
    variable("region", { value: "eu", localValue: "local-eu" }),
  ];

  it("lists what the environment inherits and overrides, and overrides a base variable", async () => {
    vi.useFakeTimers();
    const onSave = setup([variable("baseUrl", { value: "https://api.stg.test" })], base);
    const list = screen.getByRole("group", { name: "Inherited from the base environment (Global variables)" });
    const rows = within(list).getAllByRole("listitem");
    // Disabled base variables and secrets without a value are not inherited.
    expect(rows.map((r) => r.firstChild?.textContent)).toEqual(["baseUrl", "timeout", "apiKey", "region"]);
    expect(rows[0].textContent).toContain("Overridden here");
    // A secret base value is masked; a local value is what is sent.
    expect(rows[2].textContent).toContain(SECRET_MASK);
    expect(list.textContent).not.toContain("base-k3y");
    expect(rows[3].textContent).toContain("local-eu");
    expect(auditA11y()).toEqual([]);

    fireEvent.click(within(list).getByRole("button", { name: "Override timeout in this environment" }));
    fireEvent.click(within(list).getByRole("button", { name: "Override apiKey in this environment" }));
    await act(() => vi.advanceTimersByTimeAsync(SAVE_DELAY_MS));
    expect(onSave).toHaveBeenLastCalledWith([
      variable("baseUrl", { value: "https://api.stg.test" }),
      variable("timeout", { value: "30" }),
      // A secret is overridden without copying the base's value.
      variable("apiKey", { value: "", secret: true }),
    ]);
    const after = within(screen.getByRole("group", { name: "Inherited from the base environment (Global variables)" }));
    expect(after.getAllByText("Overridden here")).toHaveLength(2);
  });

  it("shows no inherited list for the global variables themselves", () => {
    setup(base);
    expect(screen.queryByRole("group", { name: /Inherited from the base environment/ })).toBeNull();
  });
});
