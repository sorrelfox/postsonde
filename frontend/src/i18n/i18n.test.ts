import { afterEach, describe, expect, it, vi } from "vitest";
import { en } from "./catalog";
import { formatMessage, formatParts, selectPlural } from "./format";
import {
  currentLocale,
  DEFAULT_LOCALE,
  errorText,
  formatDate,
  formatNumber,
  formatRelativeTime,
  isMessageKey,
  LOCALE_SYSTEM,
  PSEUDO_LOCALE,
  registerLocale,
  resolveLocale,
  setLocale,
  subscribeLocale,
  t,
  tParts,
} from "./index";
import { pseudoCatalogue, pseudoString } from "./pseudo";

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("formatMessage", () => {
  it("fills in placeholders, numbers in the locale's digits without grouping", () => {
    expect(formatMessage("en", "Theme: {theme}. Switch to {next}", { theme: "Dark", next: "System" })).toBe(
      "Theme: Dark. Switch to System",
    );
    expect(formatMessage("en", "Port {port}", { port: 8080 })).toBe("Port 8080");
    expect(formatMessage("de", "{n} s", { n: 2.5 })).toBe("2,5 s");
  });

  it("leaves a placeholder without a value as written", () => {
    expect(formatMessage("en", "Hello {name}")).toBe("Hello {name}");
  });

  it("keeps {{…}} as text: template variables are not placeholders", () => {
    expect(formatMessage("en", "Insert with {{$file PATH}} or {{name}}", { name: "x" })).toBe(
      "Insert with {{$file PATH}} or {{name}}",
    );
  });

  it("picks the plural form by the plural rules of the locale, exact counts first", () => {
    const message = { "=0": "No requests", one: "{count} request", other: "{count} requests" };
    expect(formatMessage("en", message, { count: 0 })).toBe("No requests");
    expect(formatMessage("en", message, { count: 1 })).toBe("1 request");
    expect(formatMessage("en", message, { count: 5 })).toBe("5 requests");
    // Polish: 2–4 are "few", 5 is "many"; a missing category falls back to other.
    const pl = { one: "{count} plik", few: "{count} pliki", other: "{count} plików" };
    expect(selectPlural("pl", pl, 3)).toBe("{count} pliki");
    expect(selectPlural("pl", pl, 5)).toBe("{count} plików");
  });

  it("keeps non-text values as parts, joining the text around them", () => {
    const node = { node: true };
    expect(formatParts("en", "From {folder}. Other {n} files", { folder: node, n: 2 })).toEqual([
      "From ",
      node,
      ". Other 2 files",
    ]);
    expect(formatParts("en", "")).toEqual([""]);
  });
});

describe("t", () => {
  it("returns the English message with its parameters", () => {
    expect(t("shell.statusBar.ready")).toBe("Ready");
    expect(t("shell.titleBar.theme.switch", { theme: "Dark", next: "System" })).toBe("Theme: Dark. Switch to System");
    expect(t("shell.autosave.failed", { count: 1 })).toBe("Save failed");
    expect(t("shell.autosave.failed", { count: 3 })).toBe("3 saves failed");
  });

  it("returns the key, and warns once, for a key the catalogue lacks", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => undefined);
    const missing = "shell.no.such.key" as Parameters<typeof t>[0];
    expect(t(missing)).toBe("shell.no.such.key");
    expect(t(missing)).toBe("shell.no.such.key");
    expect(warn).toHaveBeenCalledTimes(1);
    warn.mockRestore();
  });

  it("tells message keys from other text", () => {
    expect(isMessageKey("shell.statusBar.ready")).toBe(true);
    expect(isMessageKey("shell.nope")).toBe(false);
    expect(isMessageKey("Ready")).toBe(false);
  });

  it("gives parts for React nodes in placeholders", () => {
    const node = { el: "span" };
    expect(tParts("settings.dataDir.deleteBody", { folder: node })[1]).toBe(node);
  });
});

