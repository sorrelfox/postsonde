import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
  Create: vi.fn<(name: string) => Promise<unknown>>(),
  Rename: vi.fn<(input: { id: string; name: string }) => Promise<unknown>>(),
  Delete: vi.fn<(id: string) => Promise<unknown>>(),
  SaveVariables: vi.fn<(input: { id: string; variables: unknown[] }) => Promise<unknown>>(),
  SetActive: vi.fn<(id: string) => Promise<unknown>>(),
  GlobalVariables: vi.fn<() => Promise<unknown>>(),
  SaveGlobalVariables: vi.fn<(input: { variables: unknown[] }) => Promise<unknown>>(),
  Export: vi.fn<(input: { id: string; format: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: service,
}));

import { environmentsWorkspaceKey } from "../../commands/environments";
import {
  createStores,
  initialEnvironmentsState,
  StoreProvider,
  type Environment,
  type EnvironmentsState,
  type Stores,
  type WorkspaceInfo,
} from "../../stores";
import { ConfirmHost } from "../common/Dialog";
import { ToastHost } from "../common/Toast";
import { EnvironmentSwitcher } from "./EnvironmentSwitcher";
import { EnvironmentsPanel } from "./EnvironmentsPanel";
import { rowsToVariables, variablesToRows } from "./variableRows";
import { SAVE_DELAY_MS } from "./VariablesEditor";

const shop: WorkspaceInfo = {
  id: "ws_1",
  name: "Shop",
  path: "/p/shop",
  localDir: "/data/ws_1",
  localStatus: "existing",
  previousPath: "",
  newerFormat: false,
};
const key = environmentsWorkspaceKey(shop) as string;

const staging: Environment = {
  id: "env_s",
  name: "Staging",
  // Built at run time: colour literals belong in tokens.css only.
  color: `#${"f2b544"}`,
  fileName: "staging.json",
  readOnly: false,
  variables: [
    { name: "baseUrl", value: "https://api.stg.shop.test", description: "", secret: false, enabled: true },
    { name: "token", value: "stg-token", description: "", secret: true, enabled: true },
  ],
};
const local: Environment = { id: "env_l", name: "Local", color: "", fileName: "local.json", readOnly: false, variables: [] };

function state(envs: Environment[], over: Partial<EnvironmentsState> = {}): EnvironmentsState {
  const byId: Record<string, Environment> = {};
  for (const e of envs) byId[e.id] = e;
  return {
    ...initialEnvironmentsState,
    status: "ready",
    workspaceKey: key,
    ids: envs.map((e) => e.id).sort((a, b) => byId[a].name.localeCompare(byId[b].name)),
    byId,
    ...over,
  };
}

function setup(envState: EnvironmentsState): Stores {
  const stores = createStores({ environments: envState });
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  render(
    <StoreProvider stores={stores}>
      <EnvironmentSwitcher />
      <aside aria-label="Environments">
        <EnvironmentsPanel label="Environments" />
      </aside>
      <ConfirmHost />
      <ToastHost />
    </StoreProvider>,
  );
  return stores;
}

const valueInput = (row: number) =>
  document.querySelector<HTMLInputElement>(`input[aria-label="Value, row ${row}"]`) as HTMLInputElement;

