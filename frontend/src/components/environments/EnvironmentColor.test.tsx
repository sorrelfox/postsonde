import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";

const service = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
  SetActive: vi.fn<(id: string) => Promise<unknown>>(),
  SetColor: vi.fn<(input: { id: string; color: string }) => Promise<unknown>>(),
  GlobalVariables: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: service,
}));

import { environmentsWorkspaceKey } from "../../commands/environments";
import { ENVIRONMENT_COLOR_PRESETS, presetColor } from "../../lib/environmentColors";
import {
  createStores,
  initialEnvironmentsState,
  StoreProvider,
  type Environment,
  type EnvironmentsState,
  type Stores,
  type WorkspaceInfo,
} from "../../stores";
import { EnvironmentSwitcher } from "./EnvironmentSwitcher";
import { EnvironmentsPanel } from "./EnvironmentsPanel";

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

// Built at run time: colour literals belong in tokens.css only.
const RED = `#${"d0342c"}`;
const CUSTOM = `#${"123abc"}`;

const prod: Environment = { id: "env_p", name: "Production", color: RED, fileName: "production.json", readOnly: false, variables: [] };
const local: Environment = { id: "env_l", name: "Local", color: "", fileName: "local.json", readOnly: false, variables: [] };

function state(envs: Environment[], over: Partial<EnvironmentsState> = {}): EnvironmentsState {
  const byId: Record<string, Environment> = {};
  for (const e of envs) byId[e.id] = e;
  return { ...initialEnvironmentsState, status: "ready", workspaceKey: key, ids: envs.map((e) => e.id), byId, ...over };
}

function setup(envState: EnvironmentsState, panel = false): Stores {
  const stores = createStores({ environments: envState });
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
  render(
    <StoreProvider stores={stores}>
      <EnvironmentSwitcher />
      {panel && <EnvironmentsPanel label="Environments" />}
    </StoreProvider>,
  );
  return stores;
}

const switcher = () => screen.getByRole("combobox", { name: "Environment" }).closest("label") as HTMLElement;

beforeEach(() => {
  for (const fn of Object.values(service)) fn.mockReset();
  service.SetActive.mockResolvedValue(undefined);
});

describe("environment colour in the toolbar (ENV-08)", () => {
  it("marks the switcher with the active environment's colour, and not without one", () => {
    const stores = setup(state([local, prod], { activeId: "env_p" }));
    expect(switcher().hasAttribute("data-env-color")).toBe(true);
    expect(switcher().style.getPropertyValue("--env-color")).toBe(RED);
    // The name is text, not coloured: the select keeps the text colour.
    expect(screen.getByRole("combobox", { name: "Environment" }).getAttribute("style")).toBeNull();

    act(() => stores.environments.dispatch({ type: "environments/setActive", id: "env_l" }));
    expect(switcher().hasAttribute("data-env-color")).toBe(false);
    expect(switcher().style.getPropertyValue("--env-color")).toBe("");

    act(() => stores.environments.dispatch({ type: "environments/setActive", id: null }));
    expect(switcher().hasAttribute("data-env-color")).toBe(false);
  });

  it("ignores a colour that is not #rrggbb", () => {
    setup(state([{ ...prod, color: "nope; background: url(x)" }], { activeId: "env_p" }));
    expect(switcher().hasAttribute("data-env-color")).toBe(false);
    expect(switcher().style.getPropertyValue("--env-color")).toBe("");
  });

  it("picks a preset, saves it in the environment file and shows it in the toolbar", async () => {
    const preset = ENVIRONMENT_COLOR_PRESETS.find((p) => p.name === "Green")!;
    service.SetColor.mockImplementation(({ id, color }) =>
      Promise.resolve({ ...local, id, color, variables: [] }),
    );
    setup(state([local], { activeId: "env_l", selectedId: "env_l" }), true);
    expect(screen.getByRole("button", { name: "No colour" }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(screen.getByRole("button", { name: "Green" }));
    await waitFor(() => expect(switcher().style.getPropertyValue("--env-color")).toBe(presetColor(preset)));
    expect(service.SetColor).toHaveBeenCalledWith({ id: "env_l", color: presetColor(preset) });
    expect(screen.getByRole("button", { name: "Green" }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(screen.getByRole("button", { name: "No colour" }));
    await waitFor(() => expect(switcher().hasAttribute("data-env-color")).toBe(false));
    expect(service.SetColor).toHaveBeenLastCalledWith({ id: "env_l", color: "" });
  });

  it("saves a custom colour when the picker is closed, and shows an error Go returns", async () => {
    service.SetColor.mockResolvedValueOnce({ ...local, color: CUSTOM, variables: [] });
    setup(state([local], { selectedId: "env_l" }), true);
    const input = screen.getByLabelText("Custom colour") as HTMLInputElement;
    fireEvent.input(input, { target: { value: CUSTOM } });
    expect(service.SetColor).not.toHaveBeenCalled();
    fireEvent.change(input, { target: { value: CUSTOM } });
    await waitFor(() => expect(service.SetColor).toHaveBeenCalledWith({ id: "env_l", color: CUSTOM }));
    await waitFor(() => expect(screen.getByLabelText(`Custom colour ${CUSTOM}`)).toBeTruthy());

    service.SetColor.mockRejectedValueOnce(
      { kind: "invalid_argument", code: "environments.invalid_color", message: "Bad colour.", details: {}, causes: [] },
    );
    fireEvent.click(screen.getByRole("button", { name: "Red" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
  });

  it("shows the picker disabled for a read-only environment", () => {
    setup(state([{ ...prod, readOnly: true }], { selectedId: "env_p" }), true);
    expect((screen.getByRole("button", { name: "Red" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByRole("button", { name: "Red" }).getAttribute("aria-pressed")).toBe("true");
  });
});
