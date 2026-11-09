import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import tokenTable from "../../../internal/core/themes/testdata/tokens.json.golden?raw";
import themeInitSource from "../../public/theme-init.js?raw";
import {
  applyCustomTheme,
  appliedCustomThemeId,
  CUSTOM_THEME_STORAGE_KEY,
  isThemeColor,
  resetCustomThemeForTests,
  sanitizeCustomTheme,
  storedCustomTheme,
  THEME_TOKENS,
} from "./customTheme";
import { applyTheme, currentTheme, resetThemeForTests, setSystemDark, subscribeTheme } from "./theme";

// Built at run time: colour literals belong in tokens.css only.
const hex = (digits: string) => `#${digits}`;
const root = () => document.documentElement;

function clearRootStyle() {
  root().removeAttribute("style");
  delete root().dataset.theme;
}

beforeEach(() => {
  localStorage.clear();
  clearRootStyle();
  resetThemeForTests();
  resetCustomThemeForTests();
  vi.stubGlobal("matchMedia", () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

const ocean = {
  id: "ocean.json",
  base: "light",
  vars: [
    { name: "--bg0", value: hex("001122") },
    { name: "--accent", value: hex("0080ff") },
    { name: "--tk-kw", value: hex("c792ea80") },
  ],
};

describe("applyCustomTheme (UI-05)", () => {
  it("sets the theme's colours as CSS variables on :root and pins its base", () => {
    applyTheme("dark");
    const seen: string[] = [];
    subscribeTheme(() => seen.push(currentTheme()));

    expect(applyCustomTheme(ocean)).toEqual([]);
    const style = getComputedStyle(root());
    expect(style.getPropertyValue("--bg0")).toBe(hex("001122"));
    expect(root().style.getPropertyValue("--accent")).toBe(hex("0080ff"));
    expect(root().style.getPropertyValue("--tk-kw")).toBe(hex("c792ea80"));
    expect(currentTheme()).toBe("light");
    expect(seen).toEqual(["light"]);
    expect(appliedCustomThemeId()).toBe("ocean.json");

    // The preference and the OS no longer change the theme while it is applied.
    applyTheme("dark");
    setSystemDark(false);
    expect(currentTheme()).toBe("light");
  });

  it("removes its colours and goes back to the preference", () => {
    applyTheme("dark");
    applyCustomTheme(ocean);
    applyCustomTheme(null);
    expect(root().style.getPropertyValue("--bg0")).toBe("");
    expect(root().style.getPropertyValue("--accent")).toBe("");
    expect(currentTheme()).toBe("dark");
    expect(appliedCustomThemeId()).toBeNull();
    expect(localStorage.getItem(CUSTOM_THEME_STORAGE_KEY)).toBeNull();
  });

  it("replaces the previous theme's properties", () => {
    applyCustomTheme(ocean);
    applyCustomTheme({ id: "b.json", base: "dark", vars: [{ name: "--text", value: hex("eeeeee") }] });
    expect(root().style.getPropertyValue("--bg0")).toBe("");
    expect(root().style.getPropertyValue("--text")).toBe(hex("eeeeee"));
    expect(currentTheme()).toBe("dark");
  });

  it("refuses unknown properties and anything that is not a normalised colour", () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const malicious = [
      { name: "--bg1", value: "red" },
      { name: "--bg2", value: `${hex("ffffff")}; background: url(https://evil.example/x)` },
      { name: "--bg3", value: "url(https://evil.example/x)" },
      { name: "--line", value: "expression(alert(1))" },
      { name: "--line2", value: `${hex("fff")}} body { display: none` },
      { name: "--text", value: "var(--bg0)" },
      { name: "--text2", value: hex("ABCDEF") },
      { name: "--text3", value: hex("abc") },
      { name: "background", value: hex("000000") },
      { name: "--bg0; color", value: hex("000000") },
      { name: "--shadow", value: hex("000000") },
      { name: "--my-token", value: hex("000000") },
      { name: "--accent", value: hex("112233") },
    ];
    const rejected = applyCustomTheme({ id: "evil.json", base: "dark", vars: malicious });
    expect(rejected).toHaveLength(malicious.length - 1);
    expect(root().style.getPropertyValue("--accent")).toBe(hex("112233"));
    const css = root().getAttribute("style") ?? "";
    expect(css).not.toMatch(/url|expression|display|background|shadow|my-token/);
    for (const name of ["--bg1", "--bg2", "--bg3", "--line", "--line2", "--text", "--text2", "--text3"]) {
      expect(root().style.getPropertyValue(name)).toBe("");
    }
  });

  it("refuses a theme whose base is not dark or light", () => {
    applyTheme("dark");
    expect(applyCustomTheme({ id: "x.json", base: "sepia", vars: ocean.vars })).toEqual(["base"]);
    expect(root().style.getPropertyValue("--bg0")).toBe("");
    expect(appliedCustomThemeId()).toBeNull();
    expect(sanitizeCustomTheme({ id: "x.json", base: "", vars: [] })).toBeNull();
  });

  it("caches the applied theme for first paint", () => {
    applyCustomTheme(ocean);
    expect(storedCustomTheme()).toEqual(ocean);
    localStorage.setItem(CUSTOM_THEME_STORAGE_KEY, "{not json");
    expect(storedCustomTheme()).toBeNull();
    localStorage.setItem(CUSTOM_THEME_STORAGE_KEY, JSON.stringify({ id: 1 }));
    expect(storedCustomTheme()).toBeNull();
  });

  it("accepts only lower-case #rrggbb and #rrggbbaa colours", () => {
    expect(isThemeColor(hex("a1b2c3"))).toBe(true);
    expect(isThemeColor(hex("a1b2c3d4"))).toBe(true);
    for (const bad of [hex("A1B2C3"), hex("abc"), `rgb${"(0, 0, 0)"}`, "transparent", "", 7, null]) {
      expect(isThemeColor(bad)).toBe(false);
    }
  });
});

describe("theme tokens", () => {
  it("are the tokens Go accepts in theme files", () => {
    // Go's table (internal/core/themes TestTokenTable), which also checks
    // that each token is set by both themes of tokens.css.
    const table = JSON.parse(tokenTable) as Record<string, Record<string, string>>;
    const fromGo = new Set(Object.values(table).flatMap((section) => Object.values(section)));
    expect([...THEME_TOKENS].sort()).toEqual([...fromGo].sort());
  });
});

describe("public/theme-init.js", () => {
  const run = () => new Function(themeInitSource)();

  it("applies the cached custom theme before first paint, with the same checks", () => {
    localStorage.setItem("postsonde.theme", "dark");
    localStorage.setItem(
      CUSTOM_THEME_STORAGE_KEY,
      JSON.stringify({
        id: "ocean.json",
        base: "light",
        vars: [
          { name: "--bg0", value: hex("001122") },
          { name: "--bg1", value: "url(https://evil.example/x)" },
          { name: "background", value: hex("000000") },
        ],
      }),
    );
    run();
    expect(root().dataset.theme).toBe("light");
    expect(root().style.getPropertyValue("--bg0")).toBe(hex("001122"));
    expect(root().style.getPropertyValue("--bg1")).toBe("");
    expect(root().style.getPropertyValue("background")).toBe("");
  });

  it("uses the preference without a cached custom theme", () => {
    localStorage.setItem("postsonde.theme", "dark");
    run();
    expect(root().dataset.theme).toBe("dark");
    expect(root().getAttribute("style")).toBeNull();
  });

  it("allows exactly the tokens customTheme.ts allows", () => {
    const pattern = /var name = (\/.*\/);/.exec(themeInitSource)?.[1];
    expect(pattern).toBeDefined();
    const re = new Function(`return ${pattern}`)() as RegExp;
    for (const name of THEME_TOKENS) expect(re.test(name), name).toBe(true);
    for (const name of ["--shadow", "--bg4", "--m-get2", "--tk-kw ", "--font-mono", "--accent-ink-2"]) {
      expect(re.test(name), name).toBe(false);
    }
  });
});