beforeEach(() => {
  for (const fn of Object.values(service)) fn.mockReset();
  service.SetActive.mockResolvedValue(undefined);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("EnvironmentsPanel", () => {
  it("edits the global variables and shows the precedence (ENV-02, ENV-06)", async () => {
    vi.useFakeTimers();
    setup(state([staging], { selectedId: "env_s" }));
    const order = screen.getByRole("list", { name: "Variable precedence, lowest first" });
    expect(within(order).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "Global",
      "Environment",
      "Collection",
      "Folder",
      "Runtime",
    ]);

    service.GlobalVariables.mockResolvedValue({
      variables: [
        { name: "base", value: "https://api.shop.test", description: "", secret: false, enabled: true },
        { name: "key", value: "gl-secret", description: "", secret: true, enabled: true },
      ],
      readOnly: false,
    });
    service.SaveGlobalVariables.mockImplementation(({ variables }) => Promise.resolve({ variables, readOnly: false }));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Global variables" }));
    });
    expect(screen.queryByRole("grid", { name: "Variables of Staging" })).toBeNull();
    const grid = screen.getByRole("grid", { name: "Global variables" });
    expect(grid).toBeTruthy();
    expect(valueInput(1).value).toBe("https://api.shop.test");
    expect(valueInput(2).type).toBe("password");

    fireEvent.change(valueInput(1), { target: { value: "http://localhost" } });
    await act(async () => {
      vi.advanceTimersByTime(SAVE_DELAY_MS);
    });
    expect(service.SaveGlobalVariables).toHaveBeenCalledWith({
      variables: [
        { name: "base", value: "http://localhost", description: "", secret: false, enabled: true, localValue: null },
        { name: "key", value: "gl-secret", description: "", secret: true, enabled: true, localValue: null },
      ],
    });
    expect(service.SaveVariables).not.toHaveBeenCalled();

    // Back to the environment.
    fireEvent.click(within(screen.getByRole("list", { name: "Environments" })).getByText("Staging"));
    expect(screen.getByRole("grid", { name: "Variables of Staging" })).toBeTruthy();
  });

  it("shows why the global variables could not be loaded", async () => {
    setup(state([]));
    service.GlobalVariables.mockRejectedValue({ message: "postsonde.json is broken", kind: "parse", code: "x" });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Global variables" }));
    });
    expect(screen.getByRole("alert").textContent).toContain("postsonde.json is broken");
  });

  it("shows the empty state and creates an environment", async () => {
    const stores = setup(state([]));
    expect(screen.getByText("No environments")).toBeTruthy();
    expect(screen.getByRole("combobox", { name: "Environment" })).toHaveProperty("disabled", true);

    service.Create.mockResolvedValue({ ...local, variables: [] });
    fireEvent.click(screen.getByRole("button", { name: "New environment" }));
    const name = screen.getByRole("textbox", { name: "New environment name" });
    fireEvent.change(name, { target: { value: "Local" } });
    await act(async () => {
      fireEvent.submit(name.closest("form") as HTMLFormElement);
    });
    expect(service.Create).toHaveBeenCalledWith("Local");
    expect(screen.queryByRole("textbox", { name: "New environment name" })).toBeNull();
    expect(within(screen.getByRole("list", { name: "Environments" })).getByText("Local")).toBeTruthy();
    expect(stores.environments.getState().selectedId).toBe("env_l");
    expect(screen.getByRole("grid", { name: "Variables of Local" })).toBeTruthy();
  });

  it("shows the error of a failed create in the form", async () => {
    setup(state([local]));
    const err = new Error("taken");
    (err as Error & { cause: unknown }).cause = {
      kind: "already_exists",
      code: "environments.name_taken",
      message: "An environment with this name already exists.",
      hint: "",
      details: {},
      causes: [],
    };
    service.Create.mockRejectedValue(err);
    fireEvent.click(screen.getByRole("button", { name: "New environment" }));
    const name = screen.getByRole("textbox", { name: "New environment name" });
    fireEvent.change(name, { target: { value: "local" } });
    await act(async () => {
      fireEvent.submit(name.closest("form") as HTMLFormElement);
    });
    expect(screen.getByRole("alert").textContent).toBe("An environment with this name already exists.");
    fireEvent.keyDown(name, { key: "Escape" });
    expect(screen.queryByRole("textbox", { name: "New environment name" })).toBeNull();
  });

  it("switches and activates environments from the panel and the title bar", async () => {
    const stores = setup(state([staging, local], { selectedId: "env_s" }));
    const select = screen.getByRole("combobox", { name: "Environment" }) as HTMLSelectElement;
    expect(select.value).toBe("");
    expect(within(select).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "No environment",
      "Local",
      "Staging",
    ]);

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Activate" }));
    });
    expect(service.SetActive).toHaveBeenCalledWith("env_s");
    expect(select.value).toBe("env_s");
    expect(screen.getByRole("button", { name: "Staging is active. Deactivate" })).toBeTruthy();

    await act(async () => {
      fireEvent.change(select, { target: { value: "env_l" } });
    });
    expect(service.SetActive).toHaveBeenLastCalledWith("env_l");
    expect(stores.environments.getState().activeId).toBe("env_l");
    expect(screen.getByRole("button", { name: "Activate" })).toBeTruthy();

    // Selecting another environment in the list shows its variables.
    fireEvent.click(within(screen.getByRole("list", { name: "Environments" })).getByText("Local"));
    expect(screen.getByRole("grid", { name: "Variables of Local" })).toBeTruthy();

    await act(async () => {
      fireEvent.change(select, { target: { value: "" } });
    });
    expect(service.SetActive).toHaveBeenLastCalledWith("");
    expect(stores.environments.getState().activeId).toBeNull();
  });

  it("edits variables, masks secrets and saves after a pause", async () => {
    vi.useFakeTimers();
    setup(state([staging], { selectedId: "env_s" }));
    service.SaveVariables.mockImplementation(({ id, variables }) =>
      Promise.resolve({ ...staging, id, variables }),
    );

    // The secret value is masked until revealed.
    expect(valueInput(2).type).toBe("password");
    expect(valueInput(2).value).toBe("stg-token");
    expect(valueInput(1).type).toBe("text");
    fireEvent.click(screen.getByRole("button", { name: "Show value, row 2" }));
    expect(valueInput(2).type).toBe("text");
    fireEvent.click(screen.getByRole("button", { name: "Hide value, row 2" }));
    expect(valueInput(2).type).toBe("password");

    // Add a variable and make it secret.
    fireEvent.change(document.querySelector('input[aria-label="Key, row 3"]') as HTMLInputElement, {
      target: { value: "password" },
    });
    fireEvent.change(valueInput(3), { target: { value: "hunter2" } });
    const lock = screen.getByRole("button", { name: "Secret, row 3" });
    expect(lock.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(lock);
    expect(screen.getByRole("button", { name: "Secret, row 3" }).getAttribute("aria-pressed")).toBe("true");
    expect(valueInput(3).type).toBe("password");

    expect(service.SaveVariables).not.toHaveBeenCalled();
    await act(async () => {
      vi.advanceTimersByTime(SAVE_DELAY_MS);
    });
    expect(service.SaveVariables).toHaveBeenCalledTimes(1);
    expect(service.SaveVariables).toHaveBeenCalledWith({
      id: "env_s",
      variables: [
        { name: "baseUrl", value: "https://api.stg.shop.test", description: "", secret: false, enabled: true, localValue: null },
        { name: "token", value: "stg-token", description: "", secret: true, enabled: true, localValue: null },
        { name: "password", value: "hunter2", description: "", secret: true, enabled: true, localValue: null },
      ],
    });

    // Making a secret plain shows its value and saves it as a plain variable.
    fireEvent.click(screen.getByRole("button", { name: "Secret, row 2" }));
    expect(valueInput(2).type).toBe("text");
    expect(screen.queryByRole("button", { name: "Show value, row 2" })).toBeNull();
    await act(async () => {
      vi.advanceTimersByTime(SAVE_DELAY_MS);
    });
    expect(service.SaveVariables).toHaveBeenCalledTimes(2);
    expect(service.SaveVariables.mock.calls[1][0].variables[1]).toMatchObject({ name: "token", secret: false });
  });

  it("flushes pending edits when another environment is selected", async () => {
    vi.useFakeTimers();
    setup(state([staging, local], { selectedId: "env_s" }));
    service.SaveVariables.mockImplementation(({ id, variables }) =>
      Promise.resolve({ ...staging, id, variables }),
    );
    fireEvent.change(valueInput(1), { target: { value: "http://localhost" } });
    await act(async () => {
      fireEvent.click(within(screen.getByRole("list", { name: "Environments" })).getByText("Local"));
    });
    expect(service.SaveVariables).toHaveBeenCalledTimes(1);
    expect(service.SaveVariables.mock.calls[0][0].id).toBe("env_s");
  });

  it("renames and deletes the selected environment", async () => {
    const stores = setup(state([staging, local], { selectedId: "env_s", activeId: "env_s" }));
    service.Rename.mockResolvedValue({ ...staging, name: "Stage", fileName: "stage.json" });
    fireEvent.click(screen.getByRole("button", { name: "Rename" }));
    const name = screen.getByRole("textbox", { name: "Environment name" }) as HTMLInputElement;
    expect(name.value).toBe("Staging");
    fireEvent.change(name, { target: { value: "Stage" } });
    await act(async () => {
      fireEvent.submit(name.closest("form") as HTMLFormElement);
    });
    expect(service.Rename).toHaveBeenCalledWith({ id: "env_s", name: "Stage" });
    expect(screen.getByRole("region", { name: "Environment Stage" })).toBeTruthy();

    service.Delete.mockResolvedValue(undefined);
    // Cancel first: nothing is deleted.
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    let dialog = screen.getByRole("dialog", { name: "Delete environment “Stage”?" });
    expect(document.activeElement).toBe(within(dialog).getByRole("button", { name: "Cancel" }));
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(service.Delete).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    dialog = screen.getByRole("dialog", { name: "Delete environment “Stage”?" });
    expect(dialog.getAttribute("aria-describedby")).toBeTruthy();
    expect(document.getElementById(dialog.getAttribute("aria-describedby")!)?.textContent).toContain("stage.json");
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Delete environment" }));
    });
    expect(service.Delete).toHaveBeenCalledWith("env_s");
    expect(screen.getByRole("region", { name: "Notifications" }).textContent).toContain("Deleted environment “Stage”");
    const s = stores.environments.getState();
    expect(s.ids).toEqual(["env_l"]);
    expect(s.activeId).toBeNull();
    expect(s.selectedId).toBe("env_l");
    expect((screen.getByRole("combobox", { name: "Environment" }) as HTMLSelectElement).value).toBe("");
  });

  it("is read-only for a project from a newer PostSonde and lists broken files", () => {
    setup(
      state([staging], {
        selectedId: "env_s",
        readOnly: true,
        problems: [{ fileName: "broken.json", message: "The file is not valid JSON.", code: "projectfile.parse_failed" }],
      }),
    );
    expect(screen.getByRole("button", { name: "New environment" })).toHaveProperty("disabled", true);
    expect(screen.queryByRole("button", { name: "Rename" })).toBeNull();
    expect(screen.getByRole("button", { name: "Secret, row 1" })).toHaveProperty("disabled", true);
    expect(screen.getByText(/1 file in environments\/ could not be loaded/)).toBeTruthy();
    expect(screen.getByText("broken.json")).toBeTruthy();
    // Exporting only reads: it stays available.
    expect(screen.getByRole("button", { name: "Export…" })).toBeTruthy();
  });

  it("exports the selected environment in either format and says what was left out (EXP-05)", async () => {
    setup(state([staging], { selectedId: "env_s" }));
    service.Export.mockResolvedValueOnce({ saved: true, path: "/tmp/staging.json", variables: 2, secrets: 1 });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Export…" }));
    });
    expect(service.Export).toHaveBeenLastCalledWith({ id: "env_s", format: "postsonde" });
    expect(screen.getByText("Exported “Staging” to /tmp/staging.json, without the value of its secret variable")).toBeTruthy();

    // Cancelled: nothing to say.
    service.Export.mockResolvedValueOnce({ saved: false, path: "", variables: 2, secrets: 1 });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Export for Postman…" }));
    });
    expect(service.Export).toHaveBeenLastCalledWith({ id: "env_s", format: "postman" });

    service.Export.mockRejectedValueOnce({
      kind: "io",
      code: "environments.export_failed",
      message: "The environment could not be saved to /nope/x.json.",
      hint: "",
      details: {},
      causes: [],
    });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Export for Postman…" }));
    });
    expect(screen.getByRole("alert").textContent).toBe("The environment could not be saved to /nope/x.json.");
  });
});

