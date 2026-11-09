import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Usage: vi.fn<(input: { scope: string }) => Promise<unknown>>(),
  Clear: vi.fn<(input: { scope: string; categories: string[] }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/localdata", () => ({
  LocalDataService: service,
}));

import { LocalDataRows } from "./LocalData";

const CATEGORIES = ["history", "runs", "cookies", "tokens", "schemas", "runtimeVariables"];

function usage(scope: string, project: string, projects: number, files: number) {
  return {
    scope,
    project,
    projects,
    categories: CATEGORIES.map((category) => ({ category, files, bytes: files * 2048 })),
  };
}

beforeEach(() => {
  service.Usage.mockReset().mockImplementation(({ scope }) =>
    Promise.resolve(scope === "all" ? usage("all", "Shop", 3, 6) : usage("project", "Shop", 1, 2)),
  );
  service.Clear.mockReset();
});

describe("LocalDataRows (PRV-13)", () => {
  it("shows what each kind holds and clears the ticked ones after the typed confirmation", async () => {
    render(<LocalDataRows />);
    const scope = (await screen.findByLabelText("Data of")) as HTMLSelectElement;
    expect([...scope.options].map((o) => o.textContent)).toEqual(["This project (Shop)", "All projects (3)"]);
    expect(scope.value).toBe("project");
    await waitFor(() => expect(screen.getAllByText("2 files, 4.0 KB")).toHaveLength(6));
    expect(screen.getByLabelText("OAuth 2.0 tokens")).toHaveProperty("checked", true);

    // The run history is kept.
    fireEvent.click(screen.getByLabelText("Run history"));
    fireEvent.click(screen.getByRole("button", { name: "Clear selected data…" }));
    const dialog = screen.getByRole("dialog", { name: "Clear local data?" });
    const items = within(dialog)
      .getAllByRole("listitem")
      .map((li) => li.textContent);
    expect(items).toEqual([
      "Request history: 2 files, 4.0 KB",
      "Cookies: 2 files, 4.0 KB",
      "OAuth 2.0 tokens: 2 files, 4.0 KB",
      "Cached schemas: 2 files, 4.0 KB",
      "Runtime variables: 2 files, 4.0 KB",
    ]);
    expect(within(dialog).getByText(/for the project Shop\. It cannot be undone/)).toBeTruthy();
    const confirmButton = within(dialog).getByRole("button", { name: "Clear data" });
    expect(confirmButton).toHaveProperty("disabled", true);
    fireEvent.change(within(dialog).getByLabelText("Type CLEAR to confirm"), { target: { value: "CLEAR" } });
    expect(confirmButton).toHaveProperty("disabled", false);

    service.Clear.mockResolvedValueOnce({
      scope: "project",
      projects: 1,
      removed: [
        { category: "history", files: 2, bytes: 4096 },
        { category: "cookies", files: 2, bytes: 4096 },
      ],
      failed: [{ category: "tokens", message: "PostSonde could not remove some of its local data.", hint: "Clear again." }],
    });
    await act(async () => {
      fireEvent.click(confirmButton);
    });
    expect(service.Clear).toHaveBeenCalledWith({
      scope: "project",
      categories: ["history", "cookies", "tokens", "schemas", "runtimeVariables"],
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("status").textContent).toBe("Removed 4 files (8.0 KB).");
    expect(screen.getByRole("alert").textContent).toBe(
      "Not all of “OAuth 2.0 tokens” could be removed: PostSonde could not remove some of its local data. Clear again.",
    );
    // Measured again afterwards.
    await waitFor(() => expect(service.Usage.mock.calls.length).toBeGreaterThanOrEqual(4));
  });

  it("clears every project, and only that, without an open project", async () => {
    service.Usage.mockImplementation(() => Promise.resolve(usage("all", "", 2, 0)));
    render(<LocalDataRows />);
    const scope = (await screen.findByLabelText("Data of")) as HTMLSelectElement;
    expect([...scope.options].map((o) => o.value)).toEqual(["all"]);
    expect(screen.getAllByText("Nothing stored")).toHaveLength(6);
    expect(service.Usage).not.toHaveBeenCalledWith({ scope: "project" });

    fireEvent.click(screen.getByRole("button", { name: "Clear selected data…" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/for all 2 projects/)).toBeTruthy();
    fireEvent.change(within(dialog).getByLabelText("Type CLEAR to confirm"), { target: { value: " CLEAR " } });
    service.Clear.mockResolvedValueOnce({ scope: "all", projects: 2, removed: [], failed: [] });
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Clear data" }));
    });
    expect(service.Clear).toHaveBeenCalledWith({ scope: "all", categories: CATEGORIES });
    expect(screen.getByRole("status").textContent).toBe("Nothing was stored, so nothing was removed.");
  });

  it("cannot clear with nothing ticked, and shows Go's error", async () => {
    const { unmount } = render(<LocalDataRows />);
    await screen.findByLabelText("Data of");
    for (const label of ["Request history", "Run history", "Cookies", "OAuth 2.0 tokens", "Cached schemas", "Runtime variables"]) {
      fireEvent.click(screen.getByLabelText(label));
    }
    expect(screen.getByRole("button", { name: "Clear selected data…" })).toHaveProperty("disabled", true);
    unmount();

    service.Usage.mockRejectedValue({
      kind: "unsupported",
      code: "localdata.unavailable",
      message: "PostSonde keeps no local data in this session.",
      hint: "It has no data directory, so nothing is written to disk.",
      details: {},
      causes: [],
    });
    render(<LocalDataRows />);
    expect((await screen.findByRole("alert")).textContent).toBe(
      "PostSonde keeps no local data in this session. It has no data directory, so nothing is written to disk.",
    );
  });
});
