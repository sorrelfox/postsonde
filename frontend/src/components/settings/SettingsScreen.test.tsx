import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Location: vi.fn<() => Promise<unknown>>(),
  OpenFile: vi.fn<() => Promise<void>>(),
  RevealFile: vi.fn<() => Promise<void>>(),
  CABundles: vi.fn<() => Promise<unknown>>(),
  AddCABundle: vi.fn<() => Promise<unknown>>(),
  RemoveCABundle: vi.fn<(path: string) => Promise<unknown>>(),
  ClientCerts: vi.fn<() => Promise<unknown>>(),
  AllowedDirs: vi.fn<() => Promise<unknown>>(),
}));

const workspaceService = vi.hoisted(() => ({
  Recent: vi.fn<() => Promise<unknown>>(),
  RemoveRecent: vi.fn<(path: string) => Promise<unknown>>(),
  ClearRecent: vi.fn<() => Promise<unknown>>(),
  Reveal: vi.fn<(path: string) => Promise<unknown>>(),
  Open: vi.fn<(path: string) => Promise<unknown>>(),
}));

const dataDirService = vi.hoisted(() => ({
  Info: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/datadir", () => ({
  DataDirService: dataDirService,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: workspaceService,
}));

import { flushSettings, setSettingsPersistence, setSettingsProblems } from "../../commands/settings";
import { emit } from "../../test/fakeWails";
import type { AppError } from "../../lib/errors";
import { localStorageSettings, UI_SETTINGS_STORAGE_KEY, type SettingsPersistence } from "../../lib/persist";
import { createStores, initialSettingsState, StoreProvider, type Stores } from "../../stores";
import { shortenPath, SettingsScreen, type SettingsScreenProps } from "./SettingsScreen";

function renderScreen(props: SettingsScreenProps = {}, stores: Stores = createStores()) {
  render(
    <StoreProvider stores={stores}>
      <SettingsScreen {...props} />
    </StoreProvider>,
  );
  return stores;
}

const saved = () => JSON.parse(localStorage.getItem(UI_SETTINGS_STORAGE_KEY) ?? "{}") as Record<string, unknown>;
const timeoutField = () => screen.getByRole("textbox", { name: "Default timeout" });

beforeEach(() => {
  localStorage.clear();
  setSettingsPersistence(localStorageSettings());
  service.Location.mockReset().mockResolvedValue({
    path: "/Users/ann/Library/Application Support/PostSonde/settings.json",
    exists: true,
    dataDir: "/Users/ann/Library/Application Support/PostSonde",
  });
  // The data directory rows have their own tests (DataDirectory.test.tsx);
  // here they fall back to Location's dataDir.
  dataDirService.Info.mockReset().mockRejectedValue(new Error("not in this test"));
  service.OpenFile.mockReset().mockResolvedValue(undefined);
  service.RevealFile.mockReset().mockResolvedValue(undefined);
  service.CABundles.mockReset().mockResolvedValue([]);
  service.AddCABundle.mockReset().mockResolvedValue([]);
  service.RemoveCABundle.mockReset().mockResolvedValue([]);
  // The client certificate rows have their own tests (ClientCertificates.test.tsx).
  service.ClientCerts.mockReset().mockResolvedValue([]);
  service.AllowedDirs.mockReset().mockResolvedValue([]);
  workspaceService.Recent.mockReset().mockResolvedValue([]);
  workspaceService.RemoveRecent.mockReset().mockResolvedValue(undefined);
  workspaceService.ClearRecent.mockReset().mockResolvedValue(undefined);
  workspaceService.Reveal.mockReset().mockResolvedValue(undefined);
  workspaceService.Open.mockReset();
});

afterEach(async () => {
  await flushSettings();
  setSettingsProblems([]);
  vi.useRealTimers();
});

