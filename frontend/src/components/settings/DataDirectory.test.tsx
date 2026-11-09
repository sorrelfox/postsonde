import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Info: vi.fn<() => Promise<unknown>>(),
  Pick: vi.fn<() => Promise<unknown>>(),
  Check: vi.fn<(path: string) => Promise<unknown>>(),
  Schedule: vi.fn<(req: { path: string; action: string }) => Promise<unknown>>(),
  Cancel: vi.fn<() => Promise<unknown>>(),
  DeletePrevious: vi.fn<() => Promise<unknown>>(),
  ForgetPrevious: vi.fn<() => Promise<unknown>>(),
  Reveal: vi.fn<(which: string) => Promise<void>>(),
  Restart: vi.fn<() => Promise<void>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/datadir", () => ({
  DataDirService: service,
}));

import { startupNotice } from "../../commands/dataDir";
import type { DataDirInfo } from "../../lib/dataDir";
import { createStores, StoreProvider } from "../../stores";
import { actionsFor, DataDirectoryRows, DataDirNotice } from "./DataDirectory";

const DATA = "/Users/ann/Library/Application Support/PostSonde";

function info(over: Partial<DataDirInfo> = {}): DataDirInfo {
  return {
    mode: "default",
    dataDir: DATA,
    wanted: DATA,
    default: DATA,
    root: "",
    marker: "",
    pending: null,
    previous: null,
    moved: false,
    problems: [],
    changeBlocked: null,
    canRestart: true,
    ...over,
  };
}

function problem(code: string, message: string, hint = "") {
  return { kind: "not_found", code, message, hint, details: {}, causes: [] };
}

function renderRows() {
  render(
    <StoreProvider stores={createStores()}>
      <DataDirectoryRows fallbackPath="/fallback" shorten={(p) => p} />
    </StoreProvider>,
  );
}

beforeEach(() => {
  for (const fn of Object.values(service)) fn.mockReset();
  service.Info.mockResolvedValue(info());
  service.Restart.mockResolvedValue(undefined);
  service.Reveal.mockResolvedValue(undefined);
});

