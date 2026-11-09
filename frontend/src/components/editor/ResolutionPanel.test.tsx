import { act, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  Resolution: vi.fn<(id: string) => Promise<unknown>>(),
}));
const collection = vi.hoisted(() => ({
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: collection,
}));

import type { ResolutionDTO, ResolvedVariableDTO } from "../../lib/resolution";
import { ENVIRONMENTS_CHANGED_EVENT } from "../../lib/environments";
import { HISTORY_CHANGED_EVENT } from "../../lib/responseHistory";
import { createStores, StoreProvider } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { ResolutionPanel } from "./ResolutionPanel";

const cancellable = <T,>(p: Promise<T>) => Object.assign(p, { cancel: () => undefined });

const variable = (name: string, over: Partial<ResolvedVariableDTO> = {}): ResolvedVariableDTO => ({
  name,
  kind: "variable",
  resolved: true,
  value: `${name}-value`,
  secret: false,
  generated: false,
  scope: "Global",
  source: "global",
  count: 1,
  scopes: [{ scope: "Global", source: "global", value: `${name}-value`, secret: false, wins: true }],
  ...over,
});

const RESOLUTION: ResolutionDTO = {
  environment: "Dev",
  url: "https://api.dev.test/users/7",
  variables: [
    variable("baseUrl", {
      value: "https://api.dev.test",
      scope: "Environment: Dev",
      source: "environment",
      scopes: [
        { scope: "Global", source: "global", value: "https://api.prod.test", secret: false, wins: false },
        { scope: "Environment: Dev", source: "environment", value: "https://api.dev.test", secret: false, wins: true },
      ],
    }),
    variable("token", {
      value: "",
      secret: true,
      scope: "Runtime",
      source: "runtime",
      scopes: [
        { scope: "Environment: Dev", source: "environment", value: "", secret: true, wins: false },
        { scope: "Runtime", source: "runtime", value: "", secret: true, wins: true },
      ],
    }),
    variable("user", {
      value: "me",
      local: true,
      scopes: [{ scope: "Global", source: "global", value: "me", secret: false, local: true, wins: true }],
    }),
    variable("$uuid", { kind: "dynamic", value: "", generated: true, description: "A random UUID (v4)", scope: "Dynamic", source: "dynamic", scopes: [] }),
    variable("host", { value: "h.test", count: 0, via: ["base"] }),
    variable("nope", { resolved: false, value: "", scope: "", source: "", problem: "unresolved", scopes: [] }),
  ],
};

beforeEach(() => {
  resetFakeWails();
  http.Resolution.mockReset().mockImplementation(() => cancellable(Promise.resolve(RESOLUTION)));
  collection.Flush.mockReset().mockResolvedValue(undefined);
});

async function setup(focus: string | null = null) {
  const stores = createStores();
  const view = render(
    <StoreProvider stores={stores}>
      <ResolutionPanel id="req_1" request={{ url: "/users/7" }} focus={focus} />
    </StoreProvider>,
  );
  await act(settle);
  return view;
}

const row = (name: string) => {
  const table = screen.getByRole("table", { name: "Variables of this request" });
  const found = within(table)
    .getAllByRole("row")
    .find((r) => r.getAttribute("data-name") === name);
  if (found === undefined) throw new Error(`no row ${name}`);
  return found;
};

describe("ResolutionPanel (DBG-08)", () => {
  it("lists each variable with its value, the scope that wins and the scopes it overrides", async () => {
    await setup("token");
    expect(http.Resolution).toHaveBeenCalledWith("req_1");
    expect(screen.getByText("Resolved as the next send resolves them, with the environment Dev.")).toBeTruthy();
    expect(screen.getByText("https://api.dev.test/users/7")).toBeTruthy();

    const base = row("baseUrl");
    expect(within(base).getAllByRole("cell")[1].textContent).toBe("Environment: Dev");
    const scopes = within(base).getByRole("list", { name: "Scopes that define baseUrl" });
    const items = within(scopes).getAllByRole("listitem");
    expect(items.map((li) => li.textContent)).toEqual([
      "Globalhttps://api.prod.testoverridden",
      "Environment: Devhttps://api.dev.testsent",
    ]);
    expect(items[1].hasAttribute("data-wins")).toBe(true);

    // Secrets are masked everywhere; the focused row is marked.
    const token = row("token");
    expect(token.textContent).toContain("••••••");
    expect(token.hasAttribute("data-focused")).toBe(true);
    // A local value says so.
    expect(row("user").textContent).toContain("Global · local value");
    // Dynamic values are marked as generated when sent.
    expect(row("$uuid").textContent).toContain("dynamic value");
    expect(row("$uuid").textContent).toContain("Generated when sent: A random UUID (v4)");
    // A name used only inside another's value says through which.
    expect(row("host").textContent).toContain("in {{base}}");
    // An undefined name says so.
    expect(row("nope").textContent).toContain("Not defined.");
    expect(row("nope").hasAttribute("data-unresolved")).toBe(true);

    expectAccessible();
  });

  it("asks again when the environments change", async () => {
    await setup();
    expect(http.Resolution).toHaveBeenCalledTimes(1);
    await act(async () => {
      emit(ENVIRONMENTS_CHANGED_EVENT, { activeId: "env_2" });
      await settle();
    });
    await act(settle);
    expect(http.Resolution).toHaveBeenCalledTimes(2);
  });

  it("lists a response reference with its request and when it is sent first, and asks again after a send (DYN-06)", async () => {
    http.Resolution.mockImplementation(() =>
      cancellable(
        Promise.resolve({
          environment: "",
          url: "https://h.test/",
          variables: [
            variable("$response req_login jsonpath $.token send=10m", {
              kind: "response",
              value: "tok-1",
              scope: "Response: Log in",
              source: "response",
              scopes: [],
            }),
            variable("$response req_other status send=missing", {
              kind: "response",
              resolved: false,
              value: "",
              scope: "",
              source: "",
              problem: "response",
              reason: 'The request "Other" has no response yet; it is sent first when this request is sent.',
              scopes: [],
            }),
          ],
        }),
      ),
    );
    await setup();
    const token = row("$response req_login jsonpath $.token send=10m");
    expect(token.textContent).toContain("another request's response");
    expect(token.textContent).toContain("tok-1");
    expect(token.textContent).toContain("Response: Log in");
    expect(token.textContent).toContain("Sent first when it has no response, or it is older than 10 min.");
    const other = row("$response req_other status send=missing");
    expect(other.textContent).toContain('No value yet. The request "Other" has no response yet');
    expect(other.textContent).toContain("Sent first when it has no response yet.");

    expect(http.Resolution).toHaveBeenCalledTimes(1);
    await act(async () => {
      emit(HISTORY_CHANGED_EVENT, { action: "added", id: "h_1", entry: null });
      await settle();
    });
    await act(settle);
    expect(http.Resolution).toHaveBeenCalledTimes(2);
  });

  it("shows why the request cannot be resolved", async () => {
    http.Resolution.mockImplementation(() =>
      cancellable(Promise.reject({ kind: "invalid_argument", code: "template.cycle", message: "The variable {{loop}} refers to itself." })),
    );
    await setup();
    expect(screen.getByRole("alert").textContent).toContain("refers to itself");
  });

  it("says when the request uses no variables", async () => {
    http.Resolution.mockImplementation(() => cancellable(Promise.resolve({ environment: "", url: "https://h.test/", variables: [] })));
    await setup();
    expect(screen.getByText("This request uses no variables.")).toBeTruthy();
    expect(screen.getByText("Resolved as the next send resolves them. No environment is active.")).toBeTruthy();
  });
});
