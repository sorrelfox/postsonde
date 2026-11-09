import { beforeEach, describe, expect, it, vi } from "vitest";
import { applyTheme, currentTheme, resetThemeForTests } from "../styles/theme";
import { emit, listenerCount, resetFakeWails } from "../test/fakeWails";
import { followOSTheme, THEME_CHANGED_EVENT } from "./osTheme";

const system = vi.hoisted(() => ({
  IsLinux: vi.fn(() => false),
  IsDarkMode: vi.fn(() => Promise.resolve(false)),
}));

vi.mock("@wailsio/runtime", async () => ({
  ...(await import("../test/fakeWails")).fakeRuntime,
  System: system,
}));

beforeEach(() => {
  resetFakeWails();
  resetThemeForTests();
  localStorage.clear();
  delete document.documentElement.dataset.theme;
  system.IsLinux.mockReset().mockReturnValue(false);
  system.IsDarkMode.mockReset().mockResolvedValue(false);
});

describe("followOSTheme", () => {
  it("applies OS theme changes reported by Go while the preference is 'system'", () => {
    applyTheme("system");
    const stop = followOSTheme();
    emit(THEME_CHANGED_EVENT, { dark: true });
    expect(currentTheme()).toBe("dark");
    emit(THEME_CHANGED_EVENT, { dark: false });
    expect(currentTheme()).toBe("light");
    stop();
    expect(listenerCount()).toBe(0);
  });

  it("leaves a fixed theme alone", () => {
    applyTheme("light");
    followOSTheme();
    emit(THEME_CHANGED_EVENT, { dark: true });
    expect(currentTheme()).toBe("light");
  });

  it("does not ask Go for the OS theme outside Linux", () => {
    followOSTheme();
    expect(system.IsDarkMode).not.toHaveBeenCalled();
  });

  it("takes a dark answer from Go at startup on Linux", async () => {
    system.IsLinux.mockReturnValue(true);
    system.IsDarkMode.mockResolvedValue(true);
    applyTheme("system");
    expect(currentTheme()).toBe("light");
    followOSTheme();
    await vi.waitFor(() => expect(currentTheme()).toBe("dark"));
  });

  it("keeps prefers-color-scheme when Go answers light on Linux", async () => {
    system.IsLinux.mockReturnValue(true);
    vi.stubGlobal("matchMedia", () => ({ matches: true, addEventListener: () => {}, removeEventListener: () => {} }));
    try {
      applyTheme("system");
      followOSTheme();
      await Promise.resolve();
      await Promise.resolve();
      expect(system.IsDarkMode).toHaveBeenCalled();
      expect(currentTheme()).toBe("dark");
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
