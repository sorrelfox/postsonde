import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  List: vi.fn<() => Promise<unknown>>(),
  Create: vi.fn<(name: string) => Promise<unknown>>(),
  Rename: vi.fn<(input: { id: string; name: string }) => Promise<unknown>>(),
  Delete: vi.fn<(id: string) => Promise<unknown>>(),
  SaveVariables: vi.fn<(input: { id: string; variables: unknown[] }) => Promise<unknown>>(),
  SetActive: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: service,
}));

import type { AppError } from "../lib/errors";
import type { EnvironmentDTO } from "../lib/environments";
import { createStores, type Stores, type WorkspaceInfo } from "../stores";
import { deferred, settle } from "../test/fakeWails";
import {
  createEnvironment,
  deleteEnvironment,
  environmentsWorkspaceKey,
  followEnvironmentsWorkspace,
  loadEnvironments,
  renameEnvironment,
  saveEnvironmentVariables,
  setActiveEnvironment,
} from "./environments";

const shop: WorkspaceInfo = {
  id: "ws_1",
  name: "Shop",
  path: "/p/shop",
  localDir: "/data/ws_1",
  localStatus: "existing",
  previousPath: "",
  newerFormat: false,
};

function dto(over: Partial<EnvironmentDTO> = {}): EnvironmentDTO {
  return { id: "env_s", name: "Staging", color: "", fileName: "staging.json", variables: [], readOnly: false, ...over };
}

function openStores(workspace: WorkspaceInfo | null = shop): Stores {
  const stores = createStores();
  stores.workspace.dispatch({ type: "workspace/setCurrent", workspace });
  return stores;
}

function rejection(code: string, message = "Nope"): Error {
  const err = new Error(message);
  err.name = "RuntimeError";
  (err as Error & { cause: unknown }).cause = { kind: "not_found", code, message, hint: "", details: {}, causes: [] };
  return err;
}

beforeEach(() => {
  for (const fn of Object.values(service)) fn.mockReset();
  service.List.mockResolvedValue({
    environments: [dto(), dto({ id: "env_l", name: "Local", fileName: "local.json", variables: null })],
    activeId: "env_s",
    problems: null,
    readOnly: false,
  });
});

describe("environment commands", () => {
  it("loads the environments of the open project", async () => {
    const stores = openStores();
    await loadEnvironments(stores);
    const s = stores.environments.getState();
    expect(s.status).toBe("ready");
    expect(s.workspaceKey).toBe(environmentsWorkspaceKey(shop));
    expect(s.ids).toEqual(["env_l", "env_s"]);
    expect(s.byId.env_l.variables).toEqual([]);
    expect(s.activeId).toBe("env_s");
    expect(s.problems).toEqual([]);
  });

  it("clears without a project and keeps a load failure in the store", async () => {
    const none = openStores(null);
    await loadEnvironments(none);
    expect(service.List).not.toHaveBeenCalled();
    expect(none.environments.getState().status).toBe("idle");

    const stores = openStores();
    service.List.mockRejectedValue(rejection("environments.list_failed", "Cannot read"));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    await loadEnvironments(stores);
    warn.mockRestore();
    expect(stores.environments.getState().status).toBe("error");
    expect(stores.environments.getState().error).toBe("Cannot read");
  });

  it("reloads when another project opens and clears when it closes", async () => {
    const stores = openStores();
    const stop = followEnvironmentsWorkspace(stores);
    await settle();
    expect(service.List).toHaveBeenCalledTimes(1);

    // The same project again: no reload.
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: { ...shop } });
    expect(service.List).toHaveBeenCalledTimes(1);

    const pending = deferred<unknown>();
    service.List.mockReturnValueOnce(pending.promise);
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: { ...shop, id: "ws_2", path: "/p/other" } });
    expect(service.List).toHaveBeenCalledTimes(2);
    expect(stores.environments.getState().ids).toEqual([]);

    // Closed before the list arrived: the late result is dropped.
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: null });
    pending.resolve({ environments: [dto()], activeId: "", problems: [], readOnly: false });
    await settle();
    expect(stores.environments.getState().status).toBe("idle");
    expect(stores.environments.getState().ids).toEqual([]);

    stop();
    stores.workspace.dispatch({ type: "workspace/setCurrent", workspace: shop });
    expect(service.List).toHaveBeenCalledTimes(2);
  });

  it("creates, renames, saves and deletes", async () => {
    const stores = openStores();
    await loadEnvironments(stores);

    service.Create.mockResolvedValue(dto({ id: "env_n", name: "Production", fileName: "production.json" }));
    await createEnvironment(stores, "  Production ");
    expect(service.Create).toHaveBeenCalledWith("Production");
    expect(stores.environments.getState().selectedId).toBe("env_n");

    service.Rename.mockResolvedValue(dto({ id: "env_n", name: "Prod", fileName: "prod.json" }));
    await renameEnvironment(stores, "env_n", "Prod");
    expect(service.Rename).toHaveBeenCalledWith({ id: "env_n", name: "Prod" });
    expect(stores.environments.getState().byId.env_n.name).toBe("Prod");

    const variables = [
      { name: "token", value: "s3cret", description: "", secret: true, enabled: true, localValue: null },
      { name: "base", value: "https://shared", description: "", secret: false, enabled: true, localValue: "http://localhost" },
    ];
    service.SaveVariables.mockResolvedValue(dto({ id: "env_n", name: "Prod", variables }));
    await saveEnvironmentVariables(stores, "env_n", variables);
    expect(service.SaveVariables).toHaveBeenCalledWith({ id: "env_n", variables });
    expect(stores.environments.getState().byId.env_n.variables).toEqual(variables);

    // A secret variable never carries a local value, either way.
    await saveEnvironmentVariables(stores, "env_n", [{ ...variables[0], localValue: "stray" }]);
    expect(service.SaveVariables).toHaveBeenLastCalledWith({ id: "env_n", variables: [variables[0]] });

    service.Delete.mockResolvedValue(undefined);
    await deleteEnvironment(stores, "env_n");
    expect(stores.environments.getState().byId.env_n).toBeUndefined();

    service.Create.mockRejectedValue(rejection("environments.name_taken", "Taken"));
    const err = (await createEnvironment(stores, "Local").catch((e: unknown) => e)) as AppError;
    expect(err.code).toBe("environments.name_taken");
    expect(err.message).toBe("Taken");
  });

  it("switches the active environment at once and undoes a failed switch", async () => {
    const stores = openStores();
    await loadEnvironments(stores);

    service.SetActive.mockResolvedValue(undefined);
    await setActiveEnvironment(stores, "env_l");
    expect(service.SetActive).toHaveBeenCalledWith("env_l");
    expect(stores.environments.getState().activeId).toBe("env_l");

    await setActiveEnvironment(stores, null);
    expect(service.SetActive).toHaveBeenLastCalledWith("");
    expect(stores.environments.getState().activeId).toBeNull();

    const pending = deferred<unknown>();
    service.SetActive.mockReturnValueOnce(pending.promise);
    const call = setActiveEnvironment(stores, "env_s");
    expect(stores.environments.getState().activeId).toBe("env_s");
    pending.reject(rejection("environments.local_data_failed"));
    await expect(call).rejects.toMatchObject({ code: "environments.local_data_failed" });
    expect(stores.environments.getState().activeId).toBeNull();
  });
});
