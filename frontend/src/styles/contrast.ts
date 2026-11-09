// WCAG 2.x colour contrast (UI-11): relative luminance and contrast ratio
// of sRGB colours as tokens.css writes them (hex digits, or the rgb and
// rgba functions), and the token pairs the app draws. Used by the token
// contrast test (contrast.test.ts) and by the custom theme warnings
// (themeContrast.ts). No React, no DOM.

/** An sRGB colour, channels 0 to 255, alpha 0 to 1. */
export interface Rgba {
  r: number;
  g: number;
  b: number;
  a: number;
}

/** WCAG AA minimums. */
export const AA_TEXT = 4.5;
/** Large text (18pt, or 14pt bold), UI components and graphical objects (focus rings, borders that identify a control). */
export const AA_NON_TEXT = 3;

/** Parses a hex colour (3, 4, 6 or 8 digits) or the rgb and rgba functions; null for anything else. */
export function parseColor(input: string): Rgba | null {
  const text = input.trim().toLowerCase();
  const hex = /^#([0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/.exec(text);
  if (hex !== null) {
    let digits = hex[1] ?? "";
    if (digits.length <= 4) digits = [...digits].map((d) => d + d).join("");
    const n = (i: number) => parseInt(digits.slice(i, i + 2), 16);
    return { r: n(0), g: n(2), b: n(4), a: digits.length === 8 ? n(6) / 255 : 1 };
  }
  const fn = /^rgba?\s*\(([^)]*)\)$/.exec(text);
  if (fn !== null) {
    const parts = (fn[1] ?? "")
      .split(/[\s,/]+/)
      .filter((p) => p !== "")
      .map((p) => (p.endsWith("%") ? (parseFloat(p) / 100) * 255 : parseFloat(p)));
    if (parts.length < 3 || parts.length > 4 || parts.some((p) => Number.isNaN(p))) return null;
    const [r = 0, g = 0, b = 0] = parts;
    let a = parts[3] ?? 1;
    // An alpha written as a percentage was scaled to 0..255 above.
    if (fn[1]?.trim().split(/[\s,/]+/)[3]?.endsWith("%") === true) a /= 255;
    return { r, g, b, a };
  }
  return null;
}

/** `top` drawn over the opaque `bottom`. */
export function composite(top: Rgba, bottom: Rgba): Rgba {
  const a = top.a;
  return {
    r: top.r * a + bottom.r * (1 - a),
    g: top.g * a + bottom.g * (1 - a),
    b: top.b * a + bottom.b * (1 - a),
    a: 1,
  };
}

