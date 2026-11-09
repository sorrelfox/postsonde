import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const scopesService = vi.hoisted(() => ({
  Scopes: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/proxyscopes", () => ({
  ProxyScopesService: scopesService,
}));

import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale } from "../../i18n";
import type { RequestSettingsDTO } from "../../lib/collection";
import type { RequestProxyDTO } from "../../lib/requestProxy";
import { expectAccessible } from "../../test/a11y";
import { emit, listenerCount, resetFakeWails, settle } from "../../test/fakeWails";
import { englishLeftovers } from "../../test/pseudoLocale";
import { RequestProxyFold, RequestProxySettings } from "./RequestProxySettings";

const baseSettings: RequestSettingsDTO = {
  timeoutMs: null,
  followRedirects: null,
  maxRedirects: null,
  followOriginalMethod: null,
  forwardAuthorization: null,
  verifyTls: null,
  connectTimeoutMs: null,
  urlEncoding: "",
  httpVersion: "",
  sendCookies: null,
  storeCookies: null,
  tls: null,
  deadlineMs: null,
  extra: '{"x-kept":true}',
};

const proxy = (over: Partial<RequestProxyDTO> = {}): RequestProxyDTO => ({
  mode: "custom",
  url: "",
  password: "",
  localSecrets: [],
  extra: "",
  ...over,
});

const effective = { proxy: "", httpProxy: "", httpSplit: false, level: "app", source: "", remoteDns: false, bypass: [], noProxyVar: "", error: null };
const scopes = (eff: Partial<typeof effective> = {}, environments: unknown[] = []) => ({
  open: true,
  project: { mode: "inherit", url: "", hasPassword: false, bypass: [] },
  environments,
  effective: { ...effective, ...eff },
  error: null,
});

/** Every settings object the section saved, in order. */
let saved: RequestSettingsDTO[] = [];

/** The section over settings kept in state, as the editors keep their draft. */
function Harness({ initial, udp, readOnly = false, locked }: { initial: RequestSettingsDTO; udp?: boolean; readOnly?: boolean; locked?: string }) {
  const [settings, setSettings] = useState(initial);
  return (
    <RequestProxySettings
      settings={settings}
      readOnly={readOnly}
      udp={udp}
      locked={locked}
      edit={(patch) => {
        const next = patch.settings as RequestSettingsDTO;
        saved.push(next);
        setSettings(next);
      }}
    />
  );
}

const last = () => saved[saved.length - 1];
const lastProxy = () => last()?.proxy ?? null;
const mode = (name: string) => within(screen.getByRole("group", { name: "Proxy of this request" })).getByRole("button", { name });
const field = (name: string) => screen.getByLabelText(name) as HTMLInputElement;

async function show(proxyDTO: RequestProxyDTO | null, opts: { udp?: boolean; readOnly?: boolean; locked?: string } = {}) {
  const view = render(<Harness initial={{ ...baseSettings, proxy: proxyDTO }} {...opts} />);
  await act(settle);
  return view;
}

