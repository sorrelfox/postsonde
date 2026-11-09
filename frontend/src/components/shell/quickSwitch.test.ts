import { describe, expect, it } from "vitest";
import type { Environment, TreeNode } from "../../stores";
import { buildSwitchItems, environmentKey, nodeKey, searchSwitchItems, touchRecent } from "./quickSwitch";

const folder = (id: string, name: string, parentId: string | null, childIds: string[]): TreeNode => ({
  kind: "folder",
  id,
  parentId,
  name,
  path: id,
  fileKind: "",
  readOnly: "",
  childIds,
});

const request = (id: string, name: string, parentId: string | null, method = "GET", fileKind = "http"): TreeNode => ({
  kind: "request",
  id,
  parentId,
  name,
  path: `${id}.json`,
  fileKind,
  readOnly: "",
  method,
});

const env = (id: string, name: string): Environment => ({ id, name, color: "", fileName: `${id}.json`, readOnly: false, variables: [] });

function sample() {
  const nodes: TreeNode[] = [
    folder("users", "Users", null, ["admin", "get"]),
    folder("admin", "Admin", "users", ["del"]),
    request("del", "Delete user", "admin", "DELETE"),
    request("get", "Get user", "users"),
    request("gql", "Products", null, "POST", "graphql"),
  ];
  const byId = Object.fromEntries(nodes.map((n) => [n.id, n]));
  return buildSwitchItems(byId, ["users", "gql"], [env("env_l", "Local"), env("env_s", "Staging")]);
}

const names = (results: ReturnType<typeof searchSwitchItems>) => results.map((r) => r.item.name);

describe("buildSwitchItems", () => {
  it("lists tree items depth first with their folder path and badge, then environments", () => {
    const items = sample();
    expect(items.map((i) => [i.type, i.name, i.path, i.badge?.label ?? ""])).toEqual([
      ["folder", "Users", "", ""],
      ["folder", "Admin", "Users", ""],
      ["request", "Delete user", "Users / Admin", "DEL"],
      ["request", "Get user", "Users", "GET"],
      ["request", "Products", "", "GQL"],
      ["environment", "Local", "", ""],
      ["environment", "Staging", "", ""],
    ]);
    expect(items[2].key).toBe(nodeKey("del"));
    expect(items[5].key).toBe(environmentKey("env_l"));
  });
});

describe("searchSwitchItems", () => {
  it("lists recent items first for an empty query", () => {
    const items = sample();
    expect(names(searchSwitchItems(items, "", [nodeKey("get"), environmentKey("env_s"), "node:gone"]))).toEqual([
      "Get user",
      "Staging",
      "Users",
      "Admin",
      "Delete user",
      "Products",
      "Local",
    ]);
    expect(searchSwitchItems(items, "", [], 3)).toHaveLength(3);
  });

  it("matches names, highlights the matched characters and ranks the best first", () => {
    const results = searchSwitchItems(sample(), "user");
    // Admin matches through its path, Users / Admin.
    expect(names(results)).toEqual(["Users", "Get user", "Delete user", "Admin"]);
    expect(results[1].nameHits).toEqual([4, 5, 6, 7]);
    expect(names(searchSwitchItems(sample(), "stg"))).toEqual(["Staging"]);
    expect(searchSwitchItems(sample(), "zzz")).toEqual([]);
  });

  it("matches the folder path and name together", () => {
    const results = searchSwitchItems(sample(), "admin del");
    expect(names(results)).toEqual(["Delete user"]);
    expect(results[0].pathHits).toEqual([8, 9, 10, 11, 12]);
    expect(results[0].nameHits).toEqual([0, 1, 2]);
  });

  it("boosts recent items", () => {
    const items = sample();
    expect(names(searchSwitchItems(items, "user"))[0]).toBe("Users");
    expect(names(searchSwitchItems(items, "user", [nodeKey("del")]))[0]).toBe("Delete user");
  });

  it("filters a large workspace well within a frame", () => {
    // 10k requests in 500 folders, two levels deep, plus environments.
    const nodes: Record<string, TreeNode> = {};
    const rootIds: string[] = [];
    const verbs = ["Get", "List", "Create", "Update", "Delete", "Search"];
    const nouns = ["user", "order", "product", "invoice", "payment", "shipment", "coupon", "review"];
    const methods = ["GET", "GET", "POST", "PUT", "DELETE", "GET"];
    for (let a = 0; a < 20; a++) {
      const areaId = `area${a}`;
      const childIds: string[] = [];
      for (let f = 0; f < 25; f++) {
        const folderId = `${areaId}_f${f}`;
        const reqIds: string[] = [];
        for (let r = 0; r < 20; r++) {
          const id = `${folderId}_r${r}`;
          const v = (a + f + r) % verbs.length;
          nodes[id] = request(id, `${verbs[v]} ${nouns[(f + r) % nouns.length]} ${r}`, folderId, methods[v]);
          reqIds.push(id);
        }
        nodes[folderId] = folder(folderId, `${nouns[f % nouns.length]} flows ${f}`, areaId, reqIds);
        childIds.push(folderId);
      }
      nodes[areaId] = folder(areaId, `Area ${a}`, null, childIds);
      rootIds.push(areaId);
    }
    const envs = Array.from({ length: 20 }, (_, i) => env(`env${i}`, `Environment ${i}`));

    let t = performance.now();
    const items = buildSwitchItems(nodes, rootIds, envs);
    const buildMs = performance.now() - t;
    expect(items.length).toBe(10_000 + 500 + 20 + 20);

    const queries = ["g", "gu", "get", "get user", "crprd", "area 7 order", "delete invoice 12", "xyzzy", "environment 3"];
    // Warm up the JIT, as a key press after the first one would find it.
    for (const q of queries) searchSwitchItems(items, q);
    // Each query's cost is its fastest of several rounds: a loaded machine
    // (other test files, CI neighbours) only ever adds time, so the minimum
    // is the least noisy estimate of the work itself.
    const best = queries.map((q) => {
      let min = Infinity;
      for (let round = 0; round < 7; round++) {
        const start = performance.now();
        searchSwitchItems(items, q);
        min = Math.min(min, performance.now() - start);
      }
      return min;
    });
    best.sort((x, y) => x - y);
    const median = best[Math.floor(best.length / 2)];
    const slowest = best[best.length - 1];
    console.info(
      `quick switcher, ${items.length} items: build ${buildMs.toFixed(1)} ms, filter median ${median.toFixed(2)} ms, slowest query ${slowest.toFixed(2)} ms`,
    );
    // A frame is 16 ms; a key press should leave most of it for rendering.
    // The default suite runs under whatever load the machine has, so it only
    // catches a regression by an order of magnitude; POSTSONDE_STRICT_PERF=1
    // (on an idle machine) checks the real budget.
    const strict = import.meta.env.POSTSONDE_STRICT_PERF === "1";
    expect(median).toBeLessThan(strict ? 8 : 40);
    expect(slowest).toBeLessThan(strict ? 16 : 150);
    expect(names(searchSwitchItems(items, "delete invoice 12")).length).toBeGreaterThan(0);
  });
});

describe("touchRecent", () => {
  it("moves the key to the front and caps the list", () => {
    expect(touchRecent(["a", "b", "c"], "b")).toEqual(["b", "a", "c"]);
    expect(touchRecent(["a", "b", "c"], "d", 3)).toEqual(["d", "a", "b"]);
  });
});
