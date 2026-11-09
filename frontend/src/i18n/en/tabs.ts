// English messages of src/components/tabs/ (namespace "tabs"): t("tabs.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // TabBar
  "tabBar.label": "Open requests",
  "tabBar.newScratch": "New scratch request",
  "tabBar.menu.label": "Tab",
  "tabBar.menu.close": "Close",
  "tabBar.menu.closeOthers": "Close others",
  "tabBar.menu.closeRight": "Close to the right",
  "tabBar.menu.closeAll": "Close all",
  "tabBar.menu.keepOpen": "Keep open",
  "tabBar.menu.saveTo": "Save to…",
  "tabBar.menu.deleteScratch": "Delete scratch request",
  "tabBar.menu.pin": "Pin",
  "tabBar.menu.unpin": "Unpin",
  "tabBar.menu.reopen": "Reopen closed tab",
  // A tab's accessible name: its badge and name, then its states one by one.
  "tabBar.tab.label": "{badge} {name}",
  "tabBar.tab.withState": "{label}, {state}",
  "tabBar.tab.state.scratch": "scratch request",
  "tabBar.tab.state.deleted": "file deleted",
  "tabBar.tab.state.pinned": "pinned",
  "tabBar.tab.state.preview": "preview",
  "tabBar.tab.scratchTitle": "Scratch request, not in the project: right-click to save it to a folder",
  "tabBar.tab.scratchTag": "scratch",
  "tabBar.tab.deletedTag": "deleted",
  "tabBar.tab.close": "Close {name}",

  // ExternalChangeBanner
  "banner.conflict.request":
    "This request's file changed outside PostSonde while you had unsaved edits. Your edits are not saved until you choose.",
  "banner.conflict.folder":
    "This folder's settings file changed outside PostSonde while you had unsaved edits. Your edits are not saved until you choose.",
  "banner.conflict.requestWindow":
    "This request was changed in another PostSonde window while you had unsaved edits. Your edits are not saved until you choose.",
  "banner.conflict.folderWindow":
    "This folder's settings were changed in another PostSonde window while you had unsaved edits. Your edits are not saved until you choose.",
  "banner.showChanges": "Show changes",
  "banner.takeTheirs": "Take theirs",
  "banner.keepMine": "Keep mine",
  "banner.deleted": "This request's file was deleted outside PostSonde. The tab keeps its content until you close it.",
  "banner.close": "Close",
  "banner.restore": "Restore file",
  "banner.diff.title": "Your version and the file on disk",
  "banner.diff.titleWindow": "Your version and the other window's",
  "banner.diff.onlyMine": "− only in yours",
  "banner.diff.onlyTheirs": "+ only on disk",
  "banner.diff.onlyTheirsWindow": "+ only in the other window's",
  "banner.diff.label": "Differences",
  "banner.diff.same": "Both versions have the same content.",

  // NoTabState
  "noTab.title": "No request open",
  "noTab.body":
    "Open a request from the sidebar, or try something quickly in a scratch request. It is kept on this computer, outside the project, until you save it to a folder.",
  "noTab.newScratch": "New scratch request",
  "noTab.newScratchOther": "New scratch request of another type",
  "noTab.otherTypes": "Other request types",
  "noTab.list": "Scratch requests",
  "noTab.open": "Open {name}",
  "noTab.saveTo": "Save to…",
  "noTab.saveToLabel": "Save {name} to a folder",
  "noTab.delete": "Delete",
  "noTab.deleteLabel": "Delete {name}",

  // TabPanel
  "panel.kind": "Kind",
  "panel.path": "Path",
  "panel.notAvailable": "The editor for this item is not available yet.",

  // tabBadge: what kind of item a tab shows
  "kind.folder": "Folder",
  "kind.graphql": "GraphQL request",
  "kind.tcp": "TCP request",
  "kind.udp": "UDP request",
  "kind.websocket": "WebSocket request",
  "kind.grpc": "gRPC request",
  "kind.sse": "SSE request",
  "kind.socketio": "Socket.IO request",
  "kind.http": "HTTP request",
  "kind.unknown": "Item of an unknown kind",
  "kind.unknownNamed": "{kind} item",
  "kind.invalid": "File that could not be read",

  // useScratchActions
  "scratch.createFailed": "Could not create the scratch request. {error}",
  "scratch.deleteTitle": "Delete scratch request “{name}”?",
  "scratch.deleteBody": "It is not in the project, so it cannot be restored.",
  "scratch.delete": "Delete",
  "scratch.deleteFailed": "Could not delete “{name}”. {error}",
  "scratch.kindRequest": "{kind} request",
  "scratch.menu": "New scratch request",
} as const satisfies Messages;