beforeEach(() => {
  resetFakeWails();
  saved = [];
  scopesService.Scopes.mockReset().mockResolvedValue(scopes());
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("RequestProxySettings", () => {
  it("inherits by default and says what that means now", async () => {
    const view = await show(null);
    expect(screen.getByRole("region", { name: "Proxy" }).dataset.proxyMode).toBe("inherit");
    expect(mode("Inherit").getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByText("Connects directly: the app has no proxy.")).toBeTruthy();

    act(() => {
      scopesService.Scopes.mockResolvedValue(scopes({ proxy: "socks5h://proxy.lan:1080", level: "app", source: "settings" }));
      emit("settings:changed", {});
    });
    expect(await screen.findByText("Uses the app's proxy: socks5h://proxy.lan:1080.")).toBeTruthy();

    act(() => {
      scopesService.Scopes.mockResolvedValue(scopes({ proxy: "http://proxy.corp:3128", level: "project" }));
      emit("proxyscopes:changed", {});
    });
    expect(await screen.findByText("Uses this project's proxy: http://proxy.corp:3128.")).toBeTruthy();

    act(() => {
      scopesService.Scopes.mockResolvedValue(
        scopes({ proxy: "socks5://10.0.0.1:1080", level: "environment" }, [{ id: "env_stg", name: "Staging", active: true, scope: {} }]),
      );
      emit("environments:changed", {});
    });
    expect(await screen.findByText("Uses the proxy of the active environment, Staging: socks5://10.0.0.1:1080.")).toBeTruthy();

    act(() => {
      scopesService.Scopes.mockResolvedValue(scopes({ level: "project" }));
      emit("workspace:changed", {});
    });
    expect(await screen.findByText("Connects directly: this project is set to No proxy.")).toBeTruthy();
    expect(saved).toEqual([]);
    expectAccessible();

    view.unmount();
    expect(listenerCount()).toBe(0);
  });

  it("falls back to the rule when the proxy in effect cannot be read", async () => {
    scopesService.Scopes.mockRejectedValue(new Error("no backend"));
    await show(null);
    expect(screen.getByText("Uses the proxy in effect: the active environment's, else the project's, else the app's.")).toBeTruthy();
  });

  it("says a failing proxy in effect fails the request", async () => {
    scopesService.Scopes.mockResolvedValue(scopes({ error: { code: "netx.proxy_invalid_url", message: "bad" } as never }));
    await show(null);
    expect(screen.getByText(/which cannot be used now/)).toBeTruthy();
  });

  it("switches modes, keeping the URL and password and the unknown members", async () => {
    await show(null);
    fireEvent.click(mode("No proxy"));
    expect(lastProxy()).toEqual(proxy({ mode: "none" }));
    expect(last()?.extra).toBe('{"x-kept":true}');
    expect(screen.getByText("Connects directly to the server, whatever proxy the app, the project or the environment sets.")).toBeTruthy();

    fireEvent.click(mode("Inherit"));
    expect(lastProxy()).toBeNull();

    fireEvent.click(mode("Custom proxy"));
    expect(lastProxy()).toEqual(proxy());
    expect(field("Type").value).toBe("socks5");
    expect(field("Port").placeholder).toBe("1080");
    fireEvent.change(field("Host"), { target: { value: "proxy.lan" } });
    expect(lastProxy()?.url).toBe("socks5h://proxy.lan");
    fireEvent.change(field("Password"), { target: { value: "{{proxyPassword}}" } });
    fireEvent.change(field("User name"), { target: { value: "ann" } });
    expect(lastProxy()).toMatchObject({ url: "socks5h://ann@proxy.lan", password: "{{proxyPassword}}" });

    fireEvent.click(mode("Inherit"));
    expect(lastProxy()).toEqual(proxy({ mode: "inherit", url: "socks5h://ann@proxy.lan", password: "{{proxyPassword}}" }));
    fireEvent.click(mode("Custom proxy"));
    expect(field("Host").value).toBe("proxy.lan");
    expect(field("User name").value).toBe("ann");
  });

  it("keeps the unknown members of the proxy", async () => {
    await show(proxy({ mode: "none", extra: '{"x-proxy":1}' }));
    fireEvent.click(mode("Inherit"));
    expect(lastProxy()).toEqual(proxy({ mode: "inherit", extra: '{"x-proxy":1}' }));
  });

  it("composes the URL from the fields and splits a stored one", async () => {
    await show(proxy({ url: "socks5h://{{proxyUser}}@{{proxyHost}}:1080" }));
    expect(field("Host").value).toBe("{{proxyHost}}");
    expect(field("Port").value).toBe("1080");
    expect(field("User name").value).toBe("{{proxyUser}}");

    fireEvent.change(field("User name"), { target: { value: "me@corp:{{team}}" } });
    expect(lastProxy()?.url).toBe("socks5h://me%40corp%3A{{team}}@{{proxyHost}}:1080");
    fireEvent.change(field("Host"), { target: { value: "::1" } });
    expect(lastProxy()?.url).toBe("socks5h://me%40corp%3A{{team}}@[::1]:1080");
    fireEvent.change(field("Port"), { target: { value: "" } });
    expect(lastProxy()?.url).toBe("socks5h://me%40corp%3A{{team}}@[::1]");
  });

  it("shows an IPv6 proxy without its brackets", async () => {
    await show(proxy({ url: "socks5://[fd00::7]:1081" }));
    expect(field("Host").value).toBe("fd00::7");
    expect((screen.getByRole("checkbox", { name: /Resolve host names through the proxy/ }) as HTMLInputElement).checked).toBe(false);
  });

  it("toggles remote DNS between socks5h and socks5, socks4a and socks4, and hides it for HTTP", async () => {
    await show(proxy({ url: "socks5h://proxy.lan" }));
    const remote = () => screen.getByRole("checkbox", { name: /Resolve host names through the proxy/ }) as HTMLInputElement;
    expect(remote().checked).toBe(true);
    fireEvent.click(remote());
    expect(lastProxy()?.url).toBe("socks5://proxy.lan");
    expect(screen.getByText(/host names are resolved on this computer/)).toBeTruthy();
    fireEvent.click(remote());
    expect(lastProxy()?.url).toBe("socks5h://proxy.lan");

    fireEvent.change(field("Type"), { target: { value: "socks4" } });
    expect(lastProxy()?.url).toBe("socks4a://proxy.lan");
    expect(screen.getByLabelText("User ID")).toBeTruthy();
    fireEvent.click(remote());
    expect(lastProxy()?.url).toBe("socks4://proxy.lan");

    fireEvent.change(field("Type"), { target: { value: "https" } });
    expect(lastProxy()?.url).toBe("https://proxy.lan");
    expect(field("Port").placeholder).toBe("443");
    expect(screen.queryByRole("checkbox", { name: /Resolve host names/ })).toBeNull();
    expect(screen.getByText(/An HTTP proxy resolves host names itself/)).toBeTruthy();

    fireEvent.change(field("Type"), { target: { value: "http" } });
    expect(lastProxy()?.url).toBe("http://proxy.lan");
    expect(field("Port").placeholder).toBe("1080");
  });

  it("shows a URL the fields cannot hold as one field, and nothing is lost", async () => {
    await show(proxy({ url: "socks5h://proxy.lan/path" }));
    const url = field("Proxy URL");
    expect(url.value).toBe("socks5h://proxy.lan/path");
    expect(screen.queryByLabelText("Host")).toBeNull();
    fireEvent.change(url, { target: { value: "socks5h://proxy.lan:1080" } });
    expect(lastProxy()?.url).toBe("socks5h://proxy.lan:1080");
    // Still one field while it is edited; split once it is left.
    expect(field("Proxy URL")).toBeTruthy();
    fireEvent.blur(field("Proxy URL"));
    expect(field("Host").value).toBe("proxy.lan");
    expect(field("Port").value).toBe("1080");
  });

  it("validates what it saves anyway", async () => {
    await show(proxy());
    expect(screen.queryByRole("alert")).toBeNull();
    fireEvent.change(field("Port"), { target: { value: "70000" } });
    expect(lastProxy()?.url).toBe("socks5h://:70000");
    const alerts = () => screen.getAllByRole("alert").map((a) => a.textContent);
    expect(alerts()).toEqual(["Enter the proxy's host.", "Enter a port from 1 to 65535, or a {{variable}}. Empty uses 1080."]);
    expect(field("Port").getAttribute("aria-invalid")).toBe("true");

    fireEvent.change(field("Host"), { target: { value: "proxy.lan" } });
    fireEvent.change(field("Port"), { target: { value: "{{proxyPort}}" } });
    expect(screen.queryByRole("alert")).toBeNull();

    fireEvent.change(field("Password"), { target: { value: "s3cret" } });
    expect(alerts()).toEqual(["A password needs a user name."]);
    fireEvent.change(field("User name"), { target: { value: "ann" } });
    expect(screen.queryByRole("alert")).toBeNull();

    fireEvent.change(field("Type"), { target: { value: "socks4" } });
    expect(alerts()).toEqual(["SOCKS4 takes a user ID but no password. Remove the password, or choose SOCKS5."]);
    expect(lastProxy()).toMatchObject({ url: "socks4a://ann@proxy.lan:{{proxyPort}}", password: "s3cret" });

    fireEvent.change(field("Type"), { target: { value: "http" } });
    fireEvent.change(field("User name"), { target: { value: "a:b" } });
    expect(alerts()).toEqual(["An HTTP proxy's user name cannot contain a colon."]);
  });

  it("asks for the host once it was left empty", async () => {
    await show(proxy());
    fireEvent.blur(field("Host"));
    expect(screen.getByRole("alert").textContent).toBe("Enter the proxy's host.");
  });

  it("masks a typed password, says where it is kept and drops the local mark when it is edited", async () => {
    await show(proxy({ url: "socks5h://ann@proxy.lan", password: "s3cret", localSecrets: ["password"] }));
    expect(field("Password").type).toBe("password");
    expect(screen.getByText(/Stored only on this computer, not in the project file/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Show the proxy password" }));
    expect(field("Password").type).toBe("text");

    fireEvent.change(field("Password"), { target: { value: "n3w" } });
    expect(lastProxy()).toEqual(proxy({ url: "socks5h://ann@proxy.lan", password: "n3w", localSecrets: [] }));

    fireEvent.change(field("Password"), { target: { value: "{{proxyPassword}}" } });
    expect(screen.getByText("Saved in the project as a reference; the value comes from the active environment.")).toBeTruthy();
    fireEvent.change(field("Password"), { target: { value: "" } });
    expect(lastProxy()?.password).toBe("");
    expect(screen.getByText(/a typed password is stored only on this computer/)).toBeTruthy();
  });

  it("says when the password was typed on another computer", async () => {
    await show(proxy({ url: "socks5h://ann@proxy.lan", localSecrets: ["password"] }));
    const hint = screen.getByText(/typed on another computer/).closest("p") as HTMLElement;
    expect(hint.dataset.storage).toBe("missing");
    expect(hint.textContent).toBe(
      "The proxy password was typed on another computer and is not stored on this one. Type it again, or use a {{variable}}.",
    );
    expect(field("Password").getAttribute("aria-describedby")).toBe(hint.id);
    fireEvent.change(field("Password"), { target: { value: "again" } });
    expect(lastProxy()?.localSecrets).toEqual([]);
  });

  it("warns about a mode it does not know and replaces it on a choice", async () => {
    await show(proxy({ mode: "pac", url: "socks5h://h" }));
    for (const name of ["Inherit", "Custom proxy", "No proxy"]) expect(mode(name).getAttribute("aria-pressed")).toBe("false");
    expect(screen.getByRole("note").textContent).toContain("(pac) is not one this version of PostSonde knows");
    fireEvent.click(mode("No proxy"));
    expect(lastProxy()).toEqual(proxy({ mode: "none", url: "socks5h://h" }));
  });

  it("says UDP needs SOCKS5", async () => {
    await show(proxy({ url: "socks5h://h" }), { udp: true });
    expect(screen.queryByRole("note")).toBeNull();
    fireEvent.change(field("Type"), { target: { value: "http" } });
    expect(screen.getByRole("note").textContent).toContain("UDP goes only through a SOCKS5 proxy");
  });

  it("changes nothing while read-only or locked", async () => {
    await show(proxy({ url: "socks5h://h" }), { readOnly: true });
    expect(mode("No proxy").hasAttribute("disabled")).toBe(true);
    expect(field("Host").readOnly).toBe(true);
    expect((screen.getByLabelText("Type") as HTMLSelectElement).disabled).toBe(true);
  });

  it("folds under the TCP and UDP options with the proxy in its summary", async () => {
    render(
      <RequestProxyFold settings={{ ...baseSettings, proxy: proxy({ url: "socks5h://proxy.lan:1080" }) }} readOnly={false} edit={() => undefined} locked="Disconnect to change" />,
    );
    await act(settle);
    const fold = document.querySelector("[data-proxy-fold]") as HTMLElement;
    expect(fold.querySelector("summary")?.textContent).toBe("Proxysocks5h://proxy.lan:1080");
    expect(within(fold).queryByRole("heading")).toBeNull();
    expect(within(fold).getByRole("button", { name: "Inherit", hidden: true }).hasAttribute("disabled")).toBe(true);
    expect((within(fold).getByLabelText("Host") as HTMLInputElement).disabled).toBe(true);
  });

  it("takes every text from the catalogue", async () => {
    setLocale(PSEUDO_LOCALE);
    const view = await show(proxy({ url: "socks5h://ann@proxy.lan:70000", password: "pw", localSecrets: ["password"] }), { udp: true });
    expect(englishLeftovers([view.container], ["editor"])).toEqual([]);
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "socks4" } });
    expect(englishLeftovers([view.container], ["editor"])).toEqual([]);
  });
});
