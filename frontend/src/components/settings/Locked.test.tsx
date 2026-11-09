import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Location: vi.fn<() => Promise<unknown>>(),
  CABundles: vi.fn<() => Promise<unknown>>(),
  ClientCerts: vi.fn<() => Promise<unknown>>(),
  AllowedDirs: vi.fn<() => Promise<unknown>>(),
}));

const workspaceService = vi.hoisted(() => ({
  Recent: vi.fn<() => Promise<unknown>>(),
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

import {
  flushSettings,
  isSettingLocked,
  setSettingsPersistence,
  setSettingsPolicy,
  trackSettings,
} from "../../commands/settings";
import { localStorageSettings, type SettingsPersistence } from "../../lib/persist";
import { NO_POLICY, type SettingsPolicy } from "../../lib/settings";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { PolicyStatus } from "../shell/PolicyStatus";
import { SettingsScreen, type SettingsScreenProps } from "./SettingsScreen";
import { ShortcutSettings } from "./ShortcutSettings";

const POLICY: SettingsPolicy = {
  active: true,
  path: "/Library/Application Support/PostSonde/policy.json",
  locked: ["network.verifyTls", "network.defaultTimeoutMs", "editor.fontSize", "privacy.allowedDirs", "ui.theme"],
  requireTlsVerification: true,
  problems: [],
};

function renderScreen(props: SettingsScreenProps = {}, stores: Stores = createStores()) {
  render(
    <StoreProvider stores={stores}>
      <SettingsScreen {...props} />
    </StoreProvider>,
  );
  return stores;
}

beforeEach(() => {
  localStorage.clear();
  setSettingsPersistence(localStorageSettings());
  service.Location.mockReset().mockResolvedValue({ path: "/tmp/settings.json", exists: true, dataDir: "/tmp" });
  service.CABundles.mockReset().mockResolvedValue([]);
  service.ClientCerts.mockReset().mockResolvedValue([]);
  service.AllowedDirs.mockReset().mockResolvedValue([]);
  workspaceService.Recent.mockReset().mockResolvedValue([]);
  dataDirService.Info.mockReset().mockRejectedValue(new Error("not in this test"));
});

afterEach(async () => {
  await flushSettings();
  setSettingsPolicy(NO_POLICY);
});

describe("settings locked by the admin policy", () => {
  it("knows locked settings by file key and store key", () => {
    setSettingsPolicy(POLICY);
    expect(isSettingLocked("network.verifyTls")).toBe(true);
    expect(isSettingLocked("verifyTls")).toBe(true);
    expect(isSettingLocked("editorFontSize")).toBe(true);
    expect(isSettingLocked("editorTabSize")).toBe(false);
    expect(isSettingLocked("privacy.allowedDirs")).toBe(true);
  });

  it("shows locked controls disabled, with a lock and the administrator tooltip", () => {
    setSettingsPolicy(POLICY);
    renderScreen({ initialPage: "network" });

    const notice = screen.getByRole("status", { name: "Administrator policy" });
    expect(notice.textContent).toContain("Some settings are managed by your administrator");
    expect(notice.textContent).toContain("5 settings are locked");
    expect(notice.textContent).toContain(POLICY.path);

    const group = screen.getByRole("group", { name: "Verify TLS certificates" });
    for (const button of within(group).getAllByRole("button")) expect(button.matches(":disabled")).toBe(true);
    const row = group.closest("[data-locked]");
    expect(row?.getAttribute("data-locked")).toBe("true");
    const lock = within(row as HTMLElement).getByRole("img", { name: "Locked: set by your administrator" });
    // The reason is a tooltip that focus shows too, and the lock's description (UI-11).
    expect(lock.tabIndex).toBe(0);
    const tip = document.getElementById(lock.getAttribute("aria-describedby") ?? "");
    expect(tip?.getAttribute("role")).toBe("tooltip");
    expect(tip?.textContent).toBe("Set by your administrator");
    expect(tip?.hidden).toBe(true);
    act(() => lock.focus());
    expect(tip?.hidden).toBe(false);
    fireEvent.keyDown(lock, { key: "Escape" });
    expect(tip?.hidden).toBe(true);
    expect(row?.querySelector("fieldset")?.getAttribute("title")).toBe("Set by your administrator");
    expect(screen.getByText(/Required by your administrator/)).toBeTruthy();

    const timeout = screen.getByRole("textbox", { name: /Default timeout/ }) as HTMLInputElement;
    expect(timeout.closest("fieldset")?.disabled).toBe(true);

    // Unlocked settings stay editable.
    const limit = screen.getByRole("textbox", { name: "Response body limit" }) as HTMLInputElement;
    expect(limit.closest("fieldset")).toBeNull();
    expect(screen.getByRole("button", { name: "Add file…" }).closest("fieldset")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Privacy" }));
    const addFolder = screen.getByRole("button", { name: "Add folder…" });
    expect(addFolder.closest("fieldset")?.disabled).toBe(true);
    expect(screen.getByText("Allowed directories for file access").textContent).toBe("Allowed directories for file access");
  });

  it("shows nothing locked without a policy", () => {
    renderScreen({ initialPage: "network" });
    expect(screen.queryByRole("status", { name: "Administrator policy" })).toBeNull();
    expect(screen.queryByRole("img", { name: /Locked/ })).toBeNull();
    const group = screen.getByRole("group", { name: "Verify TLS certificates" });
    expect(group.closest("fieldset")).toBeNull();
  });

  it("lists the problems of a policy file that is not applied", () => {
    setSettingsPolicy({
      ...NO_POLICY,
      path: "/etc/postsonde/policy.json",
      problems: [{ kind: "parse", code: "settings.policy_invalid", message: "The administrator policy file is not a valid JSON object, so no setting is locked.", hint: "", details: {}, causes: [] }],
    });
    renderScreen();
    const notice = screen.getByRole("status", { name: "Administrator policy" });
    expect(notice.textContent).toContain("The administrator policy is not applied");
    expect(notice.textContent).toContain("not a valid JSON object");
  });

  it("does not save a locked setting changed outside the settings screen", async () => {
    const saves: unknown[] = [];
    const persistence: SettingsPersistence = {
      load: async () => ({ theme: "dark" }),
      save: async (patch) => {
        saves.push(patch);
      },
    };
    setSettingsPersistence(persistence);
    setSettingsPolicy(POLICY);
    const stores = createStores();
    const stop = trackSettings(stores);
    stores.settings.dispatch({ type: "settings/setTheme", theme: "light" });
    stores.settings.dispatch({ type: "settings/setSidebarCollapsed", collapsed: true });
    await flushSettings();
    await vi.waitFor(() => expect(stores.settings.getState().theme).toBe("dark"));
    stop();
    expect(saves).toEqual([{ sidebarCollapsed: true }]);
  });

  it("disables recording for locked shortcuts, all of them or one", () => {
    const row = (id: string) => document.querySelector<HTMLElement>(`[data-shortcut-id="${id}"]`) as HTMLElement;
    const changeButton = (id: string) => within(row(id)).getByRole("button", { name: /^Change the shortcut of/ });

    setSettingsPolicy({ ...POLICY, locked: ["shortcuts"] });
    const { unmount } = render(
      <StoreProvider stores={createStores()}>
        <ShortcutSettings />
      </StoreProvider>,
    );
    expect(changeButton("sidebar.toggle").matches(":disabled")).toBe(true);
    expect(within(row("sidebar.toggle")).getByRole("img", { name: /Locked/ })).toBeTruthy();
    expect((screen.getByRole("button", { name: "Reset all" }) as HTMLButtonElement).disabled).toBe(true);
    unmount();

    setSettingsPolicy({ ...POLICY, locked: ["shortcuts.sidebar.toggle"] });
    render(
      <StoreProvider stores={createStores()}>
        <ShortcutSettings />
      </StoreProvider>,
    );
    expect(changeButton("sidebar.toggle").matches(":disabled")).toBe(true);
    const other = document.querySelector<HTMLElement>('[data-shortcut-id]:not([data-shortcut-id="sidebar.toggle"])');
    const otherChange = within(other as HTMLElement).queryByRole("button", { name: /^Change the shortcut of/ });
    if (otherChange) expect(otherChange.matches(":disabled")).toBe(false);
  });

  it("marks the status bar while a policy is in effect", () => {
    const { rerender } = render(<PolicyStatus />);
    expect(screen.queryByTestId("policy-status")).toBeNull();
    setSettingsPolicy(POLICY);
    rerender(<PolicyStatus />);
    const status = screen.getByTestId("policy-status");
    expect(status.textContent).toBe("Managed");
    const tip = document.getElementById(status.getAttribute("aria-describedby") ?? "");
    expect(tip?.textContent).toContain("Locked: network.verifyTls");
    expect(status.tabIndex).toBe(0);
  });
});