describe("SettingsScreen", () => {
  it("lists the pages and switches between them", () => {
    renderScreen();
    const nav = screen.getByRole("navigation", { name: "Settings" });
    expect(within(nav).getAllByRole("button").map((b) => b.textContent)).toEqual(["General", "Editor", "Network", "Shortcuts", "Privacy"]);
    expect(within(nav).getByRole("button", { name: "General" }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("heading", { level: 1, name: "General" })).toBeTruthy();

    fireEvent.click(within(nav).getByRole("button", { name: "Network" }));
    expect(screen.getByRole("heading", { level: 1, name: "Network" })).toBeTruthy();
    expect(timeoutField()).toBeTruthy();

    fireEvent.click(within(nav).getByRole("button", { name: "Privacy" }));
    expect(screen.getByText("What PostSonde never does")).toBeTruthy();
    expect(screen.getByText("No telemetry, crash reports or usage statistics")).toBeTruthy();
    expect(screen.getByText("Allowed directories for file access")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Add folder…" })).toBeTruthy();
  });

  it("applies and saves the theme as it is picked", async () => {
    const stores = renderScreen();
    const group = screen.getByRole("group", { name: "Theme" });
    expect(within(group).getByRole("button", { name: "System" }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(within(group).getByRole("button", { name: "Dark" }));
    await waitFor(() => expect(stores.settings.getState().theme).toBe("dark"));
    expect(saved()).toMatchObject({ theme: "dark" });
    expect(within(group).getByRole("button", { name: "Dark" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("shows where the files are and reveals or opens the settings file", async () => {
    renderScreen();
    expect(await screen.findByText("…/PostSonde/settings.json")).toBeTruthy();
    expect(screen.getByText("~/Library/Application Support/PostSonde")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Reveal" }));
    fireEvent.click(screen.getByRole("button", { name: "Open settings file" }));
    await waitFor(() => expect(service.OpenFile).toHaveBeenCalledTimes(1));
    expect(service.RevealFile).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("says why the settings file could not be opened", async () => {
    service.OpenFile.mockRejectedValue(
      Object.assign(new Error("raw"), {
        cause: {
          kind: "unsupported",
          code: "settings.open_failed",
          message: "The settings file cannot be opened here.",
          hint: "This run of PostSonde keeps its settings in memory.",
          causes: [],
        },
      }),
    );
    renderScreen();
    fireEvent.click(screen.getByRole("button", { name: "Open settings file" }));
    expect((await screen.findByRole("alert")).textContent).toContain("The settings file cannot be opened here.");
  });

  describe("default timeout", () => {
    it("saves seconds as milliseconds, shortly after typing and at once on Enter", async () => {
      vi.useFakeTimers();
      const stores = renderScreen({ initialPage: "network" });
      const field = timeoutField();
      expect((field as HTMLInputElement).value).toBe("");

      fireEvent.focus(field);
      fireEvent.change(field, { target: { value: "30" } });
      expect(stores.settings.getState().defaultTimeoutMs).toBe(0);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(400);
      });
      expect(stores.settings.getState().defaultTimeoutMs).toBe(30000);
      expect(saved()).toMatchObject({ defaultTimeoutMs: 30000 });

      fireEvent.change(field, { target: { value: "2.5" } });
      fireEvent.keyDown(field, { key: "Enter" });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(stores.settings.getState().defaultTimeoutMs).toBe(2500);

      // Empty is no timeout.
      fireEvent.change(field, { target: { value: "" } });
      fireEvent.blur(field);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(0);
      });
      expect(stores.settings.getState().defaultTimeoutMs).toBe(0);
      expect(saved()).toMatchObject({ defaultTimeoutMs: 0 });
    });

    it("shows an invalid value's error next to the field and does not save it", async () => {
      const stores = renderScreen({ initialPage: "network" });
      const field = timeoutField();
      fireEvent.focus(field);
      fireEvent.change(field, { target: { value: "-5" } });
      fireEvent.blur(field);

      const error = screen.getByRole("alert");
      expect(error.textContent).toBe("Enter at least 1 second, or leave it empty for no timeout.");
      expect(field.getAttribute("aria-invalid")).toBe("true");
      expect(field.getAttribute("aria-describedby")).toBe(error.id);
      expect((field as HTMLInputElement).value).toBe("-5");
      await flushSettings();
      expect(stores.settings.getState().defaultTimeoutMs).toBe(0);
      expect(saved()).not.toHaveProperty("defaultTimeoutMs");

      // Fixing it clears the error.
      fireEvent.focus(field);
      fireEvent.change(field, { target: { value: "10" } });
      expect(screen.queryByRole("alert")).toBeNull();
      expect(field.getAttribute("aria-invalid")).toBeNull();
    });

    it("shows the error from the settings file when it refuses the value", async () => {
      const refusal: AppError = {
        kind: "invalid_argument",
        code: "settings.invalid_value",
        message: 'The setting "network.defaultTimeoutMs" has an invalid value, 5000.',
        hint: "",
        details: {},
        causes: [],
      };
      const refusing: SettingsPersistence = {
        load: () => Promise.resolve({}),
        save: () => Promise.reject(refusal),
      };
      setSettingsPersistence(refusing);
      const stores = renderScreen({ initialPage: "network" });
      const field = timeoutField();
      fireEvent.change(field, { target: { value: "5" } });
      fireEvent.keyDown(field, { key: "Enter" });

      expect((await screen.findByRole("alert")).textContent).toContain('"network.defaultTimeoutMs" has an invalid value');
      expect(stores.settings.getState().defaultTimeoutMs).toBe(0);
    });

    it("follows hand edits of the file while it is not being edited", () => {
      const stores = renderScreen({ initialPage: "network" });
      act(() => stores.settings.dispatch({ type: "settings/load", settings: { defaultTimeoutMs: 45000 } }));
      expect((timeoutField() as HTMLInputElement).value).toBe("45");

      fireEvent.focus(timeoutField());
      fireEvent.change(timeoutField(), { target: { value: "1" } });
      act(() => stores.settings.dispatch({ type: "settings/load", settings: { defaultTimeoutMs: 60000 } }));
      expect((timeoutField() as HTMLInputElement).value).toBe("1");
    });
  });

  describe("response body limit (HTTP-37)", () => {
    const limitField = () => screen.getByRole("textbox", { name: "Response body limit" }) as HTMLInputElement;

    it("shows the default and saves whole MiB; refuses a value out of range", async () => {
      const stores = renderScreen({ initialPage: "network" });
      expect(limitField().value).toBe("512");

      fireEvent.focus(limitField());
      fireEvent.change(limitField(), { target: { value: "2048" } });
      fireEvent.keyDown(limitField(), { key: "Enter" });
      await waitFor(() => expect(stores.settings.getState().maxResponseBodyMb).toBe(2048));
      expect(saved()).toMatchObject({ maxResponseBodyMb: 2048 });

      fireEvent.change(limitField(), { target: { value: "0" } });
      fireEvent.blur(limitField());
      expect(screen.getByRole("alert").textContent).toBe("Enter a whole number of MiB from 1 to 16384.");
      await flushSettings();
      expect(stores.settings.getState().maxResponseBodyMb).toBe(2048);
    });
  });

  describe("TLS verification (HTTP-23)", () => {
    it("is on by default; turning it off saves it and warns", async () => {
      const stores = renderScreen({ initialPage: "network" });
      const group = screen.getByRole("group", { name: "Verify TLS certificates" });
      const verify = within(group).getByRole("button", { name: "Verify" });
      const dont = within(group).getByRole("button", { name: "Don't verify" });
      expect(verify.getAttribute("aria-pressed")).toBe("true");
      expect(screen.queryByText(/any certificate is accepted/)).toBeNull();

      fireEvent.click(dont);
      await waitFor(() => expect(stores.settings.getState().verifyTls).toBe(false));
      expect(saved()).toMatchObject({ verifyTls: false });
      expect(dont.getAttribute("aria-pressed")).toBe("true");
      expect(screen.getByText(/any certificate is accepted/)).toBeTruthy();

      fireEvent.click(verify);
      await waitFor(() => expect(stores.settings.getState().verifyTls).toBe(true));
      expect(saved()).toMatchObject({ verifyTls: true });
    });
  });

  describe("custom CA certificates (NET-04)", () => {
    const corp = {
      path: "/Users/ann/certs/corp-root.pem",
      certificates: [
        { subject: "CN=Corp Root", issuer: "CN=Corp Root", notBefore: "2025-01-01T00:00:00Z", notAfter: "2030-01-01T00:00:00Z", expired: false, isCa: true, sha256: "ab" },
        { subject: "CN=Corp Issuing", issuer: "CN=Corp Root", notBefore: "2025-01-01T00:00:00Z", notAfter: "2028-06-30T00:00:00Z", expired: false, isCa: true, sha256: "cd" },
      ],
      warnings: [],
      error: null,
    };
    const old = {
      path: "/Users/ann/certs/old.pem",
      certificates: [
        { subject: "CN=Old", issuer: "CN=Old", notBefore: "2020-01-01T00:00:00Z", notAfter: "2021-01-01T00:00:00Z", expired: true, isCa: true, sha256: "ef" },
      ],
      warnings: [{ code: "tlsconf.ca_expired", message: 'The certificate "Old" expired on 2021-01-01.', hint: "Get a current copy." }],
      error: null,
    };
    const networkPage = () => fireEvent.click(screen.getByRole("button", { name: "Network" }));

    it("lists none, adds a file through the dialog and removes it", async () => {
      renderScreen();
      networkPage();
      await waitFor(() => expect(service.CABundles).toHaveBeenCalled());
      expect(screen.getAllByText("None")).toHaveLength(2); // CA certificates and client certificates

      service.AddCABundle.mockResolvedValueOnce([corp, old]);
      fireEvent.click(screen.getByRole("button", { name: "Add file…" }));
      expect(await screen.findByText("corp-root.pem")).toBeTruthy();
      expect(service.AddCABundle).toHaveBeenCalledTimes(1);
      expect(screen.getAllByText("None")).toHaveLength(1);
      expect(screen.getByText("2 certificates, the first expires 2028-06-30")).toBeTruthy();
      expect(screen.getByText("1 certificate, expired")).toBeTruthy();
      expect(screen.getByText(/"Old" expired on 2021-01-01/)).toBeTruthy();

      service.RemoveCABundle.mockResolvedValueOnce([old]);
      fireEvent.click(screen.getByRole("button", { name: "Remove corp-root.pem from the trusted CA certificates" }));
      await waitFor(() => expect(screen.queryByText("corp-root.pem")).toBeNull());
      expect(service.RemoveCABundle).toHaveBeenCalledWith(corp.path);
    });

    it("shows why a file was refused", async () => {
      renderScreen();
      networkPage();
      service.AddCABundle.mockRejectedValueOnce({
        kind: "parse",
        code: "tlsconf.ca_no_certificates",
        message: "The file /tmp/key.pem contains no PEM certificate.",
        hint: "Use a PEM file.",
        details: {},
        causes: [],
      });
      fireEvent.click(screen.getByRole("button", { name: "Add file…" }));
      expect((await screen.findByRole("alert")).textContent).toContain("contains no PEM certificate. Use a PEM file.");
    });

    it("shows a bundle that can no longer be used, and follows hand edits", async () => {
      service.CABundles.mockResolvedValueOnce([
        {
          path: "/Users/ann/certs/gone.pem",
          certificates: [],
          warnings: [],
          error: { kind: "not_found", code: "tlsconf.file_unreadable", message: "The file /Users/ann/certs/gone.pem could not be read.", hint: "Fix the file.", details: {}, causes: [] },
        },
      ]);
      renderScreen();
      networkPage();
      expect((await screen.findByRole("alert")).textContent).toContain("could not be read. Fix the file.");

      service.CABundles.mockResolvedValueOnce([corp]);
      act(() => emit("settings:changed", { settings: {}, problems: [], source: "file" }));
      expect(await screen.findByText("corp-root.pem")).toBeTruthy();
      expect(screen.queryByText("gone.pem")).toBeNull();
    });
  });

  it("lists the settings file's problems on every page", () => {
    renderScreen({}, createStores({ settings: initialSettingsState }));
    expect(screen.queryByText(/settings\.json has/)).toBeNull();
    act(() =>
      setSettingsProblems([
        {
          kind: "invalid_argument",
          code: "settings.invalid_value",
          message: 'The setting "ui.theme" has an invalid value, "blue".',
          hint: 'Use one of "system", "dark", "light" in settings.json.',
          details: {},
          causes: [],
        },
      ]),
    );
    expect(screen.getByText("settings.json has a problem")).toBeTruthy();
    expect(screen.getByText(/"ui.theme" has an invalid value/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Privacy" }));
    expect(screen.getByText("settings.json has a problem")).toBeTruthy();

    act(() => setSettingsProblems([]));
    expect(screen.queryByText(/settings\.json has/)).toBeNull();
  });
});

describe("Recent projects (General)", () => {
  const shop = { id: "ws_1", name: "Shop", path: "/p/shop", openedAt: "2026-09-18T09:00:00Z", available: true };
  const billing = { id: "ws_2", name: "Billing", path: "/p/billing", openedAt: "2026-09-17T09:00:00Z", available: true };
  const gone = { id: "ws_3", name: "", path: "/Volumes/usb/old", openedAt: "2026-09-16T09:00:00Z", available: false };

  function openStores(): Stores {
    const stores = createStores();
    stores.workspace.dispatch({
      type: "workspace/setCurrent",
      workspace: {
        id: "ws_1",
        name: "Shop",
        path: "/p/shop",
        localDir: "/data/ws_1",
        localStatus: "existing",
        previousPath: "",
        newerFormat: false,
      },
    });
    return stores;
  }

  it("lists the recent projects; the open one cannot be removed", async () => {
    workspaceService.Recent.mockResolvedValue([shop, billing, gone]);
    renderScreen({}, openStores());
    const group = await screen.findByRole("region", { name: "Recent projects" });
    await waitFor(() => expect(within(group).getByText("Billing")).toBeTruthy());
    expect(within(group).getByText(/open now/)).toBeTruthy();
    expect(within(group).getByText(/not found/)).toBeTruthy();
    expect(within(group).queryByRole("button", { name: "Remove Shop from recent projects" })).toBeNull();
    expect(within(group).queryByRole("button", { name: "Open Shop" })).toBeNull();
    expect((within(group).getByRole("button", { name: "Open old" }) as HTMLButtonElement).disabled).toBe(true);

    workspaceService.Recent.mockResolvedValue([shop, gone]);
    fireEvent.click(within(group).getByRole("button", { name: "Remove Billing from recent projects" }));
    await waitFor(() => expect(within(group).queryByText("Billing")).toBeNull());
    expect(workspaceService.RemoveRecent).toHaveBeenCalledWith("/p/billing");

    fireEvent.click(within(group).getByRole("button", { name: "Reveal Shop" }));
    await waitFor(() => expect(workspaceService.Reveal).toHaveBeenCalledWith("/p/shop"));

    workspaceService.Recent.mockResolvedValue([shop]);
    fireEvent.click(within(group).getByRole("button", { name: "Clear" }));
    await waitFor(() => expect(within(group).queryByText(/not found/)).toBeNull());
    expect(workspaceService.ClearRecent).toHaveBeenCalledTimes(1);
    expect(within(group).queryByRole("button", { name: "Clear" })).toBeNull();
  });

  it("shows why an action failed", async () => {
    workspaceService.Recent.mockResolvedValue([shop, billing]);
    workspaceService.ClearRecent.mockRejectedValue(new Error("disk full"));
    renderScreen({}, openStores());
    const group = await screen.findByRole("region", { name: "Recent projects" });
    fireEvent.click(await within(group).findByRole("button", { name: "Clear" }));
    expect(await within(group).findByRole("alert")).toBeTruthy();
  });
});

describe("shortenPath", () => {
  it("abbreviates the home folder and keeps the last segments", () => {
    expect(shortenPath("/Users/ann/Library/Application Support/PostSonde")).toBe("~/Library/Application Support/PostSonde");
    expect(shortenPath("/home/ann/.config/postsonde/settings.json", 2)).toBe("…/postsonde/settings.json");
    expect(shortenPath("/srv/settings.json", 2)).toBe("/srv/settings.json");
    expect(shortenPath("C:\\Users\\ann\\AppData\\Roaming\\PostSonde\\settings.json", 2)).toBe("…\\PostSonde\\settings.json");
  });
});

describe("Response position (UI-02)", () => {
  it("is beside by default; picking Below applies and saves it", async () => {
    const stores = renderScreen();
    const group = screen.getByRole("group", { name: "Response position" });
    expect(within(group).getByRole("button", { name: "Beside" }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(within(group).getByRole("button", { name: "Below" }));
    await waitFor(() => expect(stores.settings.getState().responsePosition).toBe("below"));
    expect(saved()).toMatchObject({ responsePosition: "below" });
    expect(within(group).getByRole("button", { name: "Below" }).getAttribute("aria-pressed")).toBe("true");
  });
});

describe("Language (UI-10)", () => {
  const languageField = () => screen.getByRole("combobox", { name: "Language" }) as HTMLSelectElement;
  const options = () => [...languageField().options].map((o) => [o.value, o.textContent]);

  it("follows the system by default and lists English, and the pseudo-locale in dev builds", () => {
    renderScreen();
    expect(languageField().value).toBe("system");
    expect(options()).toEqual([
      ["system", "System (English)"],
      ["en", "English"],
      // Vitest runs as a dev build (import.meta.env.DEV).
      ["en-XA", "Pseudo-locale (en-XA)"],
    ]);
  });

  it("applies and saves the choice", async () => {
    const stores = renderScreen();
    fireEvent.change(languageField(), { target: { value: "en" } });
    await waitFor(() => expect(stores.settings.getState().locale).toBe("en"));
    expect(saved()).toMatchObject({ locale: "en" });
  });

  it("lists a language set by hand that the app does not have, shown in English", () => {
    renderScreen({}, createStores({ settings: { ...initialSettingsState, locale: "de-CH" } }));
    expect(languageField().value).toBe("de-CH");
    expect(options()[options().length - 1]).toEqual(["de-CH", "de-CH (not available, English is shown)"]);
  });
});

describe("Editor page (UI-08)", () => {
  const fontSizeField = () => screen.getByRole("textbox", { name: "Font size" }) as HTMLInputElement;

  it("shows the editor settings with their defaults and a live preview", () => {
    renderScreen({ initialPage: "editor" });
    expect(screen.getByRole("heading", { level: 1, name: "Editor" })).toBeTruthy();
    expect((screen.getByRole("combobox", { name: "Font" }) as HTMLSelectElement).value).toBe("JetBrains Mono");
    expect(fontSizeField().value).toBe("12");
    const tabs = screen.getByRole("group", { name: "Tab size" });
    expect(within(tabs).getAllByRole("button").map((b) => b.textContent)).toEqual(["2", "4", "8"]);
    expect(within(tabs).getByRole("button", { name: "2" }).getAttribute("aria-pressed")).toBe("true");
    expect((screen.getByRole("checkbox", { name: "Word wrap" }) as HTMLInputElement).checked).toBe(false);
    expect((screen.getByRole("checkbox", { name: "Line numbers" }) as HTMLInputElement).checked).toBe(true);
    expect((screen.getByRole("checkbox", { name: "Match brackets" }) as HTMLInputElement).checked).toBe(true);
    const preview = screen.getByRole("region", { name: "Preview" });
    expect(within(preview).getByRole("textbox", { name: "Editor preview" }).textContent).toContain("customer_request");
  });

  it("saves the font, tab size and switches as they change", async () => {
    const stores = renderScreen({ initialPage: "editor" });
    fireEvent.change(screen.getByRole("combobox", { name: "Font" }), { target: { value: "Menlo" } });
    await waitFor(() => expect(stores.settings.getState().editorFontFamily).toBe("Menlo"));

    fireEvent.click(within(screen.getByRole("group", { name: "Tab size" })).getByRole("button", { name: "4" }));
    await waitFor(() => expect(stores.settings.getState().editorTabSize).toBe(4));

    fireEvent.click(screen.getByRole("checkbox", { name: "Word wrap" }));
    await waitFor(() => expect(stores.settings.getState().editorWordWrap).toBe(true));
    fireEvent.click(screen.getByRole("checkbox", { name: "Line numbers" }));
    await waitFor(() => expect(stores.settings.getState().editorLineNumbers).toBe(false));
    fireEvent.click(screen.getByRole("checkbox", { name: "Match brackets" }));
    await waitFor(() => expect(stores.settings.getState().editorMatchBrackets).toBe(false));

    expect(saved()).toEqual({
      editorFontFamily: "Menlo",
      editorTabSize: 4,
      editorWordWrap: true,
      editorLineNumbers: false,
      editorMatchBrackets: false,
    });
  });

  it("shows a font or tab size set by hand in the file", () => {
    const stores = renderScreen({ initialPage: "editor" });
    act(() =>
      stores.settings.dispatch({ type: "settings/load", settings: { editorFontFamily: "Iosevka", editorTabSize: 3 } }),
    );
    const select = screen.getByRole("combobox", { name: "Font" }) as HTMLSelectElement;
    expect(select.value).toBe("Iosevka");
    expect(within(select).getByRole("option", { name: "Iosevka (from settings.json)" })).toBeTruthy();
    const tabs = screen.getByRole("group", { name: "Tab size" });
    expect(within(tabs).getAllByRole("button").map((b) => b.textContent)).toEqual(["2", "3", "4", "8"]);
    expect(within(tabs).getByRole("button", { name: "3" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("saves the font size shortly after typing, and refuses one out of range", async () => {
    vi.useFakeTimers();
    const stores = renderScreen({ initialPage: "editor" });
    const field = fontSizeField();
    fireEvent.focus(field);
    fireEvent.change(field, { target: { value: "15" } });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(400);
    });
    expect(stores.settings.getState().editorFontSize).toBe(15);
    expect(saved()).toMatchObject({ editorFontSize: 15 });

    fireEvent.change(field, { target: { value: "40" } });
    fireEvent.blur(field);
    const error = screen.getByRole("alert");
    expect(error.textContent).toBe("Use a whole number of pixels from 8 to 32.");
    expect(field.getAttribute("aria-describedby")).toBe(error.id);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(400);
    });
    expect(stores.settings.getState().editorFontSize).toBe(15);
  });

  it("shows the error from the settings file when it refuses a value", async () => {
    const refusal: AppError = {
      kind: "invalid_argument",
      code: "settings.invalid_value",
      message: 'The setting "editor.fontFamily" has an invalid value.',
      hint: "",
      details: {},
      causes: [],
    };
    setSettingsPersistence({ load: () => Promise.resolve({}), save: () => Promise.reject(refusal) });
    const stores = renderScreen({ initialPage: "editor" });
    fireEvent.change(screen.getByRole("combobox", { name: "Font" }), { target: { value: "Menlo" } });
    expect((await screen.findByRole("alert")).textContent).toContain('"editor.fontFamily" has an invalid value');
    expect(stores.settings.getState().editorFontFamily).toBe("JetBrains Mono");
  });
});
