import { describe, expect, it } from "vitest";
import type { KeyValueDTO } from "./collection";
import {
  encodeQueryKey,
  encodeQueryValue,
  paramsToUrl,
  parseQuery,
  parseUrlQuery,
  splitUrl,
  urlToParams,
} from "./urlParams";

const row = (name: string, value: string, over: Partial<KeyValueDTO> = {}): KeyValueDTO => ({
  name,
  value,
  description: "",
  enabled: true,
  extra: "",
  ...over,
});

/** name=value pairs of rows, with "!" before disabled ones. */
const brief = (rows: readonly KeyValueDTO[] | null) =>
  (rows ?? []).map((r) => `${r.enabled ? "" : "!"}${r.name}=${r.value}`);

describe("splitUrl", () => {
  it.each([
    ["https://x.dev/a", { base: "https://x.dev/a", query: null, fragment: "" }],
    ["https://x.dev/a?b=1", { base: "https://x.dev/a", query: "b=1", fragment: "" }],
    ["https://x.dev/a?", { base: "https://x.dev/a", query: "", fragment: "" }],
    ["/a?b=1#top", { base: "/a", query: "b=1", fragment: "#top" }],
    ["/a#top?b=1", { base: "/a", query: null, fragment: "#top?b=1" }],
    ["/a?b=?c#d#e", { base: "/a", query: "b=?c", fragment: "#d#e" }],
    ["x.dev/a?b", { base: "x.dev/a", query: "b", fragment: "" }],
    ["{{baseUrl}}/path?x=1", { base: "{{baseUrl}}/path", query: "x=1", fragment: "" }],
    ["{{base?url#}}/p?x={{a#b}}", { base: "{{base?url#}}/p", query: "x={{a#b}}", fragment: "" }],
    ["\\{{a?b}}", { base: "\\{{a", query: "b}}", fragment: "" }],
    ["{{a?b", { base: "{{a", query: "b", fragment: "" }],
    ["", { base: "", query: null, fragment: "" }],
  ])("%s", (url, want) => {
    expect(splitUrl(url)).toEqual(want);
  });
});

describe("parseQuery", () => {
  it("splits on & and the first =", () => {
    expect(parseQuery("a=1&b=x=y&c")).toEqual([
      { key: "a", value: "1", raw: "a=1" },
      { key: "b", value: "x=y", raw: "b=x=y" },
      { key: "c", value: "", raw: "c" },
    ]);
  });

  it("keeps empty values, empty keys and repeated keys", () => {
    expect(parseQuery("a=&=v&a=2&a=2")).toEqual([
      { key: "a", value: "", raw: "a=" },
      { key: "", value: "v", raw: "=v" },
      { key: "a", value: "2", raw: "a=2" },
      { key: "a", value: "2", raw: "a=2" },
    ]);
  });

  it("drops empty entries", () => {
    expect(parseQuery("")).toEqual([]);
    expect(parseQuery("&&a=1&&b=2&")).toEqual([
      { key: "a", value: "1", raw: "a=1" },
      { key: "b", value: "2", raw: "b=2" },
    ]);
  });

  it("keeps the encoding as written", () => {
    expect(parseQuery("q=a+b&r=a%20b&s=%26&t=é")).toEqual([
      { key: "q", value: "a+b", raw: "q=a+b" },
      { key: "r", value: "a%20b", raw: "r=a%20b" },
      { key: "s", value: "%26", raw: "s=%26" },
      { key: "t", value: "é", raw: "t=é" },
    ]);
  });

  it("does not split inside {{variables}}", () => {
    expect(parseQuery("{{k=v}}={{a&b=c}}&x={{ y }}")).toEqual([
      { key: "{{k=v}}", value: "{{a&b=c}}", raw: "{{k=v}}={{a&b=c}}" },
      { key: "x", value: "{{ y }}", raw: "x={{ y }}" },
    ]);
  });

  it("splits inside escaped braces", () => {
    expect(parseUrlQuery("/p?a=\\{{x&y}}")).toEqual([
      { key: "a", value: "\\{{x", raw: "a=\\{{x" },
      { key: "y}}", value: "", raw: "y}}" },
    ]);
  });
});