describe("locales", () => {
  it("resolves system to the first OS language the app has, by tag or by language, else English", () => {
    expect(resolveLocale(LOCALE_SYSTEM, ["fr-FR", "en-GB"])).toBe("en");
    expect(resolveLocale(LOCALE_SYSTEM, ["fr-FR"])).toBe(DEFAULT_LOCALE);
    expect(resolveLocale(LOCALE_SYSTEM, [])).toBe(DEFAULT_LOCALE);
    // The pseudo-locale is chosen only by name, never for "system".
    expect(resolveLocale(LOCALE_SYSTEM, [PSEUDO_LOCALE])).toBe("en");
    expect(resolveLocale("en-xa")).toBe(PSEUDO_LOCALE);
    expect(resolveLocale("de")).toBe(DEFAULT_LOCALE);
  });

  it("switches the language live, sets <html lang> and notifies subscribers", () => {
    const listener = vi.fn();
    const off = subscribeLocale(listener);
    expect(setLocale(PSEUDO_LOCALE)).toBe(PSEUDO_LOCALE);
    expect(currentLocale()).toBe(PSEUDO_LOCALE);
    expect(document.documentElement.lang).toBe(PSEUDO_LOCALE);
    expect(listener).toHaveBeenCalledTimes(1);
    expect(t("shell.statusBar.ready")).toBe(pseudoString("Ready"));
    setLocale(PSEUDO_LOCALE);
    expect(listener).toHaveBeenCalledTimes(1);
    setLocale("xx-unknown");
    expect(currentLocale()).toBe(DEFAULT_LOCALE);
    expect(listener).toHaveBeenCalledTimes(2);
    off();
  });

  it("falls back to English message by message, and uses a locale's error messages by code", () => {
    const off = registerLocale({
      tag: "fr",
      name: "Français",
      intl: "fr",
      messages: () => ({ shell: { "statusBar.ready": "Prêt" } }),
      errors: () => ({ "settings.invalid_value": "Valeur invalide : {field}", "settings.invalid_value.hint": "Corrigez {field}." }),
    });
    try {
      expect(setLocale(LOCALE_SYSTEM, ["fr-CA"])).toBe("fr");
      expect(t("shell.statusBar.ready")).toBe("Prêt");
      expect(t("shell.statusBar.label")).toBe("Status bar");
      expect(errorText("settings.invalid_value", "message", "ui.theme: bad", { field: "ui.theme" })).toBe(
        "Valeur invalide : ui.theme",
      );
      expect(errorText("settings.invalid_value", "hint", "", { field: "ui.theme" })).toBe("Corrigez ui.theme.");
      expect(errorText("settings.other", "message", "Go's text")).toBe("Go's text");
      expect(errorText("", "message", "Go's text")).toBe("Go's text");
    } finally {
      setLocale(DEFAULT_LOCALE);
      off();
    }
    // English keeps Go's text: en/errors.ts is empty.
    expect(errorText("settings.invalid_value", "message", "Go's text")).toBe("Go's text");
  });

  it("formats numbers, dates and relative times in the language in use", () => {
    expect(formatNumber(1234567.5)).toBe("1,234,567.5");
    expect(formatDate(Date.UTC(2026, 8, 22, 12), { dateStyle: "medium", timeZone: "UTC" })).toBe("Sep 22, 2026");
    expect(formatRelativeTime(-1, "day")).toBe("yesterday");
    expect(formatRelativeTime(3, "minute")).toBe("in 3 minutes");
  });
});

describe("pseudo-locale", () => {
  it("accents letters, keeps placeholders and {{…}}, and pads about 30% in brackets", () => {
    expect(pseudoString("Send request")).toBe("[Šéñð ŕéǫûéšţ one]");
    expect(pseudoString("Hi {name}, use {{$file PATH}}")).toMatch(/^\[Ĥî \{name\}, ûšé \{\{\$file PATH\}\}( \w+)+\]$/);
    const long = "Open a project folder to see its folders and requests here.";
    const pseudo = pseudoString(long);
    expect(pseudo.length).toBeGreaterThanOrEqual(Math.floor(long.length * 1.3));
    expect(pseudo.length).toBeLessThan(long.length * 1.5);
  });

  it("is built from the whole English catalogue, plural forms included", () => {
    const pseudo = pseudoCatalogue(en);
    for (const [ns, messages] of Object.entries(en)) {
      expect(Object.keys((pseudo as Record<string, object>)[ns])).toEqual(Object.keys(messages));
    }
    expect(pseudo.shell["autosave.failed"]).toEqual({
      one: pseudoString("Save failed"),
      other: pseudoString("{count} saves failed"),
    });
  });
});
