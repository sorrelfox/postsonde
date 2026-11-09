// The kind and method badge of a tree item (COL-02): the short mono
// label before its name ("GET", "GQL", "WS", …) and its colour tone. The
// tones are the mockup's --m-* tokens; see docs/frontend.md (Collection tree).

import { t, type MessageKey } from "../../i18n";
import type { TreeNode } from "../../stores";

/** A --m-* colour token (`dir` is the neutral one), or `warn` for a file that did not load. */
export type BadgeTone = "get" | "post" | "put" | "patch" | "del" | "gql" | "ws" | "dir" | "warn";

export interface Badge {
  /** At most 5 characters, so it fits the 38px badge column (DELETE is shown as DEL, which reads better). */
  readonly label: string;
  readonly tone: BadgeTone;
}

const METHODS: Readonly<Record<string, Badge>> = {
  GET: { label: "GET", tone: "get" },
  POST: { label: "POST", tone: "post" },
  PUT: { label: "PUT", tone: "put" },
  PATCH: { label: "PATCH", tone: "patch" },
  DELETE: { label: "DEL", tone: "del" },
  // QUERY (HTTP-03) is safe like GET, so it shares its tone.
  QUERY: { label: "QUERY", tone: "get" },
  HEAD: { label: "HEAD", tone: "dir" },
  OPTIONS: { label: "OPT", tone: "dir" },
};

/** File kinds of the protocols in COL-02; the ones this version cannot open yet arrive as "unknown" items. */
const KINDS: Readonly<Record<string, Badge>> = {
  graphql: { label: "GQL", tone: "gql" },
  grpc: { label: "GRPC", tone: "dir" },
  websocket: { label: "WS", tone: "ws" },
  ws: { label: "WS", tone: "ws" },
  socketio: { label: "SIO", tone: "ws" },
  "socket.io": { label: "SIO", tone: "ws" },
  sse: { label: "SSE", tone: "ws" },
  tcp: { label: "TCP", tone: "dir" },
  udp: { label: "UDP", tone: "dir" },
};

function shortLabel(text: string): string {
  const upper = text.toUpperCase().replace(/[^A-Z0-9]/g, "");
  return upper === "" ? "?" : upper.slice(0, 5);
}

/** The badge of an HTTP method ("HTTP" for none, a custom one in the neutral tone). */
export function methodBadge(method: string): Badge {
  const upper = method.toUpperCase();
  return METHODS[upper] ?? { label: upper === "" ? "HTTP" : shortLabel(upper), tone: "dir" };
}

/** The badge of a non-folder node; null for folders. */
export function nodeBadge(node: TreeNode): Badge | null {
  switch (node.kind) {
    case "folder":
      return null;
    case "request": {
      // HTTP requests show their method; other request kinds (graphql, tcp) their kind.
      if (node.fileKind === "http" || node.fileKind === "") return methodBadge(node.method);
      return KINDS[node.fileKind.toLowerCase()] ?? { label: shortLabel(node.fileKind), tone: "dir" };
    }
    case "unknown":
      return KINDS[node.fileKind.toLowerCase()] ?? { label: shortLabel(node.fileKind), tone: "dir" };
    case "invalid": {
      const known = KINDS[node.fileKind.toLowerCase()];
      return { label: known?.label ?? (node.fileKind === "http" ? "HTTP" : node.fileKind === "" ? "?" : shortLabel(node.fileKind)), tone: "warn" };
    }
  }
}

const READ_ONLY_REASONS: Readonly<Record<string, MessageKey>> = {
  newer_version: "tree.readOnly.newerVersion",
  unknown_kind: "tree.readOnly.unknownKind",
  invalid: "tree.readOnly.invalid",
  symlink: "tree.readOnly.symlink",
};

/** Tooltip text for a read-only node: the load problem when there is one, else the reason. */
export function readOnlyHint(node: TreeNode, problem: string | undefined): string | undefined {
  if (problem !== undefined && problem !== "") return problem;
  if (node.readOnly === "") return undefined;
  return t(READ_ONLY_REASONS[node.readOnly] ?? "tree.readOnly.other");
}

const SEVERITIES: Readonly<Record<string, MessageKey>> = {
  info: "tree.severity.info",
  warning: "tree.severity.warning",
  error: "tree.severity.error",
};

/** The label of a diagnostic's severity (import and export reports, the Import cURL dialog); an unknown one as it is. */
export function severityLabel(severity: string): string {
  const key = SEVERITIES[severity];
  return key === undefined ? severity : t(key);
}