/** WCAG relative luminance of an opaque colour. */
export function relativeLuminance(c: Rgba): number {
  const channel = (v: number) => {
    const s = v / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(c.r) + 0.7152 * channel(c.g) + 0.0722 * channel(c.b);
}

/** The WCAG contrast ratio (1 to 21) of `fg` on `bg`. A translucent `fg` is composited over `bg` first. */
export function contrastRatio(fg: Rgba, bg: Rgba): number {
  const top = fg.a < 1 ? composite(fg, bg) : fg;
  const a = relativeLuminance(top);
  const b = relativeLuminance(bg);
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
}

/**
 * The colour tokens of each theme block of tokens.css: the custom
 * properties of the `[data-theme="dark"]` and `[data-theme="light"]` rules,
 * as written (so `var()` references stay unresolved).
 */
export function parseThemeTokens(css: string): Record<"dark" | "light", Record<string, string>> {
  const clean = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const block = (selector: string): Record<string, string> => {
    const at = clean.indexOf(selector);
    if (at < 0) throw new Error(`tokens: no ${selector} rule`);
    const open = clean.indexOf("{", at);
    const close = clean.indexOf("}", open);
    const out: Record<string, string> = {};
    for (const m of clean.slice(open + 1, close).matchAll(/--([\w-]+)\s*:\s*([^;]+);/g)) {
      out[m[1] ?? ""] = (m[2] ?? "").trim();
    }
    return out;
  };
  return { dark: block('[data-theme="dark"]'), light: block('[data-theme="light"]') };
}

/** A background: a token, or a translucent token over an opaque one ("sel/bg1"). */
export type Surface = string;

/** Resolves a surface ("bg1" or "sel/bg1") in `tokens` to an opaque colour. */
export function resolveSurface(tokens: Readonly<Record<string, string>>, surface: Surface): Rgba {
  const [top, base] = surface.split("/");
  const color = (name: string | undefined): Rgba => {
    const value = name === undefined ? undefined : tokens[name];
    const parsed = value === undefined ? null : parseColor(value);
    if (parsed === null) throw new Error(`tokens: --${name ?? "?"} is not a colour (${value ?? "missing"})`);
    return parsed;
  };
  if (base === undefined) return color(top);
  return composite(color(top), color(base));
}

/** One pair a theme must meet: `fg` on each of `on`, at least `min`. */
export interface ContrastRule {
  fg: string;
  on: readonly Surface[];
  min: number;
  /** Why the pair matters, for the test's failure message (English, never shown in the UI). */
  use: string;
}

/** Text of any weight: on every surface, the selection and the accent tints. */
const TEXT_SURFACES = ["bg0", "bg1", "bg2", "bg3", "sel/bg0", "sel/bg1", "accent-dim/bg0", "accent-dim/bg1"];
/** Muted text: never on the accent tints. */
const MUTED_SURFACES = ["bg0", "bg1", "bg2", "bg3", "sel/bg0", "sel/bg1"];
/** Coloured text (methods, statuses, the accent as text): the panels, hovers and selected rows. */
const COLOURED_SURFACES = ["bg0", "bg1", "bg2", "sel/bg0", "sel/bg1"];
/** Syntax colours: the editor (bg1), panels around it and the selection. */
const CODE_SURFACES = ["bg0", "bg1", "bg2", "sel/bg1"];

/**
 * The token pairs the app draws, with the WCAG AA minimum of each (UI-11).
 * docs/frontend.md#accessibility explains the choice of surfaces.
 */
// i18n-ignore-start: `use` is for developers only (contrast.test.ts's failure
// messages); the settings screen's contrast warning names the tokens, never this.
export const CONTRAST_RULES: readonly ContrastRule[] = [
  { fg: "text", on: TEXT_SURFACES, min: AA_TEXT, use: "primary text" },
  { fg: "text2", on: TEXT_SURFACES, min: AA_TEXT, use: "secondary text" },
  { fg: "text3", on: MUTED_SURFACES, min: AA_TEXT, use: "muted text, placeholders, counts" },
  ...["accent", "ok", "warn", "bad"].map((fg) => ({ fg, on: COLOURED_SURFACES, min: AA_TEXT, use: "status and accent text" })),
  { fg: "accent", on: ["accent-dim/bg0", "accent-dim/bg1"], min: AA_TEXT, use: "accent text on an accent tint (active buttons)" },
  ...["m-get", "m-post", "m-put", "m-patch", "m-del", "m-gql", "m-dir", "m-ws"].map((fg) => ({
    fg,
    on: COLOURED_SURFACES,
    min: AA_TEXT,
    use: "method and kind badges",
  })),
  ...[
    "tk-kw",
    "tk-field",
    "tk-arg",
    "tk-var",
    "tk-str",
    "tk-num",
    "tk-type",
    "tk-dir",
    "tk-punct",
    "tk-cm",
    "tk-key",
    "tk-bool",
    "tk-plain",
    "tk-err",
  ].map((fg) => ({ fg, on: CODE_SURFACES, min: AA_TEXT, use: "syntax colours" })),
  { fg: "accent-ink", on: ["accent"], min: AA_TEXT, use: "text on an accent button" },
  { fg: "bg0", on: ["bad"], min: AA_TEXT, use: "text on a red badge (console error count)" },
  { fg: "accent", on: ["bg0", "bg1", "bg2", "bg3"], min: AA_NON_TEXT, use: "focus ring, focused field border, selected tab underline" },
];
// i18n-ignore-end

/** A pair below its minimum. */
export interface ContrastFailure {
  fg: string;
  on: Surface;
  ratio: number;
  min: number;
  use: string;
}

/** The pairs of `rules` that `tokens` fails; empty when all pass. */
export function checkContrast(
  tokens: Readonly<Record<string, string>>,
  rules: readonly ContrastRule[] = CONTRAST_RULES,
): ContrastFailure[] {
  const out: ContrastFailure[] = [];
  for (const rule of rules) {
    const fg = parseColor(tokens[rule.fg] ?? "");
    if (fg === null) continue;
    for (const on of rule.on) {
      let bg: Rgba;
      try {
        bg = resolveSurface(tokens, on);
      } catch {
        continue;
      }
      const ratio = contrastRatio(fg, bg);
      if (ratio < rule.min) out.push({ fg: rule.fg, on, ratio, min: rule.min, use: rule.use });
    }
  }
  return out;
}
