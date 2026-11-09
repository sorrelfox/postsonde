import { describe, expect, it } from "vitest";
import type { RequestDTO } from "./collection";
import { countLabel, firstError, importPatch, looksLikeCurl, warningsOf } from "./curl";

describe("looksLikeCurl", () => {
  it.each([
    "curl https://example.com",
    "  curl 'https://example.com' \\\n  -H 'a: b'",
    "\uFEFFcurl -X POST x",
    "$ curl example.com",
    "% curl example.com",
    "PS C:\\Users\\me> curl.exe -k https://h",
    "/usr/bin/curl -s https://h",
    "C:\\tools\\curl.exe https://h",
    "CURL https://h",
    "./curl -v h",
    "curl ^\"https://h^\"",
  ])("recognises %j", (text) => {
    expect(looksLikeCurl(text)).toBe(true);
  });

  it.each(["https://example.com/curl", "curl", "curl   ", "curly https://h", "wget https://h", "{{baseUrl}}/curl -X", "http://h/curl -X", "", "xcurl https://h"])(
    "leaves %j alone",
    (text) => {
      expect(looksLikeCurl(text)).toBe(false);
    },
  );
});

describe("importPatch", () => {
  it("replaces the http fields and keeps name, description, extra and other settings", () => {
    const settings = { timeoutMs: 1, followRedirects: true, maxRedirects: 3, followOriginalMethod: true, forwardAuthorization: true, verifyTls: null, connectTimeoutMs: 9, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "{}" };
    const current = { name: "Mine", description: "d", settings } as unknown as RequestDTO;
    const imported = {
      method: "PUT",
      url: "https://h/x",
      params: [],
      pathParams: [],
      headers: [],
      body: { mode: "none" },
      auth: { type: "inherit" },
      settings: { ...settings, timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: false, connectTimeoutMs: null, httpVersion: "http1", extra: "" },
    } as unknown as RequestDTO;
    const patch = importPatch(current, imported);
    expect(Object.keys(patch).sort()).toEqual(["auth", "body", "headers", "method", "params", "pathParams", "settings", "url"]);
    expect(patch.settings).toEqual({ ...settings, timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: false, httpVersion: "http1" });
  });
});

describe("diagnostic helpers", () => {
  const d = (severity: string, message: string) => ({ code: "c", severity, from: 0, to: 0, message });
  it("finds the first error and the warnings", () => {
    const ds = [d("info", "i"), d("warning", "w1"), d("error", "e"), d("warning", "w2")];
    expect(firstError(ds)?.message).toBe("e");
    expect(warningsOf(ds).map((w) => w.message)).toEqual(["w1", "w2"]);
    expect(firstError(null)).toBeNull();
    expect(warningsOf(null)).toEqual([]);
    expect(countLabel(1, "warning")).toBe("1 warning");
    expect(countLabel(2, "header")).toBe("2 headers");
  });
});
