import { describe, expect, it } from "vitest";
import {
  environmentsReducer,
  initialEnvironmentsState,
  isEnvironmentColor,
  selectActiveEnvironment,
  selectSelectedEnvironment,
  type Environment,
  type EnvironmentsState,
} from "./environments";

const env = (id: string, name: string, over: Partial<Environment> = {}): Environment => ({
  id,
  name,
  color: "",
  fileName: `${name.toLowerCase()}.json`,
  readOnly: false,
  variables: [],
  ...over,
});

const local = env("env_l", "Local");
const staging = env("env_s", "Staging", { variables: [{ name: "baseUrl", value: "x", description: "", secret: false, enabled: true }] });
const prod = env("env_p", "production");

function loaded(activeId: string | null = null, envs = [staging, prod, local]): EnvironmentsState {
  const s = environmentsReducer(initialEnvironmentsState, { type: "environments/loading", workspaceKey: "ws" });
  return environmentsReducer(s, {
    type: "environments/loaded",
    workspaceKey: "ws",
    environments: envs,
    activeId,
    problems: [{ fileName: "bad.json", message: "Broken", code: "projectfile.parse_failed" }],
    readOnly: false,
  });
}

describe("environmentsReducer", () => {
  it("loads, sorts by name and selects the active environment", () => {
    const s = loaded("env_s");
    expect(s.status).toBe("ready");
    expect(s.ids).toEqual(["env_l", "env_p", "env_s"]);
    expect(s.activeId).toBe("env_s");
    expect(s.selectedId).toBe("env_s");
    expect(s.problems).toHaveLength(1);
    expect(selectActiveEnvironment(s)?.name).toBe("Staging");
    expect(selectSelectedEnvironment(s)?.name).toBe("Staging");
  });

  it("drops an active id that does not exist and selects the first environment", () => {
    const s = loaded("env_gone");
    expect(s.activeId).toBeNull();
    expect(s.selectedId).toBe("env_l");
    expect(selectActiveEnvironment(s)).toBeNull();
  });

  it("ignores results for another workspace and resets for a new one", () => {
    const s = loaded();
    expect(
      environmentsReducer(s, {
        type: "environments/loaded",
        workspaceKey: "other",
        environments: [],
        activeId: null,
        problems: [],
        readOnly: false,
      }),
    ).toBe(s);
    expect(environmentsReducer(s, { type: "environments/failed", workspaceKey: "other", error: "x" })).toBe(s);

    const other = environmentsReducer(s, { type: "environments/loading", workspaceKey: "other" });
    expect(other.ids).toEqual([]);
    expect(other.status).toBe("loading");

    const again = environmentsReducer(s, { type: "environments/loading", workspaceKey: "ws" });
    expect(again.ids).toHaveLength(3);
    const failed = environmentsReducer(again, { type: "environments/failed", workspaceKey: "ws", error: "nope" });
    expect(failed.status).toBe("error");
    expect(failed.error).toBe("nope");
  });

  it("puts, removes, activates and selects", () => {
    let s = loaded("env_p");
    s = environmentsReducer(s, { type: "environments/put", environment: env("env_n", "Alpha") });
    expect(s.ids[0]).toBe("env_n");
    s = environmentsReducer(s, { type: "environments/put", environment: { ...prod, name: "Zulu" } });
    expect(s.ids[s.ids.length - 1]).toBe("env_p");

    s = environmentsReducer(s, { type: "environments/select", id: "env_p" });
    s = environmentsReducer(s, { type: "environments/remove", id: "env_p" });
    expect(s.byId.env_p).toBeUndefined();
    expect(s.activeId).toBeNull();
    expect(s.selectedId).toBe("env_s");

    expect(environmentsReducer(s, { type: "environments/remove", id: "env_p" })).toBe(s);
    expect(environmentsReducer(s, { type: "environments/setActive", id: "env_missing" })).toBe(s);
    expect(environmentsReducer(s, { type: "environments/select", id: "env_missing" })).toBe(s);

    s = environmentsReducer(s, { type: "environments/setActive", id: "env_l" });
    expect(s.activeId).toBe("env_l");
    expect(environmentsReducer(s, { type: "environments/setActive", id: "env_l" })).toBe(s);
    s = environmentsReducer(s, { type: "environments/setActive", id: null });
    expect(s.activeId).toBeNull();

    expect(environmentsReducer(s, { type: "environments/clear" })).toBe(initialEnvironmentsState);
  });

  it("copies what it stores", () => {
    const vars = [{ name: "a", value: "1", description: "", secret: false, enabled: true }];
    const input = env("env_x", "X", { variables: vars });
    const s = environmentsReducer(loaded(), { type: "environments/put", environment: input });
    expect(s.byId.env_x).toEqual(input);
    expect(s.byId.env_x.variables).not.toBe(vars);
  });
});

// Built at run time: colour literals belong in tokens.css only (scripts/check-css-tokens.sh).
const hex = (digits: string) => `#${digits}`;

describe("isEnvironmentColor", () => {
  it.each([
    [hex("d0342c"), true],
    [hex("D0342C"), true],
    [hex("fff"), false],
    ["red", false],
    [hex("d0342c; x"), false],
    ["", false],
  ])("%s → %s", (color, want) => {
    expect(isEnvironmentColor(color)).toBe(want);
  });
});
