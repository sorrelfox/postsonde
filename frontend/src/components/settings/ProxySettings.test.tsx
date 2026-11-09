import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Proxy: vi.fn<() => Promise<unknown>>(),
  SetProxy: vi.fn<(input: unknown) => Promise<unknown>>(),
  TestProxy: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: service,
}));

import { setSettingsPolicy } from "../../commands/settings";
import { NO_POLICY } from "../../lib/settings";
import { emit } from "../../test/fakeWails";
import { ProxyRows } from "./ProxySettings";

const direct = {
  mode: "env",
  url: "",
  hasPassword: false,
  envVar: "",
  envUrl: "",
  effective: "",
  remoteDns: false,
  error: null,
};

const manual = {
  ...direct,
  mode: "manual",
  url: "socks5h://alice@proxy.lan:1080",
  hasPassword: true,
  effective: "socks5h://alice@proxy.lan:1080",
  remoteDns: true,
};

beforeEach(() => {
  service.Proxy.mockReset().mockResolvedValue(direct);
  service.SetProxy.mockReset();
  service.TestProxy.mockReset();
});

afterEach(() => {
  setSettingsPolicy(NO_POLICY);
});

describe("ProxyRows", () => {
  it("says where connections go for each mode", async () => {
    render(<ProxyRows />);
    expect(await screen.findByText("The ALL_PROXY environment variable is not set, so connections go directly to their servers.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "From ALL_PROXY" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.queryByLabelText("Proxy URL")).toBeNull();

    act(() => {
      service.Proxy.mockResolvedValue({
        ...direct,
        envVar: "ALL_PROXY",
        envUrl: "socks5://env.lan:1080",
        effective: "socks5://env.lan:1080",
      });
      emit("settings:changed", { settings: {}, problems: [], source: "file" });
    });
    expect(
      await screen.findByText("Connections go through socks5://env.lan:1080, set in the ALL_PROXY environment variable."),
    ).toBeTruthy();
    expect(screen.getByText("Host names are resolved on this computer (socks5). Use socks5h:// to let the proxy resolve them.")).toBeTruthy();
    expect(screen.getByText("localhost and loopback addresses are always connected to directly.")).toBeTruthy();

    // An HTTP proxy (NET-02) resolves names itself.
    act(() => {
      service.Proxy.mockResolvedValue({
        ...direct,
        envVar: "ALL_PROXY",
        envUrl: "http://env.lan:3128",
        effective: "http://env.lan:3128",
        remoteDns: true,
      });
      emit("settings:changed", { settings: {}, problems: [], source: "file" });
    });
    expect(
      await screen.findByText("Connections go through http://env.lan:3128, set in the ALL_PROXY environment variable."),
    ).toBeTruthy();
    expect(screen.getByText(/^Host names are resolved by the HTTP proxy/)).toBeTruthy();

    act(() => {
      service.Proxy.mockResolvedValue({ ...direct, mode: "off", envVar: "ALL_PROXY", envUrl: "socks5://env.lan:1080" });
      emit("settings:changed", { settings: {}, problems: [], source: "file" });
    });
    expect(
      await screen.findByText(
        "Connections go directly to their servers; the proxy in the ALL_PROXY environment variable is not used.",
      ),
    ).toBeTruthy();

    act(() => {
      service.Proxy.mockResolvedValue({
        ...direct,
        envVar: "ALL_PROXY",
        error: {
          kind: "invalid_argument",
          code: "netx.proxy_invalid_url",
          message: "The proxy in the environment variable ALL_PROXY cannot be used.",
          hint: "",
          details: {},
          causes: [],
        },
      });
      emit("settings:changed", { settings: {}, problems: [], source: "file" });
    });
    expect(await screen.findByText("Connections fail until the proxy is fixed: they never go directly instead.")).toBeTruthy();
    expect(screen.getByRole("note").textContent).toContain("ALL_PROXY cannot be used");
  });

  it("switches to a manual proxy and saves the URL on Enter", async () => {
    render(<ProxyRows />);
    service.SetProxy.mockResolvedValueOnce({ ...manual, url: "", hasPassword: false, effective: "", error: { kind: "invalid_argument", code: "netx.proxy_invalid_url", message: "A manual proxy is chosen, but no proxy URL is set.", hint: "", details: {}, causes: [] } });
    fireEvent.click(await screen.findByRole("button", { name: "Manual" }));
    await waitFor(() => expect(service.SetProxy).toHaveBeenCalledWith({ mode: "manual", url: "", password: null }));
    const url = await screen.findByLabelText("Proxy URL");

    service.SetProxy.mockResolvedValueOnce(manual);
    fireEvent.change(url, { target: { value: "socks5h://alice:pw@proxy.lan" } });
    fireEvent.keyDown(url, { key: "Enter" });
    await waitFor(() =>
      expect(service.SetProxy).toHaveBeenLastCalledWith({ mode: "manual", url: "socks5h://alice:pw@proxy.lan", password: null }),
    );
    // Go keeps the password apart; the field shows the URL without it.
    await waitFor(() => expect((screen.getByLabelText("Proxy URL") as HTMLInputElement).value).toBe("socks5h://alice@proxy.lan:1080"));
    expect(screen.getByText("Connections go through socks5h://alice@proxy.lan:1080.")).toBeTruthy();
    expect(screen.getByText("Host names are resolved by the proxy (socks5h), so nothing is looked up on this computer.")).toBeTruthy();
    expect((screen.getByLabelText("Proxy password") as HTMLInputElement).placeholder).toBe("Stored");
  });

  it("shows Go's reason under the field it is about", async () => {
    service.Proxy.mockResolvedValue(manual);
    render(<ProxyRows />);
    const url = await screen.findByLabelText("Proxy URL");
    service.SetProxy.mockRejectedValueOnce({
      kind: "invalid_argument",
      code: "netx.proxy_invalid_url",
      message: "The proxy URL has no scheme; start it with socks5h://, socks5://, socks4:// or socks4a://.",
      hint: "",
      details: { field: "url" },
      causes: [],
    });
    fireEvent.change(url, { target: { value: "proxy.lan:1080" } });
    fireEvent.blur(url);
    expect((await screen.findByRole("alert")).textContent).toContain("has no scheme");
    expect(screen.getByLabelText("Proxy URL").getAttribute("aria-invalid")).toBe("true");
    expect(screen.getByLabelText("Proxy password").getAttribute("aria-invalid")).toBeNull();
  });

  it("stores and removes the password without showing it", async () => {
    service.Proxy.mockResolvedValue({ ...manual, hasPassword: false });
    render(<ProxyRows />);
    const password = (await screen.findByLabelText("Proxy password")) as HTMLInputElement;
    expect(password.type).toBe("password");
    expect(password.placeholder).toBe("None");
    expect(screen.queryByRole("button", { name: "Remove password" })).toBeNull();

    service.SetProxy.mockResolvedValueOnce(manual);
    fireEvent.change(password, { target: { value: "s3cret" } });
    fireEvent.keyDown(password, { key: "Enter" });
    await waitFor(() =>
      expect(service.SetProxy).toHaveBeenCalledWith({ mode: "manual", url: "socks5h://alice@proxy.lan:1080", password: "s3cret" }),
    );
    await waitFor(() => expect(password.value).toBe(""));
    expect(password.placeholder).toBe("Stored");

    service.SetProxy.mockResolvedValueOnce({ ...manual, hasPassword: false });
    fireEvent.click(screen.getByRole("button", { name: "Remove password" }));
    await waitFor(() =>
      expect(service.SetProxy).toHaveBeenLastCalledWith({ mode: "manual", url: "socks5h://alice@proxy.lan:1080", password: "" }),
    );
    await waitFor(() => expect(screen.queryByRole("button", { name: "Remove password" })).toBeNull());
  });

  it("tests the proxy and shows the hop, or where it failed (SOCKS-11)", async () => {
    service.Proxy.mockResolvedValue(manual);
    render(<ProxyRows />);
    const target = (await screen.findByLabelText("Host and port to connect to")) as HTMLInputElement;
    expect(target.value).toBe("example.com:443");
    const hop = {
      scheme: "socks5h",
      version: "SOCKS5",
      proxy: "proxy.lan:1080",
      proxyAddr: "10.0.0.1:1080",
      auth: "username/password",
      remoteDns: true,
      reply: 0,
      replyText: "succeeded",
      lookupMs: 0,
      connectMs: 1.5,
      tlsMs: 0,
      handshakeMs: 0.75,
    };
    service.TestProxy.mockResolvedValueOnce({
      ok: true,
      proxy: "socks5h://alice@proxy.lan:1080",
      target: "api.test:443",
      durationMs: 2.5,
      step: "",
      hop,
      error: null,
    });
    fireEvent.change(target, { target: { value: "api.test:443" } });
    fireEvent.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText("Connected to api.test:443 through socks5h://alice@proxy.lan:1080 in 2.5 ms.")).toBeTruthy();
    expect(service.TestProxy).toHaveBeenCalledWith({ target: "api.test:443" });
    const details = screen.getByLabelText("How the connection went");
    expect(details.textContent).toContain("proxy.lan:1080 (10.0.0.1:1080)");
    expect(details.textContent).toContain("User name and password");
    expect(details.textContent).toContain("Resolved by the proxy");
    expect(details.textContent).toContain("0 succeeded");
    expect(details.textContent).toContain("connect 1.5 ms, handshake 0.75 ms");

    service.TestProxy.mockResolvedValueOnce({
      ok: false,
      proxy: "socks5h://alice@proxy.lan:1080",
      target: "api.test:443",
      durationMs: 3,
      step: "auth",
      hop: { ...hop, reply: null, replyText: "" },
      error: {
        kind: "network",
        code: "netx.socks_auth_failed",
        message: "The SOCKS proxy at proxy.lan:1080 rejected the username or password.",
        hint: "Check the proxy username and password.",
        details: {},
        causes: [],
      },
    });
    fireEvent.keyDown(target, { key: "Enter" });
    expect(await screen.findByText("The test failed: the proxy did not accept the authentication.")).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("rejected the username or password");
    expect(screen.getByRole("status").textContent).toContain("No reply");
  });

  it("says where a SOCKS4 proxy resolves host names (SOCKS-06)", async () => {
    service.Proxy.mockResolvedValue({ ...manual, url: "socks4a://bob@legacy.lan:1080", effective: "socks4a://bob@legacy.lan:1080", hasPassword: false });
    render(<ProxyRows />);
    expect(await screen.findByText("Host names are resolved by the proxy (socks4a), so nothing is looked up on this computer.")).toBeTruthy();
    act(() => {
      service.Proxy.mockResolvedValue({ ...manual, url: "socks4://legacy.lan:1080", effective: "socks4://legacy.lan:1080", remoteDns: false });
      emit("settings:changed", { settings: {}, problems: [], source: "file" });
    });
    expect(
      await screen.findByText("Host names are resolved on this computer to IPv4 addresses (socks4). Use socks4a:// to let the proxy resolve them."),
    ).toBeTruthy();
  });

  it("offers the test only while connections go through a proxy", async () => {
    render(<ProxyRows />);
    await screen.findByText("The ALL_PROXY environment variable is not set, so connections go directly to their servers.");
    expect(screen.queryByRole("button", { name: "Test connection" })).toBeNull();
  });

  it("disables the rows the administrator locks", async () => {
    setSettingsPolicy({ active: true, path: "/etc/postsonde/policy.json", locked: ["network.proxyMode", "network.proxyUrl"], requireTlsVerification: false, problems: [] });
    service.Proxy.mockResolvedValue(manual);
    render(<ProxyRows />);
    const url = (await screen.findByLabelText(/Proxy URL/)) as HTMLInputElement;
    expect(url.matches(":disabled")).toBe(true);
    expect(screen.getByLabelText(/Proxy password/).matches(":disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "No proxy" }).matches(":disabled")).toBe(true);
    expect(screen.getAllByRole("img", { name: "Locked: set by your administrator" }).length).toBe(3);
  });
});