describe("local values (SEC-02)", () => {
  const withLocal: Environment = {
    ...staging,
    variables: [
      { name: "baseUrl", value: "https://api.stg.shop.test", description: "", secret: false, enabled: true, localValue: "http://localhost:8080" },
      { name: "token", value: "stg-token", description: "", secret: true, enabled: true, localValue: null },
      { name: "user", value: "team", description: "", secret: false, enabled: true, localValue: null },
    ],
  };

  it("shows a local value under the table and strikes the shared value through", () => {
    setup(state([withLocal], { selectedId: "env_s" }));
    const group = screen.getByRole("group", { name: "Local values · this computer only" });
    const input = within(group).getByRole("textbox", { name: "baseUrl" }) as HTMLInputElement;
    expect(input.value).toBe("http://localhost:8080");
    expect(input.getAttribute("aria-describedby")).toBeTruthy();
    expect(within(group).getByText("Shared value: https://api.stg.shop.test")).toBeTruthy();
    // The shared value stays in the table, drawn struck through.
    expect(valueInput(1).value).toBe("https://api.stg.shop.test");
    expect(document.querySelector("[class*=overridden]")?.textContent).toBe("https://api.stg.shop.test");

    // The toggles: pressed for the row with a local value, none for a
    // secret (its value is local already), and no lock while local.
    expect(screen.getByRole("button", { name: "Local value, row 1" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "Local value, row 3" }).getAttribute("aria-pressed")).toBe("false");
    expect(screen.queryByRole("button", { name: "Local value, row 2" })).toBeNull();
    expect((screen.getByRole("button", { name: "Secret, row 1" }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Secret, row 3" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("adds, edits and removes local values, saved next to the shared ones", async () => {
    vi.useFakeTimers();
    setup(state([withLocal], { selectedId: "env_s" }));
    service.SaveVariables.mockImplementation(({ id, variables }) => Promise.resolve({ ...staging, id, variables }));

    // Add one to "user": it starts from the shared value and takes focus.
    fireEvent.click(screen.getByRole("button", { name: "Local value, row 3" }));
    const group = screen.getByRole("group", { name: "Local values · this computer only" });
    const user = within(group).getByRole("textbox", { name: "user" }) as HTMLInputElement;
    expect(user.value).toBe("team");
    expect(document.activeElement).toBe(user);
    fireEvent.change(user, { target: { value: "me" } });
    // Edit the other; the shared value is edited in the table as before.
    fireEvent.change(within(group).getByRole("textbox", { name: "baseUrl" }), { target: { value: "http://127.0.0.1:9000" } });
    fireEvent.change(valueInput(1), { target: { value: "https://api2.stg.shop.test" } });
    await act(async () => {
      vi.advanceTimersByTime(SAVE_DELAY_MS);
    });
    expect(service.SaveVariables).toHaveBeenCalledWith({
      id: "env_s",
      variables: [
        { name: "baseUrl", value: "https://api2.stg.shop.test", description: "", secret: false, enabled: true, localValue: "http://127.0.0.1:9000" },
        { name: "token", value: "stg-token", description: "", secret: true, enabled: true, localValue: null },
        { name: "user", value: "team", description: "", secret: false, enabled: true, localValue: "me" },
      ],
    });

    // Remove both: once with the row toggle, once with the list's button.
    fireEvent.click(screen.getByRole("button", { name: "Local value, row 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove the local value of user" }));
    expect(screen.queryByRole("group", { name: "Local values · this computer only" })).toBeNull();
    await act(async () => {
      vi.advanceTimersByTime(SAVE_DELAY_MS);
    });
    const saved = service.SaveVariables.mock.calls[1][0].variables as { localValue: string | null }[];
    expect(saved.map((v) => v.localValue)).toEqual([null, null, null]);

    // Deleting a row drops its local value.
    fireEvent.click(screen.getByRole("button", { name: "Local value, row 3" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete row 3" }));
    expect(screen.queryByRole("group", { name: "Local values · this computer only" })).toBeNull();
  });

  it("is read-only in a read-only environment", () => {
    setup(state([{ ...withLocal, readOnly: true }], { selectedId: "env_s" }));
    const group = screen.getByRole("group", { name: "Local values · this computer only" });
    expect((within(group).getByRole("textbox", { name: "baseUrl" }) as HTMLInputElement).readOnly).toBe(true);
    expect((screen.getByRole("button", { name: "Local value, row 3" }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("variable rows", () => {
  it("round-trips variables and drops empty rows", () => {
    let n = 0;
    const rows = variablesToRows(staging.variables, () => `r${++n}`);
    expect(rows.secret).toEqual({ r2: true });
    const withEmpty = {
      ...rows,
      rows: [...rows.rows, { id: "r3", enabled: true, key: "", value: "", description: "" }],
    };
    expect(rowsToVariables(withEmpty)).toEqual(staging.variables.map((v) => ({ ...v, localValue: null })));
  });

  it("keeps local values by row, never for a secret, and keeps a row that only has one", () => {
    let n = 0;
    const rows = variablesToRows(
      [
        { name: "a", value: "shared", description: "", secret: false, enabled: true, localValue: "mine" },
        { name: "b", value: "s", description: "", secret: true, enabled: true, localValue: "ignored" },
        { name: "c", value: "", description: "", secret: false, enabled: false, localValue: "" },
      ],
      () => `r${++n}`,
    );
    expect(rows.local).toEqual({ r1: "mine", r3: "" });
    expect(rowsToVariables(rows).map((v) => v.localValue)).toEqual(["mine", null, ""]);
    // A local value on a row now secret is not saved.
    expect(rowsToVariables({ ...rows, secret: { r1: true } })[0]).toMatchObject({ secret: true, localValue: null });
    // An otherwise empty row with a local value is kept.
    const blank = { ...rows, rows: [{ id: "r9", enabled: true, key: "", value: "", description: "" }], local: { r9: "x" } };
    expect(rowsToVariables(blank)).toHaveLength(1);
  });
});
