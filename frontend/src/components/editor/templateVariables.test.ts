import { CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it } from "vitest";
import {
  hasResolutionHost,
  RESOLUTION_HOST_ATTRIBUTE,
  requestShowResolution,
  SHOW_RESOLUTION_EVENT,
  type ShowResolutionDetail,
} from "../../lib/resolution";
import { toVariableSet, type VariableDTO } from "../../lib/templateVariables";
import {
  setTemplateVariables,
  templateCompletionSource,
  templateVariables,
  templateVariablesOf,
  variableTooltipDom,
} from "./templateVariables";

const variable = (name: string, over: Partial<VariableDTO> = {}): VariableDTO => ({
  name,
  value: `${name}-value`,
  secret: false,
  resolved: true,
  scope: "Environment: dev",
  source: "environment",
  ...over,
});

const set = toVariableSet({
  variables: [variable("host"), variable("token", { value: "", secret: true }), variable("loop", { resolved: false, value: "{{nope}}" })],
  environment: "dev",
});

let view: EditorView | null = null;
afterEach(() => {
  view?.destroy();
  view = null;
});

function mount(doc: string): EditorView {
  view = new EditorView({ state: EditorState.create({ doc, extensions: templateVariables }), parent: document.body });
  return view;
}

const chips = (v: EditorView) =>
  [...v.contentDOM.querySelectorAll(".cm-templateVariable")].map(
    (el) => `${el.textContent}${el.hasAttribute("data-unresolved") ? " (red)" : ""}`,
  );

