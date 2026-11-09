import { describe, expect, it } from "vitest";
import {
  composeProxyUrl,
  DEFAULT_PROXY_FIELDS,
  encodeProxyUser,
  parseProxyUrl,
  proxyPasswordStorage,
  proxyProblems,
  requestProxyModeOf,
  requestProxyPatch,
  withProxyPassword,
  type ProxyFields,
  type RequestProxyDTO,
} from "./requestProxy";

const fields = (over: Partial<ProxyFields> = {}): ProxyFields => ({ ...DEFAULT_PROXY_FIELDS, ...over });

const proxy = (over: Partial<RequestProxyDTO> = {}): RequestProxyDTO => ({
  mode: "custom",
  url: "",
  password: "",
  localSecrets: [],
  extra: "",
  ...over,
});

describe("parseProxyUrl", () => {
  it("splits each scheme into a type and the remote DNS switch", () => {
    expect(parseProxyUrl("socks5h://proxy.lan:1080")).toEqual(fields({ host: "proxy.lan", port: "1080" }));
    expect(parseProxyUrl("socks5://proxy.lan")).toEqual(fields({ remoteDns: false, host: "proxy.lan" }));
    expect(parseProxyUrl("socks4a://p:1081")).toEqual(fields({ type: "socks4", remoteDns: true, host: "p", port: "1081" }));
    expect(parseProxyUrl("SOCKS4://p")).toEqual(fields({ type: "socks4", remoteDns: false, host: "p" }));
    expect(parseProxyUrl("http://p:3128")).toMatchObject({ type: "http", host: "p", port: "3128" });
    expect(parseProxyUrl("https://p")).toMatchObject({ type: "https", host: "p", port: "" });
  });

  it("reads the empty URL as the default fields", () => {
    expect(parseProxyUrl("")).toEqual(DEFAULT_PROXY_FIELDS);
    expect(parseProxyUrl("  ")).toEqual(DEFAULT_PROXY_FIELDS);
  });

  it("takes {{variables}} in any part but the scheme", () => {
    expect(parseProxyUrl("socks5h://{{proxyUser}}@{{proxyHost}}:1080")).toEqual(
      fields({ user: "{{proxyUser}}", host: "{{proxyHost}}", port: "1080" }),
    );
    expect(parseProxyUrl("socks5h://{{host}}:{{port}}")).toEqual(fields({ host: "{{host}}", port: "{{port}}" }));
    // A variable may hold the characters that otherwise split a URL.
    expect(parseProxyUrl("socks5://{{ a:b@c }}@h")).toEqual(fields({ remoteDns: false, user: "{{ a:b@c }}", host: "h" }));
    expect(parseProxyUrl("socks5h://pre-{{env}}.proxy.lan:10{{n}}")).toEqual(fields({ host: "pre-{{env}}.proxy.lan", port: "10{{n}}" }));
  });

  it("takes an IPv6 address in brackets", () => {
    expect(parseProxyUrl("socks5h://[::1]:1080")).toEqual(fields({ host: "::1", port: "1080" }));
    expect(parseProxyUrl("socks5h://[fe80::1%25eth0]")).toEqual(fields({ host: "fe80::1%25eth0" }));
  });

  it("percent-decodes the user name, keeping its variables", () => {
    expect(parseProxyUrl("socks5h://me%40corp%3Aeu@h")).toEqual(fields({ user: "me@corp:eu", host: "h" }));
    expect(parseProxyUrl("socks5h://a%20{{b}}@h")).toEqual(fields({ user: "a {{b}}", host: "h" }));
    // A broken escape is kept as written.
    expect(parseProxyUrl("socks5h://a%zz@h")).toEqual(fields({ user: "a%zz", host: "h" }));
  });

  it("allows Go's trailing slash", () => {
    expect(parseProxyUrl("socks5h://h:1080/")).toEqual(fields({ host: "h", port: "1080" }));
  });

  it("returns null for what the fields cannot hold", () => {
    expect(parseProxyUrl("proxy.lan:1080")).toBeNull();
    expect(parseProxyUrl("ftp://proxy.lan")).toBeNull();
    expect(parseProxyUrl("{{scheme}}://proxy.lan")).toBeNull();
    expect(parseProxyUrl("socks5h://user:secret@h")).toBeNull();
    expect(parseProxyUrl("socks5h://h/path")).toBeNull();
    expect(parseProxyUrl("socks5h://h?x=1")).toBeNull();
    expect(parseProxyUrl("socks5h://::1")).toBeNull();
    expect(parseProxyUrl("socks5h://[::1")).toBeNull();
    expect(parseProxyUrl("socks5h://[::1]x")).toBeNull();
  });
});