describe("DataDirectoryRows", () => {
  it("moves the data to a picked folder and offers a restart", async () => {
    const target = "/Volumes/Work/PostSonde";
    service.Pick.mockResolvedValue({ path: target, exists: true, hasData: false, isDefault: false, canMove: true });
    service.Schedule.mockResolvedValue(info({ pending: { action: "move", dataDir: target } }));
    renderRows();

    expect(await screen.findByText(DATA)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Change…" }));

    const dialog = await screen.findByRole("dialog", { name: "Change the data directory" });
    expect(within(dialog).getByText(target)).toBeTruthy();
    const move = within(dialog).getByRole("radio", { name: /Move my data there/ }) as HTMLInputElement;
    expect(move.checked).toBe(true);
    expect(within(dialog).getByRole("radio", { name: /Start fresh/ })).toBeTruthy();
    fireEvent.click(within(dialog).getByRole("button", { name: "Change" }));

    await waitFor(() => expect(service.Schedule).toHaveBeenCalledWith({ path: target, action: "move" }));
    const prompt = await screen.findByRole("dialog", { name: "Restart PostSonde?" });
    fireEvent.click(within(prompt).getByRole("button", { name: "Later" }));

    expect(await screen.findByText("After restart")).toBeTruthy();
    expect(screen.getByText(target)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Restart now" }));
    await waitFor(() => expect(service.Restart).toHaveBeenCalledTimes(1));
  });

  it("starts fresh when chosen", async () => {
    service.Pick.mockResolvedValue({ path: "/new", exists: false, hasData: false, isDefault: false, canMove: true });
    service.Schedule.mockResolvedValue(info({ pending: { action: "fresh", dataDir: "/new" } }));
    renderRows();
    await screen.findByText(DATA);
    fireEvent.click(screen.getByRole("button", { name: "Change…" }));
    const dialog = await screen.findByRole("dialog", { name: "Change the data directory" });
    fireEvent.click(within(dialog).getByRole("radio", { name: /Start fresh/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Change" }));
    await waitFor(() => expect(service.Schedule).toHaveBeenCalledWith({ path: "/new", action: "fresh" }));
  });

  it("shows why a picked folder cannot be used", async () => {
    const err = new Error("raw");
    (err as Error & { cause: unknown }).cause = problem(
      "datadir.not_empty",
      "This folder already has other files in it.",
      "Choose an empty folder.",
    );
    service.Pick.mockRejectedValue(err);
    renderRows();
    await screen.findByText(DATA);
    fireEvent.click(screen.getByRole("button", { name: "Change…" }));
    expect((await screen.findByRole("alert")).textContent).toContain("This folder already has other files in it.");
    expect(service.Schedule).not.toHaveBeenCalled();
  });

  it("does nothing when the folder dialog is cancelled", async () => {
    service.Pick.mockResolvedValue({ path: "", exists: false, hasData: false, isDefault: false, canMove: false });
    renderRows();
    await screen.findByText(DATA);
    fireEvent.click(screen.getByRole("button", { name: "Change…" }));
    await waitFor(() => expect(service.Pick).toHaveBeenCalled());
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("cannot change in portable mode", async () => {
    service.Info.mockResolvedValue(
      info({
        mode: "portable",
        dataDir: "/Volumes/USB/postsonde-data/data",
        changeBlocked: problem(
          "datadir.change_unavailable",
          "PostSonde runs in portable mode.",
          "Remove the postsonde.portable file beside the app to keep the data elsewhere.",
        ),
      }),
    );
    renderRows();
    expect(await screen.findByText(/Portable mode/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Change…" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Remove the postsonde.portable file/)).toBeTruthy();
  });

  it("cancels a scheduled change", async () => {
    service.Info.mockResolvedValue(info({ pending: { action: "use", dataDir: "/other" } }));
    service.Cancel.mockResolvedValue(info());
    renderRows();
    fireEvent.click(await screen.findByRole("button", { name: "Cancel change" }));
    await waitFor(() => expect(screen.queryByText("After restart")).toBeNull());
  });

  it("deletes the old copy only after confirming", async () => {
    const old = "/Users/ann/old-data";
    service.Info.mockResolvedValue(info({ moved: true, previous: { dataDir: old, movedAt: "2026-09-19T12:00:00Z" } }));
    service.DeletePrevious.mockResolvedValue(info());
    renderRows();
    expect(await screen.findByText(old)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Delete…" }));
    let dialog = await screen.findByRole("dialog", { name: "Delete the old data?" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(service.DeletePrevious).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Delete…" }));
    dialog = await screen.findByRole("dialog", { name: "Delete the old data?" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(service.DeletePrevious).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.queryByText("Old data folder")).toBeNull());
  });

  it("falls back to the path it was given when Go cannot say", async () => {
    service.Info.mockRejectedValue(new Error("no service"));
    renderRows();
    expect(await screen.findByText("/fallback")).toBeTruthy();
  });
});

describe("DataDirNotice", () => {
  it("is empty when all is well", async () => {
    const { container } = render(
      <StoreProvider stores={createStores()}>
        <DataDirNotice />
      </StoreProvider>,
    );
    await waitFor(() => expect(service.Info).toHaveBeenCalled());
    expect(container.textContent).toBe("");
  });

  it("explains a missing data directory and offers another", async () => {
    service.Info.mockResolvedValue(
      info({
        mode: "configured",
        dataDir: "",
        problems: [problem("datadir.not_found", "The data directory /Volumes/USB is not available.", "Connect it.")],
      }),
    );
    render(
      <StoreProvider stores={createStores()}>
        <DataDirNotice />
      </StoreProvider>,
    );
    expect(await screen.findByText(/is not available/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Choose another data directory…" })).toBeTruthy();
  });
});

describe("helpers", () => {
  it("offers the actions that fit the folder", () => {
    const base = { path: "/x", exists: true, isDefault: false };
    expect(actionsFor({ ...base, hasData: true, canMove: true })).toEqual(["use"]);
    expect(actionsFor({ ...base, hasData: false, canMove: true })).toEqual(["move", "fresh"]);
    expect(actionsFor({ ...base, hasData: false, canMove: false })).toEqual(["fresh"]);
  });

  it("words the startup notice", () => {
    expect(startupNotice(info())).toBeNull();
    expect(startupNotice(info({ moved: true }))).toContain(`moved to ${DATA}`);
    expect(startupNotice(info({ problems: [problem("x", "Broken.", "Fix it."), problem("y", "Also.")] }))).toBe(
      "Data directory: Broken. Fix it. (and 1 more)",
    );
  });
});
