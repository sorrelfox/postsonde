import { describe, expect, it } from "vitest";
import { collectionReducer, initialCollectionState, type CollectionState, type TreeNode } from "../../stores";
import {
  defaultTarget,
  folderOptions,
  MAX_NAME_LENGTH,
  previewPath,
  renamePreview,
  REQUEST_KINDS,
  slug,
  TOP_LEVEL,
  validateName,
  validateRename,
} from "./newItem";

const folder = (id: string, parentId: string | null, path: string, childIds: string[], readOnly = ""): TreeNode => ({
  kind: "folder",
  id,
  parentId,
  name: id[0].toUpperCase() + id.slice(1),
  path,
  fileKind: "folder",
  readOnly,
  childIds,
});
const request = (id: string, parentId: string | null, name: string, path: string): TreeNode => ({
  kind: "request",
  id,
  parentId,
  name,
  path,
  fileKind: "http",
  readOnly: "",
  method: "GET",
});

// orders/ { Get order, refunds/ { } }, locked/ (read-only), Health
function state(): CollectionState {
  return collectionReducer(initialCollectionState, {
    type: "collection/load",
    workspaceId: "ws_1",
    rootIds: ["orders", "locked", "health"],
    nodes: [
      folder("orders", null, "orders", ["get", "refunds"]),
      request("get", "orders", "Get order", "orders/get-order.json"),
      folder("refunds", "orders", "orders/refunds", []),
      folder("locked", null, "locked", [], "newer_version"),
      request("health", null, "Health", "health.json"),
    ],
  });
}

describe("slug (a port of collection.Slug)", () => {
  it.each([
    ["Create user", "create-user"],
    ["  GET /users/{id}  ", "get-users-id"],
    ["User's list", "users-list"],
    ["User’s list", "users-list"],
    ["snake_case_name", "snake-case-name"],
    ["v2.1 Health", "v2-1-health"],
    ["--- Leading and trailing ---", "leading-and-trailing"],
    ["ÀÉÎÕÜ çñ", "aeiou-cn"],
    ["Zoë Straße Øresund Łódź", "zoe-strasse-oresund-lodz"],
    ["Ærø œuvre", "aero-oeuvre"],
    ["Получить пользователя", "poluchit-polzovatelya"],
    ["Щука і їжак, ёж", "shchuka-i-izhak-ezh"],
    ["日本語 API", "api"],
    ["", "untitled"],
    ["???", "untitled"],
    ["_folder", "folder"],
    ["emoji 🚀 launch", "emoji-launch"],
    ["word ".repeat(20), `${"word-".repeat(12)}word`],
    ["a".repeat(100), "a".repeat(64)],
  ])("%j → %j", (name, want) => {
    expect(slug(name)).toBe(want);
  });

  it("gives NFC and NFD spellings the same slug", () => {
    expect(slug("Café".normalize("NFD"))).toBe(slug("Café".normalize("NFC")));
    expect(slug("йогурт".normalize("NFD"))).toBe("iogurt");
  });
});

describe("validateName", () => {
  it("needs a name, without an error message when it is empty", () => {
    expect(validateName(state(), TOP_LEVEL, "  ")).toBe("");
    expect(validateName(state(), TOP_LEVEL, "Payments")).toBeNull();
  });

  it("refuses long names, control characters and a sibling's name", () => {
    expect(validateName(state(), TOP_LEVEL, "x".repeat(MAX_NAME_LENGTH))).toBeNull();
    expect(validateName(state(), TOP_LEVEL, "x".repeat(MAX_NAME_LENGTH + 1))).toMatch(/at most/);
    expect(validateName(state(), TOP_LEVEL, "a\nb")).toMatch(/control characters/);
    expect(validateName(state(), "orders", " get ORDER ")).toMatch(/already in this folder/);
    expect(validateName(state(), TOP_LEVEL, "Get order")).toBeNull();
    expect(validateName(state(), TOP_LEVEL, "orders")).toMatch(/already/);
  });
});

