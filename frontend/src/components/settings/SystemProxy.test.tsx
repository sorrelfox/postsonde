import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
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

const system = {
  source: "macos",
  http: "http://web.proxy.test:3128",
  https: "",
  socks: "socks.proxy.test:1080",
  pac: "",
  autoDiscovery: false,
  bypass: [".local", "169.254.0.0/16"],
  skipped: [],
};

const systemProxy = {
  mode: "system",
  url: "",
  hasPassword: false,
  envVar: "",
  envUrl: "",
  effective: "socks5h://socks.proxy.test:1080",
  effectiveHttp: "http://web.proxy.test:3128",
  system,
  remoteDns: true,
  error: null,
  bypass: [],
  noProxyVar: "",
  noProxy: "",
};

beforeEach(() => {
  settingsService.Proxy.mockReset().mockResolvedValue(systemProxy);
  settingsService.SetProxy.mockReset();
  settingsService.TestProxy.mockReset();
  scopesService.Scopes.mockReset().mockResolvedValue({
    open: false,
    project: { mode: "inherit", url: "", hasPassword: false, bypass: [] },
    environments: [],
    effective: {
      proxy: "socks5h://socks.proxy.test:1080",
      httpProxy: "http://web.proxy.test:3128",
      httpSplit: true,
      level: "app",
      source: "system",
      remoteDns: true,
      bypass: [".local"],
      noProxyVar: "",
      error: null,
    },
    error: null,
  });
});

describe("the system proxy (NET-01)", () => {
  it("is the first mode and says which proxy each kind of connection uses", async () => {
    render(<ProxyRows />);
    expect(
      await screen.findByText(
        "From the macOS network settings: http:// requests go through http://web.proxy.test:3128, every other connection through socks5h://socks.proxy.test:1080.",
      ),
    ).toBeTruthy();
    const buttons = screen.getAllByRole("button").filter((b) => b.hasAttribute("aria-pressed"));
    expect(buttons.map((b) => b.textContent)).toEqual(["System", "From ALL_PROXY", "Manual", "No proxy"]);
    expect(screen.getByRole("button", { name: "System" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByText("The system's bypass list applies too: .local, 169.254.0.0/16")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Test connection" })).toBeTruthy();
    expectAccessible();
  });

  it("names a PAC file it cannot use, WPAD and ignored bypass entries", async () => {
    settingsService.Proxy.mockResolvedValue({
      ...systemProxy,
      effective: "",
      effectiveHttp: "",
      system: { ...system, source: "windows", http: "", socks: "", pac: "http://wpad/wpad.dat", autoDiscovery: true, skipped: ["*contoso*"] },
      error: {
        kind: "unsupported",
        code: "sysproxy.pac_unsupported",
        message: "The system proxy is set by an automatic configuration (PAC) file, which PostSonde cannot run.",
        hint: "",
        details: { pac: "http://wpad/wpad.dat", source: "system" },
        causes: [],
      },
    });
    render(<ProxyRows />);
    expect(await screen.findByText("Connections fail until the proxy is fixed: they never go directly instead.")).toBeTruthy();
    expect(screen.getByRole("note").textContent).toContain("PAC");
    expect(screen.getByText("Automatic proxy discovery (WPAD) is on in the system settings; PostSonde does not use it.")).toBeTruthy();
    expect(screen.getByText("Bypass entries PostSonde cannot use are ignored: *contoso*")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Test connection" })).toBeNull();
  });

  it("says when only http:// requests, or none, use a system proxy", async () => {
    settingsService.Proxy.mockResolvedValue({ ...systemProxy, effective: "", system: { ...system, socks: "" } });
    render(<ProxyRows />);
    expect(
      await screen.findByText("From the macOS network settings: http:// requests go through http://web.proxy.test:3128; every other connection goes directly."),
    ).toBeTruthy();
    act(() => {
      settingsService.Proxy.mockResolvedValue({ ...systemProxy, effective: "", effectiveHttp: "", system: { ...system, source: "gnome", http: "", socks: "" } });
      emit("settings:changed", { settings: {}, problems: [], source: "file" });
    });
    expect(await screen.findByText("The GNOME proxy settings set no proxy, so connections go directly to their servers.")).toBeTruthy();
  });

  it("switches to the system mode", async () => {
    settingsService.Proxy.mockResolvedValue({ ...systemProxy, mode: "off", system: null, effective: "", effectiveHttp: "" });
    render(<ProxyRows />);
    settingsService.SetProxy.mockResolvedValueOnce(systemProxy);
    fireEvent.click(await screen.findByRole("button", { name: "System" }));
    await waitFor(() => expect(settingsService.SetProxy).toHaveBeenCalledWith({ mode: "system", url: "", password: null }));
    expect(await screen.findByText(/every other connection through socks5h/)).toBeTruthy();
  });

  it("shows both system proxies as the proxy in effect, with one test", async () => {
    render(<ProxySection />);
    expect(
      await screen.findByText(
        "The system proxy settings are in effect: http:// requests go through http://web.proxy.test:3128, every other connection through socks5h://socks.proxy.test:1080.",
      ),
    ).toBeTruthy();
    expect(screen.getAllByRole("button", { name: "Test connection" })).toHaveLength(1);
  });
});