describe("templateVariables", () => {
  it("draws chips, and marks the unresolved ones once the variables are known", () => {
    const v = mount('{"a": "{{host}}",\n "b": "{{token}}", "c": "{{loop}}", "d": "{{ missing }}", "e": "\\{{no}}"}');
    expect(templateVariablesOf(v.state)).toBeNull();
    expect(chips(v)).toEqual(["{{host}}", "{{token}}", "{{loop}}", "{{ missing }}"]);

    v.dispatch({ effects: setTemplateVariables.of(set) });
    expect(templateVariablesOf(v.state)).toBe(set);
    expect(chips(v)).toEqual(["{{host}}", "{{token}}", "{{loop}} (red)", "{{ missing }} (red)"]);

    // Edits are followed.
    v.dispatch({ changes: { from: 0, insert: "{{other}} " } });
    expect(chips(v)[0]).toBe("{{other}} (red)");
  });

  it("links the hover to the resolution view where there is one (DBG-08)", () => {
    let shown = 0;
    const dom = variableTooltipDom(set, "missing", () => (shown += 1));
    const button = dom.querySelector("button");
    expect(button?.textContent).toBe("Where does it come from?");
    button?.click();
    expect(shown).toBe(1);
    expect(variableTooltipDom(set, "host").querySelector("button")).toBeNull();

    // A chip's hover asks the request editor around it, which listens for the event.
    const host = document.createElement("div");
    host.setAttribute(RESOLUTION_HOST_ATTRIBUTE, "");
    document.body.append(host);
    const asked: string[] = [];
    host.addEventListener(SHOW_RESOLUTION_EVENT, (e) => asked.push((e as CustomEvent<ShowResolutionDetail>).detail.name));
    view = new EditorView({ state: EditorState.create({ doc: "{{host}}", extensions: templateVariables }), parent: host });
    expect(hasResolutionHost(view.dom)).toBe(true);
    requestShowResolution(view.dom, "host");
    expect(asked).toEqual(["host"]);
    host.remove();
  });

  it("describes a variable on hover: value (masked when secret) and scope, or why it does not resolve", () => {
    const host = variableTooltipDom(set, "host");
    expect(host.textContent).toBe("{{host}}host-valueEnvironment: dev");
    const token = variableTooltipDom(set, "token");
    expect(token.textContent).toBe("{{token}}••••••Environment: dev · secret");
    expect(token.textContent).not.toContain("token-value");
    const missing = variableTooltipDom(set, "missing");
    expect(missing.dataset.unresolved).toBe("true");
    expect(missing.textContent).toBe("{{missing}}Not defined in dev or any other scope.");
    expect(variableTooltipDom(set, "loop").textContent).toContain("uses a variable that is not defined");
    const shadowing = toVariableSet({
      variables: [variable("base", { scope: "Collection: API", source: "collection", overrides: ["Global"] })],
      environment: "",
    });
    const base = variableTooltipDom(shadowing, "base");
    expect(base.querySelector(".cm-templateVariableTooltip-overrides")?.textContent).toBe("Overrides Global");
    expect(host.querySelector(".cm-templateVariableTooltip-overrides")).toBeNull();
  });

  it("says a value is local (SEC-02), and never shows a secret's value on hover (SEC-01)", () => {
    const vars = toVariableSet({
      variables: [
        variable("base", { value: "http://localhost:8080", local: true }),
        variable("key", { secret: true, value: "should-not-show" }),
      ],
      environment: "dev",
    });
    expect(variableTooltipDom(vars, "base").textContent).toBe("{{base}}http://localhost:8080Environment: dev · local value");
    for (const name of ["key", "key.field", "key | base64"]) {
      const dom = variableTooltipDom(vars, name);
      expect(dom.textContent).not.toContain("should-not-show");
      expect(dom.textContent).toContain("••••••");
    }
  });

  it("resolves dotted names from JSON values and leaves escaped ones undecorated (ENV-13, ENV-14)", () => {
    const json = toVariableSet({
      variables: [variable("auth", { value: '{"token":"t0k","user":{"name":"Ann"}}' })],
      environment: "dev",
    });
    const v = mount('{"t": "{{auth.token}}", "u": "{{ auth.user.name }}", "x": "{{auth.nope}}", "e": "\\{{auth.token}}"}');
    v.dispatch({ effects: setTemplateVariables.of(json) });
    expect(chips(v)).toEqual(["{{auth.token}}", "{{ auth.user.name }}", "{{auth.nope}} (red)"]);
    expect(variableTooltipDom(json, "auth.user.name").textContent).toBe("{{auth.user.name}}AnnEnvironment: dev");
    const nope = variableTooltipDom(json, "auth.nope");
    expect(nope.dataset.unresolved).toBe("true");
    expect(nope.textContent).toBe('{{auth.nope}}{{auth}} has no field "nope".');
  });

  it("completes names inside {{ and filters after | (DYN-04, DYN-05)", () => {
    const withDynamic = toVariableSet({
      variables: [
        variable("host"),
        variable("$randomFirstName", { scope: "Dynamic", source: "dynamic", value: "A random first name" }),
      ],
      environment: "dev",
    });
    const state = EditorState.create({ doc: "x {{ho y {{a | sh", extensions: templateVariables }).update({
      effects: setTemplateVariables.of(withDynamic),
    }).state;
    const at = (pos: number, explicit = false) =>
      templateCompletionSource(new CompletionContext(state, pos, explicit)) as CompletionResult | null;
    const names = at(6);
    expect(names?.from).toBe(4);
    expect(names?.options.map((o) => [o.label, o.detail, o.info])).toEqual([
      ["host", "Environment: dev", "host-value"],
      ["$randomFirstName", "dynamic", "A random first name"],
    ]);
    const filters = at(state.doc.length);
    expect(filters?.from).toBe(state.doc.length - 2);
    expect(filters?.options.some((o) => o.label === "sha256" && o.type === "function")).toBe(true);
    expect(at(1)).toBeNull();
  });

  it("shows a filter pipe's input scope on hover and marks unknown filters red", () => {
    const v = mount('{"a": "{{ host | base64 }}", "b": "{{host | nope}}", "c": "{{token | sha256}}"}');
    v.dispatch({ effects: setTemplateVariables.of(set) });
    expect(chips(v)).toEqual(["{{ host | base64 }}", "{{host | nope}} (red)", "{{token | sha256}}"]);
    expect(variableTooltipDom(set, "host | base64").textContent).toBe(
      "{{host | base64}}base64 of {{host}}, computed when sentEnvironment: dev",
    );
    expect(variableTooltipDom(set, "token | sha256").textContent).toBe(
      "{{token | sha256}}••••••Environment: dev · secret",
    );
  });

  it("has no chips state in an editor without the extension", () => {
    const plain = new EditorView({ state: EditorState.create({ doc: "{{a}}" }), parent: document.body });
    expect(templateVariablesOf(plain.state)).toBeUndefined();
    plain.destroy();
  });
});