describe("encodeQueryKey / encodeQueryValue", () => {
  it("encodes only what would break the query, outside variables", () => {
    expect(encodeQueryKey("a=b&c#d %20+")).toBe("a%3Db%26c%23d %20+");
    expect(encodeQueryValue("a=b&c#d")).toBe("a=b%26c%23d");
    expect(encodeQueryValue("{{a&b}}&")).toBe("{{a&b}}%26");
    expect(encodeQueryValue("plain")).toBe("plain");
  });
});

describe("paramsToUrl", () => {
  it("writes enabled rows in order and leaves disabled and blank rows out", () => {
    const params = [row("a", "1"), row("b", "2", { enabled: false }), row("", ""), row("c", ""), row("", "v")];
    expect(paramsToUrl("{{baseUrl}}/p", params)).toBe("{{baseUrl}}/p?a=1&c&=v");
  });

  it("keeps the fragment after the query", () => {
    expect(paramsToUrl("/p?old=1#frag", [row("a", "1")])).toBe("/p?a=1#frag");
    expect(paramsToUrl("/p#frag", [row("a", "1")])).toBe("/p?a=1#frag");
  });

  it("drops the ? when no row is in the URL", () => {
    expect(paramsToUrl("/p?a=1#f", [row("a", "1", { enabled: false })])).toBe("/p#f");
    expect(paramsToUrl("/p?a=1", [])).toBe("/p");
    expect(paramsToUrl("/p?a=1", null)).toBe("/p");
  });

  it("returns the URL untouched when the query already matches", () => {
    for (const url of ["/p?", "/p?a=1&&b=", "/p?a=1&b&", "/p?a=%20+"]) {
      expect(paramsToUrl(url, urlToParams(url, null))).toBe(url);
    }
    expect(paramsToUrl("/p?", null)).toBe("/p?");
  });

  it("keeps the text of entries that still match", () => {
    // b= keeps its =, the new row c is written without one.
    expect(paramsToUrl("/p?a=1&b=", [row("b", ""), row("c", "")])).toBe("/p?b=&c");
  });

  it("encodes & and # in values, = too in keys, but not inside variables", () => {
    const url = paramsToUrl("/p", [row("k=1", "a&b#c=d"), row("v", "{{x&y}}"), row("s", "a b%20+")]);
    expect(url).toBe("/p?k%3D1=a%26b%23c=d&v={{x&y}}&s=a b%20+");
    // And back: rows that encode to the URL's text keep their own text.
    const rows = [row("k=1", "a&b#c=d"), row("v", "{{x&y}}"), row("s", "a b%20+")];
    expect(urlToParams(url, rows)).toBe(rows);
  });

  it("handles repeated keys", () => {
    expect(paramsToUrl("/p", [row("a", "1"), row("a", "2"), row("a", "1")])).toBe("/p?a=1&a=2&a=1");
  });
});

