import { describe, expect, it } from "vitest";
import type { NodeId, TreeNode } from "../../stores";
import { buildFilterIndex, filterTree } from "./filter";

const folder = (id: string, parentId: string | null, name: string, childIds: string[]): TreeNode => ({
  kind: "folder",
  id,
  parentId,
  name,
  path: id,
  fileKind: "",
  readOnly: "",
  childIds,
});
const request = (id: string, parentId: string | null, name: string, method = "GET", fileKind = "http"): TreeNode => ({
  kind: "request",
  id,
  parentId,
  name,
  path: `${id}.json`,
  fileKind,
  readOnly: "",
  method,
});

// Users/ { List users, Create user (POST), Admin/ { Delete user (DELETE) } }, Orders/ { Get order, Update settings (PUT) }, Schema (GraphQL)
const LIST: TreeNode[] = [
  folder("users", null, "Users", ["list", "create", "admin"]),
  request("list", "users", "List users"),
  request("create", "users", "Create user", "POST"),
  folder("admin", "users", "Admin", ["delete"]),
  request("delete", "admin", "Delete user", "DELETE"),
  folder("orders", null, "Orders", ["get", "settings"]),
  request("get", "orders", "Get order"),
  request("settings", "orders", "Update settings", "PUT"),
  request("schema", null, "Schema", "POST", "graphql"),
];
const NODES: Record<NodeId, TreeNode> = Object.fromEntries(LIST.map((n) => [n.id, n]));
const ROOT = ["users", "orders", "schema"];
const INDEX = buildFilterIndex(NODES, ROOT);

const run = (query: string) => filterTree(NODES, INDEX, query);
const hitIds = (query: string) => [...(run(query)?.hits.keys() ?? [])];

describe("filterTree", () => {
  it("is off for a blank query", () => {
    expect(run("")).toBeNull();
    expect(run("   ")).toBeNull();
  });

  it("matches names fuzzily, jumping only to word starts, with the matched letters", () => {
    expect(hitIds("user")).toEqual(["users", "list", "create", "delete"]);
    expect(run("order")?.hits.get("get")?.positions).toEqual([4, 5, 6, 7, 8]);
    expect(run("lu")?.hits.get("list")?.positions).toEqual([0, 5]);
    expect(hitIds("upset")).toEqual(["settings"]);
    // Letters in order but not at word starts: no match.
    expect(hitIds("usr")).toEqual([]);
  });

  it("keeps the folders above a match visible and opens them", () => {
    const r = run("delete user");
    expect(r?.hits.has("delete")).toBe(true);
    expect([...(r?.visible ?? [])].sort()).toEqual(["admin", "delete", "users"]);
    expect(r?.expandedIds).toEqual({ users: true, admin: true });
    expect(r?.itemCount).toBe(1);
  });

  it("shows everything inside a folder that matches by name, without opening it", () => {
    const r = run("admin");
    expect([...(r?.hits.keys() ?? [])]).toEqual(["admin"]);
    expect(r?.visible.has("delete")).toBe(true);
    expect(r?.expandedIds).toEqual({ users: true });
    expect(r?.itemCount).toBe(0);
  });

  it("filters by method or kind when the query starts with one", () => {
    expect(hitIds("post")).toEqual(["create", "schema"]);
    expect(hitIds("POST user")).toEqual(["create"]);
    expect(run("del user")?.hits.get("delete")).toEqual({ positions: [7, 8, 9, 10], method: true });
    expect(hitIds("delete")).toEqual(["delete"]);
    expect(hitIds("del")).toEqual(["delete"]);
    expect(hitIds("gql")).toEqual(["schema"]);
    expect(hitIds("graphql")).toEqual(["schema"]);
    expect(run("put")?.hits.get("settings")).toEqual({ positions: [], method: true });
  });

  it("matches a lone method word in other names only as a whole run of letters", () => {
    // "Get order" contains "get"; "Update settings" does not match "put" letter by letter.
    expect(hitIds("get")).toEqual(["list", "get"]);
    expect(run("get")?.hits.get("get")).toEqual({ positions: [], method: true });
    expect(hitIds("put")).toEqual(["settings"]);
  });

  it("filters a tree of 5,000 requests in a few milliseconds", () => {
    const nodes: Record<NodeId, TreeNode> = {};
    const rootIds: NodeId[] = [];
    for (let f = 0; f < 100; f++) {
      const childIds: NodeId[] = [];
      for (let r = 0; r < 50; r++) {
        const id = `r${f}-${r}`;
        childIds.push(id);
        nodes[id] = request(id, `f${f}`, `Request ${r} of group ${f} ${r % 3 === 0 ? "users" : "orders"}`, r % 2 === 0 ? "GET" : "POST");
      }
      nodes[`f${f}`] = folder(`f${f}`, null, `Group ${f}`, childIds);
      rootIds.push(`f${f}`);
    }
    const index = buildFilterIndex(nodes, rootIds);
    const queries = ["u", "us", "use", "user", "users", "post users", "get ord", "group 7", "zzz"];
    const times: number[] = [];
    for (let round = 0; round < 5; round++) {
      for (const q of queries) {
        const start = performance.now();
        filterTree(nodes, index, q);
        times.push(performance.now() - start);
      }
    }
    times.sort((a, b) => a - b);
    expect(times[Math.floor(times.length / 2)]).toBeLessThan(16);
    // POST (odd r) and "users" (r a multiple of 3): 8 in each of the 100 groups.
    expect(filterTree(nodes, index, "post users")?.itemCount).toBe(800);
  });
});
