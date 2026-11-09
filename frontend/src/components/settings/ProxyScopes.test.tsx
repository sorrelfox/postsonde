import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const settingsService = vi.hoisted(() => ({
  Proxy: vi.fn<() => Promise<unknown>>(),
  SetProxy: vi.fn<(input: unknown) => Promise<unknown>>(),
  SetProxyBypass: vi.fn<(entries: string[]) => Promise<unknown>>(),
  TestProxy: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const scopesService = vi.hoisted(() => ({
  Scopes: vi.fn<() => Promise<unknown>>(),
  SetScope: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: settingsService,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/proxyscopes", () => ({
  ProxyScopesService: scopesService,
}));

import { expectAccessible } from "../../test/a11y";
import { emit } from "../../test/fakeWails";
import { ProxyRows } from "./ProxySettings";
import { ProxySection } from "./ProxyScopes";

const appProxy = {
  mode: "off",
  url: "",
  hasPassword: false,
  envVar: "",
  envUrl: "",
  effective: "",
  remoteDns: false,
  error: null,
  bypass: [],
  noProxyVar: "",
  noProxy: "",
};

const inherit = { mode: "inherit", url: "", hasPassword: false, bypass: [] };

const scopes = {
  open: true,
  project: inherit,
  environments: [
    { id: "env_dev", name: "Dev", active: false, scope: inherit },
    { id: "env_stg", name: "Staging", active: true, scope: inherit },
  ],
  effective: { proxy: "", level: "app", source: "", remoteDns: false, bypass: [], noProxyVar: "", error: null },
  error: null,
};

const withProject = {
  ...scopes,
  project: { mode: "manual", url: "http://bob@proxy.corp:3128", hasPassword: true, bypass: ["*.corp"] },
  effective: {
    proxy: "http://bob@proxy.corp:3128",
    level: "project",
    source: "project",
    remoteDns: true,
    bypass: [".corp"],
    noProxyVar: "",
    error: null,
  },
};

beforeEach(() => {
  settingsService.Proxy.mockReset().mockResolvedValue(appProxy);
  settingsService.SetProxy.mockReset();
  settingsService.SetProxyBypass.mockReset();
  settingsService.TestProxy.mockReset();
  scopesService.Scopes.mockReset().mockResolvedValue(scopes);
  scopesService.SetScope.mockReset();
});

/** The group of mode buttons of the scope labelled `label`. */
const modes = (label: string) => screen.getByRole("group", { name: label });

describe("ProxySection (SOCKS-08)", () => {
  it("says which scope's proxy is in effect and offers the test only with one", async () => {
    render(<ProxySection />);
    expect(await screen.findByText("No proxy: connections go directly to their servers.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Test connection" })).toBeNull();
    expect(within(modes("This project")).getByRole("button", { name: "Use app setting" }).getAttribute("aria-pressed")).toBe("true");
    expect(within(modes("Staging (active)")).getByRole("button", { name: "Use project setting" })).toBeTruthy();
    expect(modes("Dev")).toBeTruthy();

    act(() => {
      scopesService.Scopes.mockResolvedValue(withProject);
      emit("environments:changed", { activeId: "env_stg" });
    });
    expect(await screen.findByText("Connections go through http://bob@proxy.corp:3128, this project's proxy.")).toBeTruthy();
    expect(screen.getByText("Connected to directly: .corp, and localhost.")).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "Test connection" })).toHaveLength(1);

    act(() => {
      scopesService.Scopes.mockResolvedValue({
        ...withProject,
        environments: [scopes.environments[0], { ...scopes.environments[1], scope: { ...inherit, mode: "off" } }],
        effective: { ...scopes.effective, level: "environment", source: "environment" },
      });
      emit("proxyscopes:changed", { level: "environment", environmentId: "env_stg" });
    });
    expect(await screen.findByText("Connections go directly to their servers: the active environment, Staging, is set to No proxy.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Test connection" })).toBeNull();
    expectAccessible();
  });

  it("gives the project a proxy of its own, its password kept in Go", async () => {
    render(<ProxySection />);
    const group = await waitFor(() => modes("This project"));
    scopesService.SetScope.mockResolvedValueOnce({ ...scopes, project: { ...inherit, mode: "manual" } });
    fireEvent.click(within(group).getByRole("button", { name: "Manual" }));
    await waitFor(() =>
      expect(scopesService.SetScope).toHaveBeenCalledWith({ level: "project", environmentId: "", mode: "manual", url: "", password: null, bypass: [] }),
    );
    const url = await screen.findByLabelText("Proxy URL");

    scopesService.SetScope.mockResolvedValueOnce(withProject);
    fireEvent.change(url, { target: { value: "http://bob:pw@proxy.corp:3128" } });
    fireEvent.keyDown(url, { key: "Enter" });
    await waitFor(() =>
      expect(scopesService.SetScope).toHaveBeenLastCalledWith({
        level: "project",
        environmentId: "",
        mode: "manual",
        url: "http://bob:pw@proxy.corp:3128",
        password: null,
        bypass: [],
      }),
    );
    // Go keeps the password apart; the field shows the URL without it.
    await waitFor(() => expect((screen.getByLabelText("Proxy URL") as HTMLInputElement).value).toBe("http://bob@proxy.corp:3128"));
    const password = screen.getByLabelText("Proxy password") as HTMLInputElement;
    expect(password.type).toBe("password");
    expect(password.placeholder).toBe("Stored");

    // The scope's bypass list, split as Go splits NO_PROXY.
    const bypass = screen.getByLabelText("Also bypass") as HTMLTextAreaElement;
    expect(bypass.value).toBe("*.corp");
    scopesService.SetScope.mockResolvedValueOnce(withProject);
    fireEvent.focus(bypass);
    fireEvent.change(bypass, { target: { value: "*.corp\n10.0.0.0/8, [::1]:8080" } });
    fireEvent.blur(bypass);
    await waitFor(() =>
      expect(scopesService.SetScope).toHaveBeenLastCalledWith(
        expect.objectContaining({ level: "project", bypass: ["*.corp", "10.0.0.0/8", "[::1]:8080"] }),
      ),
    );
    expectAccessible();
  });

  it("shows Go's reason under the scope field it is about", async () => {
    scopesService.Scopes.mockResolvedValue({
      ...scopes,
      environments: [scopes.environments[0], { ...scopes.environments[1], scope: { mode: "manual", url: "socks5h://s.lan:1080", hasPassword: false, bypass: [] } }],
    });
    render(<ProxySection />);
    const bypass = (await screen.findByLabelText("Also bypass")) as HTMLTextAreaElement;
    scopesService.SetScope.mockRejectedValueOnce({
      kind: "invalid_argument",
      code: "netx.proxy_invalid_bypass",
      message: 'The bypass list entry "a..b" cannot be used: the host name has an empty label (two dots in a row).',
      hint: "",
      details: { field: "bypass", entry: "a..b" },
      causes: [],
    });
    fireEvent.change(bypass, { target: { value: "a..b" } });
    fireEvent.blur(bypass);
    expect((await screen.findByRole("alert")).textContent).toContain("empty label");
    expect(bypass.getAttribute("aria-invalid")).toBe("true");
    expect(scopesService.SetScope).toHaveBeenCalledWith(expect.objectContaining({ level: "environment", environmentId: "env_stg", bypass: ["a..b"] }));
    expect(screen.getByLabelText("Proxy URL").getAttribute("aria-invalid")).toBeNull();
  });

  it("asks for a project when none is open", async () => {
    scopesService.Scopes.mockResolvedValue({ ...scopes, open: false, environments: [] });
    render(<ProxySection />);
    expect(await screen.findByText("Open a project to give it, or each of its environments, a proxy of its own on this computer.")).toBeTruthy();
    expect(screen.queryByRole("group", { name: "This project" })).toBeNull();
  });
});