describe("urlToParams", () => {
  it("splits a pasted URL into rows", () => {
    expect(urlToParams("https://api.dev/v1?limit=20&q=a%20b&flag#x", null)).toEqual([
      row("limit", "20"),
      row("q", "a%20b"),
      row("flag", ""),
    ]);
  });

  it("returns the same array when nothing changes", () => {
    const params = [row("a", "1", { description: "d" }), row("b", "", { enabled: false })];
    expect(urlToParams("/p?a=1", params)).toBe(params);
    expect(urlToParams("/p?a=1&", params)).toBe(params);
    expect(urlToParams("/p", null)).toBeNull();
    const empty: KeyValueDTO[] = [];
    expect(urlToParams("/p?", empty)).toBe(empty);
  });

  it("updates values in place, keeping description, extra and disabled rows", () => {
    const params = [
      row("limit", "20", { description: "Page size", extra: '{"x":1}' }),
      row("cursor", "", { enabled: false, description: "off" }),
      row("sort", "asc"),
    ];
    const next = urlToParams("/p?limit=50&sort=desc", params);
    expect(next).toEqual([
      row("limit", "50", { description: "Page size", extra: '{"x":1}' }),
      row("cursor", "", { enabled: false, description: "off" }),
      row("sort", "desc"),
    ]);
    expect(next?.[1]).toBe(params[1]);
  });

  it("keeps a row while its key is being typed", () => {
    const params = [row("a", "1"), row("limit", "20", { description: "Page size" }), row("z", "9")];
    expect(urlToParams("/p?a=1&limitx=20&z=9", params)).toEqual([
      row("a", "1"),
      row("limitx", "20", { description: "Page size" }),
      row("z", "9"),
    ]);
    // Key and value both changed at once, still in place.
    expect(urlToParams("/p?a=1&l=2&z=9", params)?.[1]).toEqual(row("l", "2", { description: "Page size" }));
  });

  it("deletes the rows whose entries were removed, keeping the others' descriptions", () => {
    const params = [row("a", "1", { description: "A" }), row("b", "2", { description: "B" }), row("c", "3", { description: "C" })];
    expect(urlToParams("/p?a=1&c=3", params)).toEqual([row("a", "1", { description: "A" }), row("c", "3", { description: "C" })]);
    expect(urlToParams("/p", params)).toEqual([]);
  });

  it("matches repeated keys by value first", () => {
    const params = [row("a", "1", { description: "one" }), row("a", "2", { description: "two" })];
    expect(urlToParams("/p?a=2", params)).toEqual([row("a", "2", { description: "two" })]);
    expect(urlToParams("/p?a=1&a=2&a=3", params)).toEqual([
      row("a", "1", { description: "one" }),
      row("a", "2", { description: "two" }),
      row("a", "3"),
    ]);
  });

  it("inserts new entries after the row before them, disabled rows staying put", () => {
    const params = [
      row("off1", "", { enabled: false }),
      row("a", "1"),
      row("off2", "", { enabled: false }),
      row("b", "2"),
    ];
    expect(brief(urlToParams("/p?new=0&a=1&mid=x&b=2&end=z", params))).toEqual([
      "!off1=",
      "new=0",
      "a=1",
      "mid=x",
      "!off2=",
      "b=2",
      "end=z",
    ]);
  });

  it("appends rows after disabled ones when no row is in the URL", () => {
    const params = [row("off", "1", { enabled: false }), row("", "")];
    expect(brief(urlToParams("/p?a=1", params))).toEqual(["!off=1", "=", "a=1"]);
  });

  it("does not bring back a disabled row when the URL has the same entry", () => {
    const params = [row("a", "1", { enabled: false })];
    expect(brief(urlToParams("/p?a=1", params))).toEqual(["!a=1", "a=1"]);
  });

  it("does not split template variables", () => {
    expect(urlToParams("{{baseUrl}}/p?q={{a&b=c}}&{{k#}}=1", null)).toEqual([
      row("q", "{{a&b=c}}"),
      row("{{k#}}", "1"),
    ]);
  });

  it("keeps a row's text when it encodes to the URL's", () => {
    const params = [row("q", "a&b")];
    expect(paramsToUrl("/p", params)).toBe("/p?q=a%26b");
    expect(urlToParams("/p?q=a%26b", params)).toBe(params);
    // Typed in the URL as %26: the row takes the URL's text.
    expect(urlToParams("/p?q=a%26bc", params)).toEqual([row("q", "a%26bc")]);
  });

  it("round-trips: the URL rebuilt from the merged rows is the URL", () => {
    const urls = [
      "https://x.dev/a?b=1&c=&d&=e&b=1#frag",
      "{{baseUrl}}/p?q={{a&b}}&r=%2B+x",
      "/p?&&a=1&",
      "x.dev?",
      "/p#only?frag",
    ];
    const start = [row("keep", "me", { enabled: false, description: "d" })];
    for (const url of urls) {
      const params = urlToParams(url, start);
      expect(paramsToUrl(url, params)).toBe(url);
      expect(params?.[0]).toBe(start[0]);
    }
  });
});