describe("folderOptions", () => {
  it("lists the top level, then every folder depth first, read-only ones disabled", () => {
    expect(folderOptions(state(), "shop-api")).toEqual([
      { id: TOP_LEVEL, name: "shop-api", depth: 0, disabled: false },
      { id: "orders", name: "Orders", depth: 1, disabled: false },
      { id: "refunds", name: "Refunds", depth: 2, disabled: false },
      { id: "locked", name: "Locked", depth: 1, disabled: true },
    ]);
  });
});

describe("defaultTarget", () => {
  it("is the folder, the folder of an item, or the top level", () => {
    const s = state();
    expect(defaultTarget(s, null)).toBe(TOP_LEVEL);
    expect(defaultTarget(s, "refunds")).toBe("refunds");
    expect(defaultTarget(s, "get")).toBe("orders");
    expect(defaultTarget(s, "health")).toBe(TOP_LEVEL);
    expect(defaultTarget(s, "locked")).toBe(TOP_LEVEL);
    expect(defaultTarget(s, "gone")).toBe(TOP_LEVEL);
  });
});

describe("previewPath", () => {
  it("shows the file Go writes, with a collision suffix", () => {
    const s = state();
    expect(previewPath(s, "orders", "Refund order", false)).toBe("orders/refund-order.json");
    expect(previewPath(s, "orders", "Get order", false)).toBe("orders/get-order-2.json");
    expect(previewPath(s, TOP_LEVEL, "Payments", true)).toBe("payments/_folder.json");
    expect(previewPath(s, "orders", "Refunds", true)).toBe("orders/refunds-2/_folder.json");
    expect(previewPath(s, "refunds", "", false)).toBe("orders/refunds/untitled.json");
    expect(previewPath(s, TOP_LEVEL, "Environments", true)).toBe("environments-2/_folder.json");
    expect(previewPath(s, TOP_LEVEL, "CON", false)).toBe("con-2.json");
  });
});

describe("REQUEST_KINDS", () => {
  it("offers every kind CreateRequest accepts, HTTP first", () => {
    expect(REQUEST_KINDS.map((k) => k.kind)).toEqual(["http", "graphql", "websocket", "tcp", "udp", "grpc", "sse", "socketio"]);
  });
});

describe("validateRename", () => {
  it("allows the item's own name in another case, and refuses a sibling's", () => {
    const s = state();
    expect(validateRename(s, "get", "Get order")).toBe("");
    expect(validateRename(s, "get", "  ")).toBe("");
    expect(validateRename(s, "get", "get ORDER")).toBeNull();
    expect(validateRename(s, "get", "refunds")).toBe("An item with this name is already in this folder.");
    expect(validateRename(s, "health", "orders")).toBe("An item with this name is already in this folder.");
    expect(validateRename(s, "health", "Status")).toBeNull();
    expect(validateRename(s, "missing", "X")).toBe("");
  });
});

describe("renamePreview (a port of renamedEntry)", () => {
  it("shows where the file or directory moves, or null when it stays", () => {
    const s = state();
    expect(renamePreview(s, "get", "Cancel an order")).toEqual({ from: "orders/get-order.json", to: "orders/cancel-an-order.json" });
    expect(renamePreview(s, "get", "GET order!")).toBeNull();
    expect(renamePreview(s, "refunds", "Returns")).toEqual({ from: "orders/refunds/", to: "orders/returns/" });
    expect(renamePreview(s, "health", "Postsonde")).toEqual({ from: "health.json", to: "postsonde-2.json" });
    expect(renamePreview(s, "get", "")).toBeNull();
  });

  it("adds a suffix while the name is taken, and keeps an existing one", () => {
    const s = collectionReducer(initialCollectionState, {
      type: "collection/load",
      workspaceId: "ws_1",
      rootIds: ["a", "b"],
      nodes: [request("a", null, "Orders", "orders.json"), request("b", null, "Orders 2", "orders-2.json")],
    });
    expect(renamePreview(s, "b", "orders")).toBeNull();
    expect(renamePreview(s, "a", "Orders 2")).toEqual({ from: "orders.json", to: "orders-2-2.json" });
  });
});
