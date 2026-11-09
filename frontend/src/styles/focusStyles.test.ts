// Visible focus everywhere (UI-11): a stylesheet that removes the focus
// outline must draw another indicator for the same element when it has
// focus (box-shadow, border, background, outline or underline), or say in
// an "a11y:" comment why the element needs none (a container focused only
// from code, whose content shows focus itself). A static check of every
// stylesheet under src/, since jsdom does not render.
import { describe, expect, it } from "vitest";

const sheets = import.meta.glob<string>("../**/*.css", { query: "?raw", import: "default", eager: true });

interface Rule {
  selectors: string[];
  body: string;
  /** The comments inside the rule and right before it. */
  notes: string;
}

function parseRules(css: string): Rule[] {
  const rules: Rule[] = [];
  const re = /([^{}]*)\{([^{}]*)\}/g;
  for (const m of css.matchAll(re)) {
    const head = m[1] ?? "";
    const rawBody = m[2] ?? "";
    const notes = [...head.matchAll(/\/\*([\s\S]*?)\*\//g), ...rawBody.matchAll(/\/\*([\s\S]*?)\*\//g)].map((c) => c[1] ?? "").join(" ");
    const selectorText = head.replace(/\/\*[\s\S]*?\*\//g, "").trim();
    if (selectorText === "" || selectorText.startsWith("@")) continue;
    rules.push({
      selectors: selectorText.split(",").map((s) => s.trim()).filter((s) => s !== ""),
      body: rawBody.replace(/\/\*[\s\S]*?\*\//g, ""),
      notes,
    });
  }
  return rules;
}

const REMOVES_OUTLINE = /(^|[;\s])outline\s*:\s*(none|0)\s*(;|$)/;
const INDICATOR =
  /(^|[;\s])(box-shadow\s*:(?!\s*none)|border(-[a-z-]+)?\s*:|background(-color)?\s*:|outline\s*:(?!\s*(none|0)\s*(;|$))|outline-color\s*:|text-decoration(-[a-z]+)?\s*:(?!\s*none))/;
const FOCUS = /:focus(-visible|-within)?/g;

const strip = (selector: string) => selector.replace(FOCUS, "").replace(/\s+/g, " ").trim();
/** The class names of the last compound selector. */
const lastClasses = (selector: string) => {
  const last = selector.split(/[\s>+~]+/).pop() ?? "";
  return new Set([...last.matchAll(/\.([\w-]+)/g)].map((m) => m[1] ?? ""));
};
const covers = (a: Set<string>, b: Set<string>) => a.size > 0 && [...a].every((c) => b.has(c));

/** The selectors in `css` that remove the outline without a replacement. */
export function focusWithoutIndicator(css: string): string[] {
  const rules = parseRules(css);
  const focusRules = rules.filter((r) => INDICATOR.test(r.body) && r.selectors.some((s) => /:focus/.test(s)));
  const out: string[] = [];
  for (const rule of rules) {
    if (!REMOVES_OUTLINE.test(rule.body) || /a11y:/.test(rule.notes)) continue;
    for (const selector of rule.selectors) {
      const base = strip(selector);
      // The same rule draws the replacement for the focused state.
      if (/:focus/.test(selector) && INDICATOR.test(rule.body)) continue;
      const replaced = focusRules.some((f) =>
        f.selectors.some((s) => {
          if (!/:focus/.test(s)) return false;
          // A wrapper that shows focus of what is inside it.
          if (/:focus-within/.test(s)) return true;
          const other = strip(s);
          return other === base || covers(lastClasses(base), lastClasses(other)) || covers(lastClasses(other), lastClasses(base));
        }),
      );
      if (!replaced) out.push(selector);
    }
  }
  return out;
}

describe("focus indicators (UI-11)", () => {
  it("finds the stylesheets", () => {
    expect(Object.keys(sheets).length).toBeGreaterThan(20);
    expect(Object.keys(sheets).some((path) => path.endsWith("base.css"))).toBe(true);
  });

  it("flags an outline removed without a replacement", () => {
    expect(focusWithoutIndicator(".a { outline: none; }")).toEqual([".a"]);
    expect(focusWithoutIndicator(".a:focus-visible { outline: none; }")).toEqual([".a:focus-visible"]);
    expect(focusWithoutIndicator(".a:focus-visible { outline: none; box-shadow: var(--focus-ring); }")).toEqual([]);
    expect(focusWithoutIndicator(".a { outline: 0; } .a:focus { border-color: var(--accent); }")).toEqual([]);
    expect(focusWithoutIndicator(".a { outline: none; } .wrap:focus-within { box-shadow: var(--focus-ring); }")).toEqual([]);
    expect(focusWithoutIndicator(".a:focus { outline: none; /* a11y: focused from code only */ }")).toEqual([]);
    expect(focusWithoutIndicator(".a { outline-offset: 0; }")).toEqual([]);
  });

  it("keeps a visible focus indicator wherever a stylesheet removes the outline", () => {
    const failures = Object.entries(sheets).flatMap(([path, css]) =>
      focusWithoutIndicator(css).map((selector) => `${path.replace(/^\.\.\//, "")}: ${selector}`),
    );
    expect(failures).toEqual([]);
  });

  it("gives keyboard focus the accent outline by default", () => {
    const base = Object.entries(sheets).find(([path]) => /(^\.\/|styles\/)base\.css$/.test(path))?.[1] ?? "";
    expect(base).toMatch(/:focus-visible\s*\{\s*outline:\s*2px solid var\(--accent\);/);
  });
});
