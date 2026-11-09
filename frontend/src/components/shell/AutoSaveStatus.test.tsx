import { describe, expect, it } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { createStores, StoreProvider } from "../../stores";
import { AutoSaveStatus } from "./AutoSaveStatus";

function renderStatus() {
  const stores = createStores();
  render(
    <StoreProvider stores={stores}>
      <AutoSaveStatus />
    </StoreProvider>,
  );
  return stores;
}

describe("AutoSaveStatus", () => {
  it("shows ready, saving, auto-saved and a failure with details", () => {
    const stores = renderStatus();
    expect(screen.getByRole("status").textContent).toContain("Ready");

    act(() => stores.autosave.dispatch({ type: "autosave/queued", id: "req_1", revision: 1 }));
    expect(screen.getByRole("status").textContent).toContain("Saving…");

    act(() =>
      stores.autosave.dispatch({ type: "autosave/saved", id: "req_1", revision: 1, at: "2026-09-16T10:00:00Z" }),
    );
    const saved = screen.getByRole("status");
    expect(saved.textContent).toContain("Auto-saved");
    expect(saved.getAttribute("title")).toMatch(/^Saved at /);

    act(() =>
      stores.autosave.dispatch({
        type: "autosave/failed",
        id: "req_1",
        revision: 2,
        error: { message: "A project file could not be saved.", hint: "Check the disk.", code: "projectfile.save_failed" },
        at: "2026-09-16T10:00:01Z",
      }),
    );
    const button = screen.getByRole("button", { name: "Save failed" });
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText(/could not be saved/)).toBeNull();

    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("A project file could not be saved. Check the disk.")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(stores.autosave.getState().failed).toEqual({});
    expect(screen.getByRole("status").textContent).toContain("Auto-saved");
  });

  it("counts several failures", () => {
    const stores = renderStatus();
    const error = { message: "No.", hint: "", code: "collection.read_only" };
    act(() => {
      stores.autosave.dispatch({ type: "autosave/failed", id: "a", revision: 1, error, at: "t1" });
      stores.autosave.dispatch({ type: "autosave/failed", id: "b", revision: 1, error, at: "t2" });
    });
    expect(screen.getByRole("button", { name: "2 saves failed" }).getAttribute("title")).toBe("No.");
  });
});
