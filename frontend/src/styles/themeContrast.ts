// Contrast warnings for custom themes (UI-11, UI-05): a theme is the user's
// file, so it is never refused for low contrast, but the settings screen
// says which pairs fall below WCAG AA. The built-in tokens underneath come
// from tokens.css itself, so the check sees what the app draws.
import tokensCss from "./tokens.css?raw";
import { checkContrast, parseThemeTokens, type ContrastFailure } from "./contrast";
import type { CustomThemeInput } from "./customTheme";

let builtIn: ReturnType<typeof parseThemeTokens> | null = null;

/**
 * The pairs of `theme` below WCAG AA, worst first, counting only pairs in
 * which the theme sets at least one of the two colours. Empty for an
 * unknown base.
 */
export function customThemeContrast(theme: CustomThemeInput): ContrastFailure[] {
  if (theme.base !== "dark" && theme.base !== "light") return [];
  builtIn ??= parseThemeTokens(tokensCss);
  const tokens: Record<string, string> = { ...builtIn[theme.base] };
  const set = new Set<string>();
  for (const v of theme.vars ?? []) {
    const name = v.name.replace(/^--/, "");
    tokens[name] = v.value;
    set.add(name);
  }
  if (set.size === 0) return [];
  return checkContrast(tokens)
    .filter((f) => set.has(f.fg) || f.on.split("/").some((part) => set.has(part)))
    .sort((a, b) => a.ratio / a.min - b.ratio / b.min);
}