describe("composeProxyUrl", () => {
  it("writes the scheme of the type and the remote DNS switch", () => {
    expect(composeProxyUrl(fields({ host: "p", port: "1080" }))).toBe("socks5h://p:1080");
    expect(composeProxyUrl(fields({ remoteDns: false, host: "p" }))).toBe("socks5://p");
    expect(composeProxyUrl(fields({ type: "socks4", host: "p" }))).toBe("socks4a://p");
    expect(composeProxyUrl(fields({ type: "socks4", remoteDns: false, host: "p" }))).toBe("socks4://p");
    expect(composeProxyUrl(fields({ type: "http", remoteDns: false, host: "p", port: "3128" }))).toBe("http://p:3128");
    expect(composeProxyUrl(fields({ type: "https", host: "p" }))).toBe("https://p");
  });

  it("is empty for the default fields with nothing typed", () => {
    expect(composeProxyUrl(DEFAULT_PROXY_FIELDS)).toBe("");
    // Another type is kept, so it is found again.
    expect(composeProxyUrl(fields({ type: "http" }))).toBe("http://");
  });

  it("puts an IPv6 address in brackets", () => {
    expect(composeProxyUrl(fields({ host: "::1", port: "1080" }))).toBe("socks5h://[::1]:1080");
    expect(composeProxyUrl(fields({ host: "[::1]" }))).toBe("socks5h://[::1]");
    expect(composeProxyUrl(fields({ host: "{{ipv6:host}}" }))).toBe("socks5h://{{ipv6:host}}");
  });

  it("percent-encodes the user name but not its variables", () => {
    expect(composeProxyUrl(fields({ user: "me@corp:eu/x", host: "h" }))).toBe("socks5h://me%40corp%3Aeu%2Fx@h");
    expect(composeProxyUrl(fields({ user: "{{proxyUser}}", host: "{{proxyHost}}", port: "1080" }))).toBe(
      "socks5h://{{proxyUser}}@{{proxyHost}}:1080",
    );
    expect(encodeProxyUser("a b{{x y}}%")).toBe("a%20b{{x y}}%25");
  });

  it("parses back what it writes", () => {
    for (const f of [
      fields({ host: "proxy.lan", port: "1080", user: "ann" }),
      fields({ type: "socks4", remoteDns: false, host: "10.0.0.1", user: "id 7" }),
      fields({ host: "::1", port: "{{port}}", user: "a@b:{{c}}" }),
      fields({ type: "https", host: "{{h}}", user: "{{u}}" }),
    ]) {
      expect(parseProxyUrl(composeProxyUrl(f))).toEqual(f);
    }
  });
});

describe("proxyProblems", () => {
  it("asks for a host and a usable port", () => {
    expect(proxyProblems(fields(), "")).toEqual({ host: "hostRequired" });
    expect(proxyProblems(fields({ host: "a b" }), "").host).toBe("hostInvalid");
    expect(proxyProblems(fields({ host: "h", port: "0" }), "").port).toBe("portInvalid");
    expect(proxyProblems(fields({ host: "h", port: "65536" }), "").port).toBe("portInvalid");
    expect(proxyProblems(fields({ host: "h", port: "8o" }), "").port).toBe("portInvalid");
    expect(proxyProblems(fields({ host: "h", port: "65535" }), "")).toEqual({});
    expect(proxyProblems(fields({ host: "{{h}}", port: "{{port}}" }), "")).toEqual({});
  });

  it("wants a user name with a password, and no password for SOCKS4", () => {
    expect(proxyProblems(fields({ host: "h" }), "pw").password).toBe("passwordNeedsUser");
    expect(proxyProblems(fields({ host: "h", user: "ann" }), "pw")).toEqual({});
    expect(proxyProblems(fields({ type: "socks4", host: "h", user: "ann" }), "pw").password).toBe("socks4Password");
    expect(proxyProblems(fields({ type: "socks4", host: "h", user: "ann" }), "")).toEqual({});
  });

  it("refuses a colon in an HTTP proxy's user name", () => {
    expect(proxyProblems(fields({ type: "http", host: "h", user: "a:b" }), "").user).toBe("httpUserColon");
    expect(proxyProblems(fields({ type: "http", host: "h", user: "{{a:b}}" }), "")).toEqual({});
    expect(proxyProblems(fields({ host: "h", user: "a:b" }), "")).toEqual({});
  });
});

describe("the settings member", () => {
  it("reads the mode, unknown modes as null", () => {
    expect(requestProxyModeOf(null)).toBe("inherit");
    expect(requestProxyModeOf(proxy({ mode: "" }))).toBe("inherit");
    expect(requestProxyModeOf(proxy({ mode: "inherit" }))).toBe("inherit");
    expect(requestProxyModeOf(proxy({ mode: "none" }))).toBe("none");
    expect(requestProxyModeOf(proxy({ mode: "pac" }))).toBeNull();
  });

  it("is left out when it inherits and keeps nothing, kept otherwise", () => {
    expect(requestProxyPatch(null, { mode: "inherit" })).toBeNull();
    expect(requestProxyPatch(null, { mode: "custom" })).toEqual(proxy({ mode: "custom" }));
    const custom = proxy({ url: "socks5h://h", password: "{{pw}}", extra: '{"x":1}' });
    expect(requestProxyPatch(custom, { mode: "inherit" })).toEqual({ ...custom, mode: "inherit" });
    expect(requestProxyPatch(custom, { mode: "none" })).toEqual({ ...custom, mode: "none" });
    expect(requestProxyPatch(proxy({ mode: "none" }), { mode: "inherit" })).toBeNull();
  });

  it("drops the local-secret mark when the password is edited", () => {
    const missing = proxy({ url: "socks5h://ann@h", localSecrets: ["password"] });
    expect(proxyPasswordStorage(missing)).toBe("missing");
    const typed = withProxyPassword(missing, "s3cret");
    expect(typed).toEqual({ ...missing, password: "s3cret", localSecrets: [] });
    expect(proxyPasswordStorage(typed)).toBe("local");
    expect(proxyPasswordStorage(proxy({ password: "{{proxyPassword}}" }))).toBe("reference");
    expect(proxyPasswordStorage(proxy())).toBe("empty");
    expect(proxyPasswordStorage(null)).toBe("empty");
  });
});
