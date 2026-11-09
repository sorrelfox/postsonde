import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  applyTheme,
  currentTheme,
  resetThemeForTests,
  resolveTheme,
  setSystemDark,
  storedThemePreference,
  subscribeTheme,
  THEME_STORAGE_KEY,
} from "./theme";

// A controllable prefers-color-scheme: jsdom has no matchMedia.
function fakeMatchMedia(initiallyDark: boolean) {
  let dark = initiallyDark;
  const listeners = new Set<(e: MediaQueryListEvent) => void>();
  const mql = {
    get matches() {
      return dark;
    },
    media: "(prefers-color-scheme: dark)",
    addEventListener: (_: string, l: (e: MediaQueryListEvent) => void) => listeners.add(l),
    removeEventListener: (_: string, l: (e: MediaQueryListEvent) => void) => listeners.delete(l),
  };
  vi.stubGlobal("matchMedia", () => mql);
  return {
    set(next: boolean) {
      dark = next;
      for (const l of [...listeners]) l({ matches: next } as MediaQueryListEvent);
    },
  };
}

beforeEach(() => {
  localStorage.clear();
  delete document.documentElement.dataset.theme;
  resetThemeForTests();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("theme", () => {
  it("applies a fixed theme and caches the preference", () => {
    fakeMatchMedia(true);
    expect(applyTheme("light")).toBe("light");
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(currentTheme()).toBe("light");
    expect(localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");
    expect(storedThemePreference()).toBe("light");
  });

  it("falls back to 'system' for a missing or unknown cached preference", () => {
    expect(storedThemePreference()).toBe("system");
    localStorage.setItem(THEME_STORAGE_KEY, "neon");
    expect(storedThemePreference()).toBe("system");
  });

  it("resolves 'system' from prefers-color-scheme and follows its changes", () => {
    const media = fakeMatchMedia(false);
    expect(applyTheme("system")).toBe("light");
    media.set(true);
    expect(currentTheme()).toBe("dark");
    media.set(false);
    expect(currentTheme()).toBe("light");
  });

  it("ignores OS changes while a fixed theme is chosen, and uses the latest when back on 'system'", () => {
    const media = fakeMatchMedia(false);
    applyTheme("light");
    media.set(true);
    expect(currentTheme()).toBe("light");
    expect(applyTheme("system")).toBe("dark");
  });

  it("takes the OS theme reported by Go when it comes after prefers-color-scheme", () => {
    const media = fakeMatchMedia(false);
    applyTheme("system");
    setSystemDark(true);
    expect(currentTheme()).toBe("dark");
    expect(resolveTheme("system")).toBe("dark");
    media.set(false);
    expect(currentTheme()).toBe("light");
  });

  it("notifies subscribers only when the applied theme changes", () => {
    fakeMatchMedia(false);
    const listener = vi.fn();
    const stop = subscribeTheme(listener);
    applyTheme("dark");
    applyTheme("dark");
    expect(listener).toHaveBeenCalledTimes(1);
    applyTheme("system");
    expect(listener).toHaveBeenCalledTimes(2);
    applyTheme("light");
    expect(listener).toHaveBeenCalledTimes(2);
    stop();
    applyTheme("dark");
    expect(listener).toHaveBeenCalledTimes(2);
  });
});
