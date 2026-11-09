// English messages of src/components/tree/ (namespace "tree"): t("tree.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // Shared by the tree's dialogs
  project: "Project",
  readOnlyProject: "This project is read-only.",
  name: "Name",
  folder: "Folder",
  folderName: "Folder name",
  requestName: "Request name",
  "severity.info": "info",
  "severity.warning": "warning",
  "severity.error": "error",

  // CollectionTree
  label: "Collection",
  labelFiltered: "{label}, filtered",
  "empty.errorTitle": "Could not load the requests",
  "empty.noProjectTitle": "No project open",
  "empty.noProjectBody": "Open a project folder to see its folders and requests here.",
  "empty.noMatchesTitle": "No matches",
  "empty.noMatchesBody": "No folder or request matches “{query}” by name or method.",
  "empty.clearFilter": "Clear the filter",
  "empty.noRequestsTitle": "No requests yet",
  "empty.noRequestsBody": "Folders and requests of this project show up here. Create one with the buttons above.",
  matchStatus: { one: "{count} match for “{query}”", other: "{count} matches for “{query}”" },
  "row.folder": { one: "{name}, {count} item", other: "{name}, {count} items" },
  "row.folderListening": {
    one: "{name}, {count} item, server listening {server}",
    other: "{name}, {count} items, server listening {server}",
  },
  "row.serverListening": "Server listening: {server}",
  "reorder.filtered": "Clear the filter to reorder items.",
  "reorder.sortedTop": "The top level is sorted by name. Choose “{choice}” in the menu to reorder by hand.",
  "reorder.sortedFolder": "“{folder}” is sorted by name. Choose “{choice}” in the menu to reorder by hand.",

  // The tree's context menu
  "menu.label": "Collection item",
  "menu.newRequest": "New request",
  "menu.newFolder": "New folder",
  "menu.importCurl": "Import cURL…",
  "menu.importFile": "Import file…",
  "menu.exportHistoryHar": "Export history as HAR…",
  "menu.exportProjectPostman": "Export project as Postman collection…",
  "menu.rename": "Rename…",
  "menu.duplicate": "Duplicate",
  "menu.copyCurl": "Copy as cURL",
  "menu.exportHar": "Export as HAR…",
  "menu.exportPostman": "Export as Postman collection…",
  "menu.exportZip": "Export as zip…",
  "menu.runFolder": "Run folder…",
  "menu.moveTo": "Move to folder…",
  "menu.moveUp": "Move up",
  "menu.moveDown": "Move down",
  "menu.delete": "Delete",
  "menu.sortByNameTop": "Sort the top level by name",
  "menu.sortByNameFolder": "Sort “{folder}” by name",
  "menu.sortManualTop": "Keep the top level in manual order",
  "menu.sortManualFolder": "Keep “{folder}” in manual order",

  // badges: why an item is read-only
  "readOnly.newerVersion": "Saved by a newer PostSonde: read-only",
  "readOnly.unknownKind": "A kind this version of PostSonde cannot open: read-only",
  "readOnly.invalid": "This file could not be read: read-only",
  "readOnly.symlink": "A symbolic link: read-only",
  "readOnly.other": "Read-only",

  // FolderSelect
  "folderSelect.topLevel": "top level",
  "folderSelect.topLevelOption": "{name} (top level)",

  // newItem: request kinds and name checks
  "kind.http": "An HTTP request with params, headers, a body and auth.",
  "kind.graphql": "A GraphQL query or mutation with variables, sent over HTTP.",
  "kind.websocket": "A WebSocket connection to send and receive messages.",
  "kind.tcp": "A raw TCP connection to send and receive text or bytes.",
  "kind.udp": "UDP datagrams to send and receive, directly or through a SOCKS5 proxy.",
  "kind.grpc": "A gRPC call, with its services from .proto files or server reflection.",
  "kind.sse": "A Server-Sent Events stream, with its events shown live as they arrive.",
  "kind.socketio": "A Socket.IO v4 connection to a namespace, to emit and receive events.",
  "nameCheck.tooLong": "Use at most {max} characters.",
  "nameCheck.control": "The name cannot contain line breaks or control characters.",
  "nameCheck.taken": "An item with this name is already in this folder.",

  // move: why an item cannot move
  "moveCheck.gone": "The item is no longer in the project.",
  "moveCheck.folderReadOnly": "Its folder is read-only, so nothing can be moved out of it.",
  "moveCheck.notFolder": "Items can only go into folders.",
  "moveCheck.intoItself": "A folder cannot be moved into itself or into one of its folders.",
  "moveCheck.targetReadOnly": "That folder is read-only.",
  "moveCheck.symlink": "A symbolic link can only be reordered in its folder.",

  // NewItemDialog
  "newItem.saveTitle": "Save to…",
  "newItem.folderTitle": "New folder",
  "newItem.requestTitle": "New request",
  "newItem.saveConfirm": "Save request",
  "newItem.folderConfirm": "Create folder",
  "newItem.requestConfirm": "Create request",
  "newItem.type": "Type",

  // TreeActionDialogs: Rename
  "rename.folderTitle": "Rename folder",
  "rename.requestTitle": "Rename request",
  "rename.confirm": "Rename",
  "rename.folderKeeps": "The folder keeps its directory name",
  "rename.fileKeeps": "The file keeps its name",
  "rename.folderRenamed": "The directory is renamed too",
  "rename.fileRenamed": "The file is renamed too",

  // TreeActionDialogs: Move to folder
  "move.folderTitle": "Move folder",
  "move.requestTitle": "Move request",
  "move.confirm": "Move",
  "move.to": "Move “{name}” to",
  "move.unchanged": "It is in this folder now; choose another one",
  "move.folderMoves": "The directory moves, with everything in it",
  "move.fileMoves": "The file moves",

  // TreeActionDialogs: Delete folder
  "delete.title": "Delete folder “{name}”?",
  "delete.confirm": "Delete folder",
  "delete.folders": { one: "{count} folder", other: "{count} folders" },
  "delete.requests": { one: "{count} request", other: "{count} requests" },
  "delete.bodyFolders":
    "The folder and the {folders} in it move to this project's trash. You can undo right after deleting, or restore them from the trash later.",
  "delete.bodyRequests":
    "The folder and the {requests} in it move to this project's trash. You can undo right after deleting, or restore them from the trash later.",
  "delete.bodyBoth":
    "The folder and the {folders} and {requests} in it move to this project's trash. You can undo right after deleting, or restore them from the trash later.",
  "delete.bodyEmpty":
    "The folder moves to this project's trash. You can undo right after deleting, or restore it from the trash later.",
  "delete.list": "What is deleted",
  "delete.more": { one: "and {count} more", other: "and {count} more" },

  // useTreeActions: toasts
  "actions.deleted": "Deleted “{name}”",
  "actions.undo": "Undo",
  "actions.restoreFailed": "Could not restore “{name}”. {reason}",
  "actions.deleteFailed": "Could not delete “{name}”. {reason}",
  "actions.duplicateFailed": "Could not duplicate “{name}”. {reason}",
  "actions.moveFailed": "Could not move “{name}”. {reason}",

  // ImportCurlDialog
  "curl.title": "Import cURL",
  "curl.confirm": "Import request",
  "curl.command": "cURL command",
  "curl.placeholder": "Paste a curl command, e.g. from “Copy as cURL” in the browser’s network panel",
  "curl.help": "Copied from browser DevTools (bash, cmd or PowerShell) or API docs. Nothing is run, and no file is read.",
  "curl.headers": { one: "{count} header", other: "{count} headers" },
  "curl.rawBody": "{type} body",
  "curl.formBody": { one: "form body, {count} field", other: "form body, {count} fields" },
  "curl.multipartBody": { one: "multipart body, {count} part", other: "multipart body, {count} parts" },
  "curl.fileBody": "file body",
  "curl.basicAuth": "Basic auth",
  "curl.bearerToken": "bearer token",
  "curl.errors": { one: "{count} error", other: "{count} errors" },
  "curl.warnings": { one: "{count} warning", other: "{count} warnings" },
  "curl.errorsAndWarnings": "{errors}, {warnings}",
  "curl.notes": "Notes",
  "curl.problems": "Problems in the command",
  "curl.selectText": "Select the text in the command",

  // ImportDialog
  "import.title": "Import",
  "import.reportTitle": "Import report",
  "import.titlePosition": "{title} ({index} of {total})",
  "import.format": "Format",
  "import.detect": "Detect from the file",
  "import.chooseFile": "Choose file…",
  "import.intro":
    "Choose a file to import into the project. You see what it will create before anything is written, and a report afterwards.",
  "import.formats": "PostSonde reads {formats}. Everything is read locally; nothing is fetched.",
  "import.formatsSeparator": ", ",
  "import.formatsLastSeparator": " and ",
  "import.detected": "{format} (detected)",
  "import.chooseAnother": "Choose another file…",
  "import.into": "Import into",
  "import.newFolder": "Create a folder for the import",
  "import.environments": { one: "Import the environment", other: "Import {count} environments" },
  "import.intoParentNote":
    "The items go straight into the folder. Settings of the file’s collection (variables, headers, auth) are left out.",
  "import.sourceChanged": "The file changed since it was previewed. This is the new preview; import again to use it.",
  "import.confirm": "Import",
  "import.aboutFile": "About the file",
  "import.creates": "What the import creates",
  "import.untitled": "Untitled",
  "import.skipBadge": "SKIP",
  "import.failed": "failed",
  "import.skipped": "skipped",
  "import.hideNotes": "Hide the notes",
  "import.showNotes": "Show the notes",
  "import.more": { one: "And {count} more.", other: "And {count} more." },
  "import.done": "Done",
  "import.count.folders": "Folders",
  "import.count.requests": "Requests",
  "import.count.environments": "Environments",
  "import.count.skipped": "Skipped",
  "import.count.lossy": "With warnings",
  "import.count.failed": "Failed",
  "import.nothingChanged": "Everything was imported as it was.",
  "import.problems": "Skipped, failed and changed items",
  "import.showInfo": "Show notes too",

  // ExportReport
  "export.report": "Export report",
  "export.notExported": "(not exported)",
} as const satisfies Messages;
