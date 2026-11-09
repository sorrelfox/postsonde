import { CompletionContext, type CompletionResult } from "@codemirror/autocomplete";
import { EditorState } from "@codemirror/state";
import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  ResponseValue: vi.fn<(id: string, name: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));

import type { ResponseValueDTO } from "../../lib/templateResponse";
import { toVariableSet, type TemplateVariableSet } from "../../lib/templateVariables";
import { settle } from "../../test/fakeWails";
import {
  fillResponseTooltip,
  setTemplateVariables,
  templateCompletionSource,
  templateVariables,
  variableTooltipDom,
} from "./templateVariables";

const cancellable = <T,>(p: Promise<T>) => Object.assign(p, { cancel: vi.fn() });

const set: TemplateVariableSet = {
  ...toVariableSet({
    variables: [{ name: "$response", value: "A value from another request's last response", secret: false, resolved: true, scope: "Dynamic", source: "dynamic" }],
    environment: "",
  }),
  requestId: "req_me",
  requests: new Map([["req_login", { id: "req_login", name: "Log in", detail: "POST login.json" }]]),
};

const dto = (over: Partial<ResponseValueDTO>): ResponseValueDTO => ({
  name: "",
  requestId: "req_login",
  requestName: "Log in",
  resolved: true,
  value: "tok-1",
  secret: false,
  receivedAt: "2026-10-01T12:00:00Z",
  status: 200,
  fromHistory: false,
  send: "never",
  maxAgeMs: 0,
  sendFirst: false,
  problem: "",
  code: "",
  ...over,
});

const now = () => Date.parse("2026-10-01T12:00:30Z");

beforeEach(() => {
  http.ResponseValue.mockReset();
});

describe("the hover of a response reference (DYN-06)", () => {
  it("names the request, then shows the stored value Go reads", async () => {
    const name = "$response req_login jsonpath $.access_token";
    http.ResponseValue.mockReturnValue(cancellable(Promise.resolve(dto({ name }))));
    const dom = variableTooltipDom(set, name, () => undefined);
    expect(dom.textContent).toContain("JSONPath $.access_token of the last response of “Log in”");
    expect(dom.textContent).toContain("Response: Log in");
    fillResponseTooltip(dom, "req_me", name, now);
    expect(dom.textContent).toContain("Reading the last response…");
    await settle();
    expect(http.ResponseValue).toHaveBeenCalledWith("req_me", name);
    const line = dom.querySelector(".cm-templateVariableTooltip-response");
    expect(line?.textContent).toBe(
      "tok-1Last response of “Log in”: status 200, 30 s agoNever sent for this reference: the stored response is read.",
    );
    // Before the link to the resolution view.
    expect(line?.nextElementSibling?.className).toBe("cm-templateVariableTooltip-resolution");
  });

  it("masks a secret value and says why there is none", async () => {
    http.ResponseValue.mockReturnValue(cancellable(Promise.resolve(dto({ secret: true, value: "" }))));
    const dom = document.createElement("div");
    fillResponseTooltip(dom, "req_me", "$response req_login status", now);
    await settle();
    expect(dom.textContent).toContain("••••••");

    http.ResponseValue.mockReturnValue(
      cancellable(Promise.resolve(dto({ resolved: false, value: "", receivedAt: "", problem: 'The request "Log in" has no response yet.', send: "missing", sendFirst: true }))),
    );
    const empty = document.createElement("div");
    fillResponseTooltip(empty, "req_me", "$response req_login status send=missing", now);
    await settle();
    expect(empty.textContent).toBe('The request "Log in" has no response yet.“Log in” is sent first at the next send.');
  });

  it("cancels the call when the hover closes", () => {
    const p = cancellable(new Promise<ResponseValueDTO>(() => undefined));
    http.ResponseValue.mockReturnValue(p);
    const stop = fillResponseTooltip(document.createElement("div"), "req_me", "$response req_login status", now);
    stop();
    expect(p.cancel).toHaveBeenCalled();
  });
});

describe("the request picker", () => {
  it("lists the requests by name and inserts the ID", () => {
    const state = EditorState.create({ doc: "Bearer {{$response ", extensions: templateVariables }).update({
      effects: setTemplateVariables.of(set),
    }).state;
    const got = templateCompletionSource(new CompletionContext(state, state.doc.length, false)) as CompletionResult | null;
    expect(got?.from).toBe(state.doc.length);
    expect(got?.options.map((o) => [o.label, o.apply, o.detail, o.type])).toEqual([["Log in", "req_login ", "POST login.json", "keyword"]]);
  });
});
