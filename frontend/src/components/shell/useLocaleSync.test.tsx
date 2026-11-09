import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { DEFAULT_LOCALE, LOCALE_CACHE_KEY, PSEUDO_LOCALE, currentLocale, setLocale } from "../../i18n";
import { pseudoString } from "../../i18n/pseudo";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { StatusBar } from "./StatusBar";
import { useLocaleSync } from "./useLocaleSync";

function Synced() {
  useLocaleSync();
  return <StatusBar />;
}

function renderSynced(): Stores {
  const stores = createStores();
  render(
    <StoreProvider stores={stores}>
      <Synced />
    </StoreProvider>,
  );
  return stores;
}

beforeEach(() => {
  localStorage.clear();
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("useLocaleSync (UI-10)", () => {
  it("applies settings.locale live, whoever changed it, and caches it for the next start", () => {
    const stores = renderSynced();
    expect(screen.getByRole("contentinfo").getAttribute("aria-label")).toBe("Status bar");

    act(() => stores.settings.dispatch({ type: "settings/load", settings: { locale: PSEUDO_LOCALE } }));
    expect(currentLocale()).toBe(PSEUDO_LOCALE);
    expect(screen.getByRole("contentinfo").getAttribute("aria-label")).toBe(pseudoString("Status bar"));
    expect(screen.getByText(pseudoString("Ready"))).toBeTruthy();
    expect(localStorage.getItem(LOCALE_CACHE_KEY)).toBe(PSEUDO_LOCALE);

    act(() => stores.settings.dispatch({ type: "settings/load", settings: { locale: "system" } }));
    expect(currentLocale()).toBe(DEFAULT_LOCALE);
    expect(screen.getByText("Ready")).toBeTruthy();
    expect(localStorage.getItem(LOCALE_CACHE_KEY)).toBeNull();
  });

  it("follows the OS language while the setting is system", () => {
    renderSynced();
    setLocale(PSEUDO_LOCALE);
    // A languagechange event resolves "system" again (never to the pseudo-locale).
    act(() => {
      window.dispatchEvent(new Event("languagechange"));
    });
    expect(currentLocale()).toBe(DEFAULT_LOCALE);
  });
});
