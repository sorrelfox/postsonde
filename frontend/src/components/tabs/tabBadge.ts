import { t } from "../../i18n";
import type { TreeNode } from "../../stores";
import { nodeBadge, type Badge } from "../tree/badges";

// i18n-ignore: a badge token, like the GET and GQL badges of the tree
const FOLDER_BADGE: Badge = { label: "DIR", tone: "dir" };

/** The badge of a tab: the tree item's method or kind badge, `DIR` for a folder (mockup). */
export function tabBadge(node: TreeNode): Badge {
  return nodeBadge(node) ?? FOLDER_BADGE;
}

/** What kind of item a tab shows, in words (in the language in use). */
export function nodeKindLabel(node: TreeNode): string {
  switch (node.kind) {
    case "folder":
      return t("tabs.kind.folder");
    case "request":
      if (node.fileKind === "graphql") return t("tabs.kind.graphql");
      if (node.fileKind === "tcp") return t("tabs.kind.tcp");
      if (node.fileKind === "udp") return t("tabs.kind.udp");
      if (node.fileKind === "websocket") return t("tabs.kind.websocket");
      if (node.fileKind === "grpc") return t("tabs.kind.grpc");
      if (node.fileKind === "sse") return t("tabs.kind.sse");
      if (node.fileKind === "socketio") return t("tabs.kind.socketio");
      return t("tabs.kind.http");
    case "unknown":
      return node.fileKind === "" ? t("tabs.kind.unknown") : t("tabs.kind.unknownNamed", { kind: node.fileKind });
    case "invalid":
      return t("tabs.kind.invalid");
  }
}
