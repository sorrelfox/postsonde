import { describe, expect, it, vi } from "vitest";

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);

import {
  cookieFromDraft,
  draftFromCookie,
  emptyDraft,
  filterCookies,
  formatExpires,
  groupCookies,
  jarLabel,
  toLocalInput,
  type CookieDTO,
} from "./cookies";

function cookie(over: Partial<CookieDTO>): CookieDTO {
  return {
    key: over.name ?? "k",
    name: "n",
    value: "v",
    domain: "example.com",
    hostOnly: true,
    path: "/",
    secure: false,
    httpOnly: false,
    sameSite: "",
    expires: "",
    created: "",
    lastAccess: "",
    binary: false,
    ...over,
  };
}

describe("groupCookies", () => {
  it("groups by domain, sorted from the rightmost label", () => {
    const groups = groupCookies([
      cookie({ name: "a", domain: "other.test" }),
      cookie({ name: "b", domain: "example.com" }),
      cookie({ name: "c", domain: "api.example.com" }),
      cookie({ name: "d", domain: "example.com" }),
    ]);
    expect(groups.map((g) => g.domain)).toEqual(["example.com", "api.example.com", "other.test"]);
    expect(groups[0].cookies.map((c) => c.name)).toEqual(["b", "d"]);
  });
});

describe("filterCookies", () => {
  it("matches domain, name or value, ignoring case", () => {
    const list = [cookie({ name: "Session", value: "abc" }), cookie({ name: "theme", value: "Dark", domain: "shop.test" })];
    expect(filterCookies(list, "  ").length).toBe(2);
    expect(filterCookies(list, "SESS").map((c) => c.name)).toEqual(["Session"]);
    expect(filterCookies(list, "dark").map((c) => c.name)).toEqual(["theme"]);
    expect(filterCookies(list, "shop").map((c) => c.name)).toEqual(["theme"]);
  });
});

describe("drafts", () => {
  it("round-trips a persistent cookie", () => {
    const expires = new Date(2030, 0, 2, 3, 4);
    const c = cookie({ hostOnly: false, secure: true, sameSite: "Lax", expires: expires.toISOString(), path: "/api" });
    const draft = draftFromCookie(c);
    expect(draft).toMatchObject({ includeSubdomains: true, session: false, sameSite: "Lax", expires: toLocalInput(expires) });
    const result = cookieFromDraft(draft);
    expect(result).toEqual({ cookie: { ...c, key: "", created: "", lastAccess: "" } });
  });

  it("makes session cookies and checks the fields it can", () => {
    const now = new Date(2030, 5, 1, 12, 0);
    const draft = emptyDraft("example.com", now);
    expect(draft.session).toBe(true);
    expect(draft.expires).toBe("2030-06-08T12:00");
    expect(cookieFromDraft(draft)).toEqual({ error: "Enter a name.", field: "name" });
    const named = { ...draft, name: "a" };
    expect(cookieFromDraft(named)).toMatchObject({ cookie: { name: "a", expires: "", hostOnly: true } });
    expect(cookieFromDraft({ ...named, domain: " " })).toMatchObject({ field: "domain" });
    expect(cookieFromDraft({ ...named, session: false, expires: "" })).toMatchObject({ field: "expires" });
  });
});

describe("formatExpires", () => {
  it("shows session cookies as Session", () => {
    expect(formatExpires("")).toBe("Session");
    expect(formatExpires("nonsense")).toBe("nonsense");
    expect(formatExpires("2030-01-02T03:04:00Z")).not.toBe("");
  });
});

describe("jarLabel", () => {
  it("names the shared jar, an environment's and the no-environment jar", () => {
    const base = { workspace: "Shop API", perEnvironment: false, environmentId: "", environment: "" };
    expect(jarLabel(base)).toEqual({ name: "Shop API", scope: "Shared by all environments" });
    // With the setting off the environment is not part of the name, even if one is active.
    expect(jarLabel({ ...base, perEnvironment: true, environmentId: "env_x", environment: "Staging" })).toEqual({
      name: "Shop API · Staging",
      scope: "Only this environment",
    });
    expect(jarLabel({ ...base, perEnvironment: true })).toEqual({
      name: "Shop API · No environment",
      scope: "Used when no environment is active",
    });
  });
});
