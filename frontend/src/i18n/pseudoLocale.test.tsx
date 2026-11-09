// The converted part of the UI in the pseudo-locale (UI-10): the app
// shell (title bar, icon rail, sidebar header, status bar), every page of
// the settings screen, and the shared dialog, confirm() and toasts. Every
// text they show must come from the catalogue: in the pseudo-locale it
// reads "[Šéñð ŕéǫûéšţ one]", and no English text of their namespaces
// (shell, settings, common) may be left. Areas converted later add their
// screens and namespaces here (docs/frontend.md#localization).
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: () => undefined });

const bundle = {
  path: "/Users/ann/certs/corp-root.pem",
  certificates: [
    { subject: "CN=Corp Root", issuer: "CN=Corp Root", notBefore: "2025-01-01T00:00:00Z", notAfter: "2030-01-01T00:00:00Z", expired: false, isCa: true, sha256: "ab" },
    { subject: "CN=Corp Issuing", issuer: "CN=Corp Root", notBefore: "2025-01-01T00:00:00Z", notAfter: "2028-06-30T00:00:00Z", expired: false, isCa: true, sha256: "cd" },
  ],
  warnings: [],
  error: null,
};
const clientCert = {
  id: "cc_1",
  host: "*.example.com",
  kind: "pkcs12",
  certFile: "",
  keyFile: "",
  pfxFile: "/Users/ann/certs/client.p12",
  hasPassphrase: true,
  subject: "CN=api client,O=Corp",
  issuer: "CN=Corp CA",
  notBefore: "2026-01-01T00:00:00Z",
  notAfter: "2027-03-01T00:00:00Z",
  expired: false,
  sha256: "ab",
  chainLength: 1,
  warnings: [],
  error: null,
};
const dataDirInfo = {
  mode: "default",
  dataDir: "/Users/ann/Library/Application Support/PostSonde",
  wanted: "/Users/ann/Library/Application Support/PostSonde",
  default: "/Users/ann/Library/Application Support/PostSonde",
  root: "",
  marker: "",
  pending: { action: "move", dataDir: "/Volumes/data/PostSonde" },
  previous: { dataDir: "/Users/ann/old-data", movedAt: "2026-09-19T12:00:00Z" },
  moved: true,
  problems: [],
  changeBlocked: null,
  canRestart: true,
};

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/console", () => ({
  ConsoleService: { List: () => cancellable({ entries: [], capacity: 2000 }), Clear: () => cancellable(undefined) },
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/network", () => ({
  NetworkService: { List: () => cancellable({ conns: [], active: 0, capacity: 500 }), Clear: () => cancellable(undefined) },
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: {
    Location: () => cancellable({ path: "/Users/ann/.config/postsonde/settings.json", exists: true, dataDir: "/data" }),
    CABundles: () => cancellable([bundle]),
    ClientCerts: () => cancellable([clientCert]),
    AllowedDirs: () => cancellable([{ path: "/Users/ann/fixtures", error: null }]),
    Proxy: () =>
      cancellable({
        mode: "manual",
        url: "socks5h://alice@proxy.lan:1080",
        hasPassword: true,
        envVar: "ALL_PROXY",
        envUrl: "socks5://env.lan:1080",
        effective: "socks5h://alice@proxy.lan:1080",
        remoteDns: true,
        error: null,
      }),
  },
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/localdata", () => ({
  LocalDataService: {
    Usage: ({ scope }: { scope: string }) =>
      cancellable({
        scope,
        project: "Shop",
        projects: scope === "all" ? 2 : 1,
        categories: ["history", "runs", "cookies", "tokens", "schemas", "runtimeVariables"].map((category, i) => ({
          category,
          files: i,
          bytes: i * 1500,
        })),
      }),
  },
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/datadir", () => ({
  DataDirService: { Info: () => cancellable(dataDirInfo) },
}));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: {
    Recent: () =>
      cancellable([
        { id: "ws_1", name: "Shop", path: "/p/shop", openedAt: "2026-09-18T09:00:00Z", available: true },
        { id: "ws_2", name: "Old", path: "/Volumes/usb/old", openedAt: "2026-09-16T09:00:00Z", available: false },
      ]),
  },
}));

import { confirm } from "../commands/dialogs";
import { setSettingsPersistence, setSettingsPolicy, setSettingsProblems } from "../commands/settings";
import { showToast } from "../commands/toasts";
import { toAppError } from "../lib/errors";
import { localStorageSettings } from "../lib/persist";
import { NO_POLICY } from "../lib/settings";
import { createStores, StoreProvider } from "../stores";
import { englishLeftovers, isPseudo, visibleTexts } from "../test/pseudoLocale";
import { AppShell } from "../components/shell/AppShell";
import { AutoSaveStatus } from "../components/shell/AutoSaveStatus";
import { PolicyStatus } from "../components/shell/PolicyStatus";
import type { Namespace } from "./catalog";
import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale, t } from "./index";
import { pseudoString } from "./pseudo";

/** The namespaces converted so far; each area adds its own when it is converted. */
const CONVERTED: readonly Namespace[] = ["shell", "settings", "common"];

function renderShell() {
  const stores = createStores();
  render(
    <StoreProvider stores={stores}>
      <AppShell
        projectName="shop-api"
        projectPath="/p/shop"
        onCloseProject={() => undefined}
        tabBar={<div />}
        request={<div />}
        statusStart={<AutoSaveStatus />}
        statusEnd={<PolicyStatus />}
      />
    </StoreProvider>,
  );
  return stores;
}

