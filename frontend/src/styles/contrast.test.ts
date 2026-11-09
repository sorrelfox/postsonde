// WCAG AA contrast of the built-in themes (UI-11): every token pair the app
// draws (CONTRAST_RULES) is checked in both themes, straight from tokens.css.
import { describe, expect, it } from "vitest";
import tokensCss from "./tokens.css?raw";
import {
  AA_NON_TEXT,
  AA_TEXT,
  checkContrast,
  composite,
  contrastRatio,
  CONTRAST_RULES,
  parseColor,
  parseThemeTokens,
  resolveSurface,
} from "./contrast";
import { customThemeContrast } from "./themeContrast";

// Built at run time: colour literals belong in tokens.css only.
const hex = (digits: string) => `#${digits}`;
const fn = (name: string, args: string) => `${name}(${args})`;

describe("contrast maths", () => {
  it("parses the colour forms tokens.css uses", () => {
    expect(parseColor(hex("fff"))).toEqual({ r: 255, g: 255, b: 255, a: 1 });
    expect(parseColor(hex("0e1013"))).toEqual({ r: 14, g: 16, b: 19, a: 1 });
    expect(parseColor(hex("00000080"))?.a).toBeCloseTo(0.5, 2);
    expect(parseColor(fn("rgba", "79, 209, 165, 0.14"))).toEqual({ r: 79, g: 209, b: 165, a: 0.14 });
    expect(parseColor(fn("rgb", "1 2 3"))).toEqual({ r: 1, g: 2, b: 3, a: 1 });
    expect(parseColor("var(--accent)")).toBeNull();
    expect(parseColor("0 0 0 2px")).toBeNull();
  });

  it("computes WCAG ratios", () => {
    const white = parseColor(hex("ffffff"))!;
    const black = parseColor(hex("000000"))!;
    expect(contrastRatio(black, white)).toBeCloseTo(21, 5);
    expect(contrastRatio(white, white)).toBeCloseTo(1, 5);
    // The WCAG reference grey: 4.54:1 on white.
    expect(contrastRatio(parseColor(hex("767676"))!, white)).toBeCloseTo(4.54, 2);
    // Translucent colours are drawn over the background first.
    expect(composite(parseColor(fn("rgba", "0, 0, 0, 0.5"))!, white)).toEqual({ r: 127.5, g: 127.5, b: 127.5, a: 1 });
  });
});

describe("built-in themes (tokens.css)", () => {
  const themes = parseThemeTokens(tokensCss);

  it("reads both theme blocks", () => {
    for (const tokens of [themes.dark, themes.light]) {
      for (const name of ["bg0", "bg1", "bg2", "bg3", "text", "text2", "text3", "accent", "sel"]) {
        expect(parseColor(tokens[name] ?? ""), name).not.toBeNull();
      }
    }
  });

  it("checks every colour token of both themes in at least one pair", () => {
    const checked = new Set(CONTRAST_RULES.flatMap((r) => [r.fg, ...r.on.flatMap((s) => s.split("/"))]));
    // Lines and the scrim are decorative (dividers, the dim behind dialogs), not text or control boundaries.
    const decorative = new Set(["line", "line2", "scrim"]);
    for (const tokens of [themes.dark, themes.light]) {
      const colours = Object.entries(tokens)
        .filter(([, value]) => parseColor(value) !== null)
        .map(([name]) => name)
        .filter((name) => !decorative.has(name));
      expect(colours.filter((name) => !checked.has(name))).toEqual([]);
    }
  });

  for (const theme of ["dark", "light"] as const) {
    it(`${theme}: every pair meets WCAG AA (${AA_TEXT}:1 text, ${AA_NON_TEXT}:1 focus ring and UI)`, () => {
      const failures = checkContrast(themes[theme]).map(
        (f) => `${f.fg} on ${f.on}: ${f.ratio.toFixed(2)}:1 < ${f.min}:1 (${f.use})`,
      );
      expect(failures).toEqual([]);
    });

    it(`${theme}: muted text stays apart from secondary text`, () => {
      // Raising --text3 to AA must not make it the same as --text2.
      const bg = resolveSurface(themes[theme], "bg1");
      const text2 = contrastRatio(parseColor(themes[theme].text2 ?? "")!, bg);
      const text3 = contrastRatio(parseColor(themes[theme].text3 ?? "")!, bg);
      expect(text2 / text3).toBeGreaterThan(1.2);
    });
  }

  it("draws the focus ring with the accent, not its faint tint", () => {
    expect(tokensCss).toMatch(/--focus-ring:\s*0 0 0 2px var\(--accent\);/);
  });
});

describe("custom theme contrast warnings", () => {
  it("lists the pairs a custom theme breaks, only where the theme set a colour", () => {
    const warnings = customThemeContrast({
      id: "ocean.json",
      base: "dark",
      vars: [
        { name: "--text3", value: hex("30343a") },
        { name: "--accent", value: hex("4fd1a5") },
      ],
    });
    expect(warnings.length).toBeGreaterThan(0);
    expect(warnings.every((w) => w.fg === "text3" || w.on.split("/").includes("text3"))).toBe(true);
    expect(warnings[0]?.fg).toBe("text3");
  });

  it("has nothing to say about a theme that keeps its contrast", () => {
    expect(customThemeContrast({ id: "a.json", base: "light", vars: [{ name: "--text", value: hex("000000") }] })).toEqual([]);
    expect(customThemeContrast({ id: "b.json", base: "light", vars: null })).toEqual([]);
    expect(customThemeContrast({ id: "c.json", base: "sepia", vars: [] })).toEqual([]);
  });
});
