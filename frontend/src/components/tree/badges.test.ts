import { describe, expect, it } from "vitest";
import type { TreeNode } from "../../stores";
import { nodeBadge, readOnlyHint } from "./badges";

const base = { id: "x", parentId: null, name: "x", path: "x.json", readOnly: "" };
const req = (method: string, fileKind = "http"): TreeNode => ({ ...base, kind: "request", fileKind, method });
const unknown = (fileKind: string): TreeNode => ({ ...base, kind: "unknown", fileKind, readOnly: "unknown_kind" });

describe("nodeBadge", () => {
  it("labels HTTP methods with the mockup's tones", () => {
    expect(nodeBadge(req("GET"))).toEqual({ label: "GET", tone: "get" });
    expect(nodeBadge(req("post"))).toEqual({ label: "POST", tone: "post" });
    expect(nodeBadge(req("PUT"))).toEqual({ label: "PUT", tone: "put" });
    expect(nodeBadge(req("PATCH"))).toEqual({ label: "PATCH", tone: "patch" });
    expect(nodeBadge(req("DELETE"))).toEqual({ label: "DEL", tone: "del" });
    expect(nodeBadge(req("QUERY"))).toEqual({ label: "QUERY", tone: "get" });
    expect(nodeBadge(req("HEAD"))).toEqual({ label: "HEAD", tone: "dir" });
    expect(nodeBadge(req("OPTIONS"))).toEqual({ label: "OPT", tone: "dir" });
    expect(nodeBadge(req("PURGE"))).toEqual({ label: "PURGE", tone: "dir" });
    expect(nodeBadge(req("PROPFIND"))).toEqual({ label: "PROPF", tone: "dir" });
    expect(nodeBadge(req(""))).toEqual({ label: "HTTP", tone: "dir" });
  });

  it("labels GraphQL and the other protocols by kind", () => {
    expect(nodeBadge(req("POST", "graphql"))).toEqual({ label: "GQL", tone: "gql" });
    expect(nodeBadge(req("", "tcp"))).toEqual({ label: "TCP", tone: "dir" });
    expect(nodeBadge(req("", "websocket"))).toEqual({ label: "WS", tone: "ws" });
    expect(nodeBadge(req("", "grpc"))).toEqual({ label: "GRPC", tone: "dir" });
    expect(nodeBadge(unknown("grpc"))).toEqual({ label: "GRPC", tone: "dir" });
    expect(nodeBadge(unknown("websocket"))).toEqual({ label: "WS", tone: "ws" });
    expect(nodeBadge(unknown("socketio"))).toEqual({ label: "SIO", tone: "ws" });
    expect(nodeBadge(unknown("sse"))).toEqual({ label: "SSE", tone: "ws" });
    expect(nodeBadge(unknown("tcp"))).toEqual({ label: "TCP", tone: "dir" });
    expect(nodeBadge(unknown("mqtt-v5"))).toEqual({ label: "MQTTV", tone: "dir" });
  });

  it("marks invalid files and gives folders no badge", () => {
    expect(nodeBadge({ ...base, kind: "invalid", fileKind: "" })).toEqual({ label: "?", tone: "warn" });
    expect(nodeBadge({ ...base, kind: "invalid", fileKind: "graphql" })).toEqual({ label: "GQL", tone: "warn" });
    expect(nodeBadge({ ...base, kind: "folder", fileKind: "", childIds: [] })).toBeNull();
  });
});

describe("readOnlyHint", () => {
  it("prefers the load problem, then the read-only reason", () => {
    expect(readOnlyHint(req("GET"), undefined)).toBeUndefined();
    expect(readOnlyHint(unknown("grpc"), undefined)).toMatch(/cannot open/);
    expect(readOnlyHint(unknown("grpc"), "Bad file")).toBe("Bad file");
    expect(readOnlyHint({ ...base, kind: "folder", fileKind: "", childIds: [], readOnly: "weird" }, undefined)).toBe("Read-only");
  });
});