/** The shell's regions: title bar, icon rail, sidebar, status bar and work area (the tree and tabs in them keep their own namespaces). */
function shellRoots(): Element[] {
  return [
    screen.getByRole("banner"),
    screen.getByRole("navigation", { name: pseudoString("Sidebar views") }),
    screen.getByRole("complementary", { name: pseudoString("Collections") }),
    screen.getByRole("contentinfo"),
    screen.getByRole("region", { name: pseudoString("Request") }).parentElement!,
  ];
}

/**
 * Components of unconverted areas inside the slice: the collection tree's
 * empty state in the sidebar (tree), which still says "No project open" in
 * English. Remove each when its area is converted.
 */
function foreignElements(): Element[] {
  return [screen.queryByText("No project open")?.parentElement ?? null].filter((e): e is HTMLElement => e !== null);
}

function expectNoEnglish(roots: readonly Element[]) {
  expect(englishLeftovers(roots, CONVERTED, { exclude: foreignElements() })).toEqual([]);
}

beforeEach(() => {
  localStorage.clear();
  setSettingsPersistence(localStorageSettings());
  setSettingsPolicy({
    active: true,
    path: "/etc/postsonde/policy.json",
    locked: ["ui.theme", "network.caBundles"],
    requireTlsVerification: true,
    problems: [],
  });
  setSettingsProblems([
    toAppError({ kind: "invalid_argument", code: "settings.invalid_value", message: "ui.splitRatio: 2 is out of range.", hint: "", details: {}, causes: [] }),
  ]);
  setLocale(PSEUDO_LOCALE);
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
  setSettingsPolicy(NO_POLICY);
  setSettingsProblems([]);
});

describe("the pseudo-locale", () => {
  it("shows the app shell's own text from the catalogue", () => {
    renderShell();
    const roots = shellRoots();
    expectNoEnglish(roots);
    const texts = visibleTexts(roots);
    expect(texts).toContain(pseudoString("Status bar"));
    expect(texts).toContain(pseudoString("Ready"));
    expect(texts).toContain(pseudoString("Managed"));
    expect(texts).toContain(pseudoString("Close project"));
    expect(texts).toContain(pseudoString("New request"));
    expect(texts.filter(isPseudo).length).toBeGreaterThan(20);
    // The Managed indicator's Tooltip describes it, from the catalogue too.
    const managed = screen.getByText(pseudoString("Managed"));
    const tooltip = document.getElementById(managed.getAttribute("aria-describedby") ?? "");
    expect(tooltip?.textContent).toContain(pseudoString("Settings managed by your administrator."));
    expectNoEnglish([tooltip!]);
    expect(document.documentElement.lang).toBe(PSEUDO_LOCALE);
  });

  it("shows every page of the settings screen from the catalogue", async () => {
    renderShell();
    fireEvent.click(screen.getByRole("button", { name: pseudoString("Settings") }));
    const nav = screen.getByRole("navigation", { name: pseudoString("Settings") });
    const settingsScreen = nav.parentElement!;
    for (const page of ["General", "Editor", "Network", "Shortcuts", "Privacy"]) {
      fireEvent.click(within(nav).getByRole("button", { name: pseudoString(page) }));
      await waitFor(() => expect(screen.getByRole("heading", { level: 1, name: pseudoString(page) })).toBeTruthy());
      // Rows filled from Go (CA bundles, client certificates, recent projects, the data directory) have arrived.
      if (page === "General") await screen.findByText(pseudoString("Old data folder"));
      if (page === "Network") {
        await screen.findByText(t("settings.ca.firstExpires", { count: t("settings.ca.count", { count: 2 }), date: "2028-06-30" }));
        await screen.findByLabelText(pseudoString("Proxy URL"));
      }
      if (page === "Privacy") {
        await screen.findByText("fixtures");
        await screen.findByText(t("settings.localData.scope.project", { name: "Shop" }));
      }
      expectNoEnglish([settingsScreen]);
      expect(visibleTexts([settingsScreen]).filter(isPseudo).length).toBeGreaterThan(5);
    }
  });

  it("shows the dialog, confirm() and toasts from the catalogue", async () => {
    renderShell();
    let answer: Promise<boolean> | undefined;
    act(() => {
      answer = confirm({ title: t("settings.dataDir.deleteTitle"), message: t("settings.dataDir.restartBody") });
      showToast({ message: t("settings.dataDir.takesEffect"), action: { label: t("settings.clientCert.edit"), run: () => undefined } });
    });
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByRole("button", { name: pseudoString("OK") })).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: pseudoString("Cancel") })).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: pseudoString("Close") }).getAttribute("title")).toBe(pseudoString("Close (Esc)"));
    const toasts = screen.getByRole("region", { name: pseudoString("Notifications"), hidden: true });
    expect(within(toasts).getByRole("button", { name: pseudoString("Dismiss notification"), hidden: true })).toBeTruthy();
    expectNoEnglish([dialog, toasts]);
    fireEvent.click(within(dialog).getByRole("button", { name: pseudoString("Cancel") }));
    await expect(answer).resolves.toBe(false);
  });

  it("switches live: English again as soon as the language is", () => {
    renderShell();
    expect(screen.getByRole("contentinfo").getAttribute("aria-label")).toBe(pseudoString("Status bar"));
    act(() => {
      setLocale(DEFAULT_LOCALE);
    });
    expect(screen.getByRole("contentinfo").getAttribute("aria-label")).toBe("Status bar");
    expect(screen.getByText("Ready")).toBeTruthy();
  });
});
