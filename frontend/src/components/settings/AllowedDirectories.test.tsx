import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  AllowedDirs: vi.fn<() => Promise<unknown>>(),
  AddAllowedDir: vi.fn<() => Promise<unknown>>(),
  RemoveAllowedDir: vi.fn<(path: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/settings", () => ({
  SettingsService: service,
}));

import { emit } from "../../test/fakeWails";
import { AllowedDirRows } from "./AllowedDirectories";

const fixtures = { path: "/Users/ann/api/fixtures", error: null };
const gone = {
  path: "/Volumes/usb/keys",
  error: {
    kind: "not_found",
    code: "settings.allowed_dir_missing",
    message: "This folder does not exist any more, so it allows nothing.",
    hint: "Restore it, or remove it from the list.",
    details: {},
    causes: [],
  },
};

function renderRows() {
  render(
    <div>
      <AllowedDirRows shorten={(p) => p.replace("/Users/ann", "~")} />
    </div>,
  );
}

beforeEach(() => {
  service.AllowedDirs.mockReset().mockResolvedValue([]);
  service.AddAllowedDir.mockReset().mockResolvedValue([]);
  service.RemoveAllowedDir.mockReset().mockResolvedValue([]);
});

describe("AllowedDirRows (PRV-10)", () => {
  it("lists none, adds a folder through the dialog and removes it", async () => {
    renderRows();
    await waitFor(() => expect(service.AllowedDirs).toHaveBeenCalled());
    expect(screen.getByText("Allowed directories for file access")).toBeTruthy();
    expect(screen.getByText("None")).toBeTruthy();

    service.AddAllowedDir.mockResolvedValueOnce([fixtures]);
    fireEvent.click(screen.getByRole("button", { name: "Add folder…" }));
    expect(await screen.findByText("fixtures")).toBeTruthy();
    expect(service.AddAllowedDir).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("None")).toBeNull();
    const path = screen.getByText("~/api/fixtures");
    expect(path.getAttribute("title")).toBe(fixtures.path);

    service.RemoveAllowedDir.mockResolvedValueOnce([]);
    fireEvent.click(screen.getByRole("button", { name: "Remove fixtures from the allowed directories" }));
    await waitFor(() => expect(screen.queryByText("fixtures")).toBeNull());
    expect(service.RemoveAllowedDir).toHaveBeenCalledWith(fixtures.path);
    expect(screen.getByText("None")).toBeTruthy();
  });

  it("keeps the list when the dialog is cancelled", async () => {
    service.AllowedDirs.mockResolvedValue([fixtures]);
    renderRows();
    expect(await screen.findByText("fixtures")).toBeTruthy();
    service.AddAllowedDir.mockResolvedValueOnce([fixtures]);
    fireEvent.click(screen.getByRole("button", { name: "Add folder…" }));
    await waitFor(() => expect(service.AddAllowedDir).toHaveBeenCalled());
    expect(screen.getAllByRole("button", { name: /^Remove / })).toHaveLength(1);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("shows a folder that is gone, and why adding failed", async () => {
    service.AllowedDirs.mockResolvedValue([gone]);
    renderRows();
    expect((await screen.findByRole("alert")).textContent).toContain("does not exist any more");

    service.AddAllowedDir.mockRejectedValueOnce({
      kind: "invalid_argument",
      code: "settings.allowed_dir_missing",
      message: "Only a folder can be allowed.",
      hint: "Choose a folder, not a file.",
      details: {},
      causes: [],
    });
    fireEvent.click(screen.getByRole("button", { name: "Add folder…" }));
    await waitFor(() => expect(screen.getAllByRole("alert")).toHaveLength(2));
    expect(screen.getAllByRole("alert")[0]!.textContent).toContain("Only a folder can be allowed.");
  });

  it("follows hand edits of the settings file", async () => {
    renderRows();
    await waitFor(() => expect(service.AllowedDirs).toHaveBeenCalledTimes(1));
    service.AllowedDirs.mockResolvedValue([fixtures]);
    act(() => emit("settings:changed", { settings: {}, problems: [], source: "file" }));
    expect(await screen.findByText("fixtures")).toBeTruthy();
  });
});