describe("the bypass list (SOCKS-09)", () => {
  it("saves the app's list on blur and shows NO_PROXY with ALL_PROXY", async () => {
    settingsService.Proxy.mockResolvedValue({ ...appProxy, mode: "env", noProxyVar: "NO_PROXY", noProxy: ".lan,10.0.0.0/8" });
    render(<ProxyRows />);
    expect(await screen.findByText("With the ALL_PROXY proxy, the entries of NO_PROXY apply too: .lan,10.0.0.0/8")).toBeTruthy();
    const list = screen.getByLabelText("Bypass list") as HTMLTextAreaElement;
    settingsService.SetProxyBypass.mockResolvedValueOnce({ ...appProxy, mode: "env", bypass: ["*.corp", "<local>"] });
    fireEvent.focus(list);
    fireEvent.change(list, { target: { value: " *.corp ;<local>\n" } });
    fireEvent.blur(list);
    await waitFor(() => expect(settingsService.SetProxyBypass).toHaveBeenCalledWith(["*.corp", "<local>"]));
    await waitFor(() => expect(list.value).toBe("*.corp\n<local>"));

    // Unchanged: nothing is saved.
    fireEvent.blur(list);
    expect(settingsService.SetProxyBypass).toHaveBeenCalledTimes(1);

    settingsService.SetProxyBypass.mockRejectedValueOnce({
      kind: "invalid_argument",
      code: "netx.proxy_invalid_bypass",
      message: 'The bypass list entry "10.0.0.0/33" cannot be used: it is not a CIDR range such as 10.0.0.0/8 or fd00::/8.',
      hint: "",
      details: { field: "bypass", entry: "10.0.0.0/33" },
      causes: [],
    });
    fireEvent.change(list, { target: { value: "10.0.0.0/33" } });
    fireEvent.blur(list);
    expect((await screen.findByRole("alert")).textContent).toContain("not a CIDR range");
    expect(list.getAttribute("aria-invalid")).toBe("true");
  });

  it("says when a tested target would bypass the proxy", async () => {
    settingsService.Proxy.mockResolvedValue({ ...appProxy, mode: "manual", url: "socks5h://p.lan:1080", effective: "socks5h://p.lan:1080", remoteDns: true });
    settingsService.TestProxy.mockResolvedValueOnce({
      ok: true,
      proxy: "socks5h://p.lan:1080",
      target: "git.corp:443",
      durationMs: 4,
      step: "",
      hop: null,
      error: null,
      bypassed: true,
    });
    render(<ProxyRows />);
    fireEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    expect(
      await screen.findByText(
        "Other connections to git.corp:443 do not use the proxy: it is on the bypass list or on this computer. The test went through the proxy anyway.",
      ),
    ).toBeTruthy();
  });
});
