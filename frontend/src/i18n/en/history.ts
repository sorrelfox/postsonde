// English messages of src/components/history/ (namespace "history"): t("history.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // Header
  "filters.show": "Show filters",
  "filters.hide": "Hide filters",
  "clear.menu": "Clear history",
  "clear.menuLabel": "Clear history",
  "clear.request": "Clear history of “{name}”…",
  "clear.all": "Clear all history…",

  // Search and filters
  "search.label": "Search history",
  "search.placeholder": "Search URL, method, status, name",
  "search.clear": "Clear search",
  "filters.label": "History filters",
  "filters.method": "Method",
  "filters.status": "Status",
  "filters.kind": "Kind",
  "filters.environment": "Environment",
  "filters.from": "From",
  "filters.to": "To",
  "filters.any": "Any",
  "filters.status.error": "Failed or cancelled",
  "filters.kind.http": "HTTP",
  "filters.kind.graphql": "GraphQL",
  "filters.kind.replay": "Replayed requests",
  "filters.deep": "Also search headers and bodies",
  "filters.deepHint": "Searches what the history kept, with secrets masked. Slower on a large history.",
  "filters.thisRequest": "Only “{name}”",
  "filters.thisRequestNone": "Only the open request",
  "filters.reset": "Reset filters",

  // Count and states
  "count.all": { "=0": "No entries", one: "{count} entry", other: "{count} entries" },
  "count.matching": { "=0": "No matching entries of {all}", one: "{count} matching entry of {all}", other: "{count} matching entries of {all}" },
  "loading": "Loading history…",
  "empty.title": "No history yet",
  "empty.body": "Requests you send show up here, newest first.",
  "noMatch.title": "No entries match",
  "noMatch.body": "Try other words, or reset the filters.",
  "error.title": "The history could not be read",
  "retry": "Try again",
  "loadMore": "Load more",
  "loadingMore": "Loading…",

  // List
  "list.label": "History entries",
  "group.today": "Today",
  "group.yesterday": "Yesterday",
  "row.failed": "Failed",
  "row.cancelled": "Cancelled",
  "row.label": "{method} {name}, {status}, {time}",
  "row.replay": "Replayed request",
  "row.environment": "Environment: {name}",
  "row.hint": "Enter restores it; Shift+F10 for more.",

  // Context menu
  "menu.label": "History entry",
  "menu.restore": "Restore into “{name}”",
  "menu.restoreUnavailable": "Restore into the request",
  "menu.restoreUnavailableTitle": "The request is no longer in the project, or is read-only.",
  "menu.scratch": "Open as scratch request",
  "menu.delete": "Delete entry",
  "menu.clearRequest": "Clear history of “{name}”…",

  // Restore
  "restore.request": "Restored “{name}” from the history.",
  "restore.scratch": "Opened the history entry as scratch request “{name}”.",
  "restore.scratchName": "{name} (history)",
  "restore.scratchNameUnnamed": "From history",
  "restore.asSent": "The history had it only as sent: variables are resolved and the auth is not included.",
  "restore.hiddenRequest": "Not restored, the request keeps its own: {fields}.",
  "restore.hiddenScratch": "Not restored in full (secret or cut short in the history): {fields}.",
  "restore.gone": "The request is no longer in the project, or is read-only.",
  "restore.undo": "Undo",
  "section.url": "URL",
  "section.params": "query parameter “{name}”",
  "section.pathParams": "path parameter “{name}”",
  "section.headers": "header “{name}”",
  "section.body": "body",
  "section.bodyFields": "form field “{name}”",
  "section.bodyParts": "form part “{name}”",
  "section.query": "query",
  "section.variables": "variables",

  // Delete and clear
  "delete.done": "Deleted the history entry.",
  "clear.allTitle": "Clear all history?",
  "clear.allMessage": { one: "The {count} entry of this project’s history is deleted. This cannot be undone.", other: "All {count} entries of this project’s history are deleted. This cannot be undone." },
  "clear.allConfirm": "Clear history",
  "clear.allDone": "Cleared the history.",
  "clear.requestTitle": "Clear the history of “{name}”?",
  "clear.requestMessage": "Every history entry of this request is deleted, with its earlier responses. This cannot be undone.",
  "clear.requestDone": { "=0": "“{name}” had no history.", one: "Deleted {count} entry of “{name}”.", other: "Deleted {count} entries of “{name}”." },
  "failed": "The history could not be changed: {message}",
} as const satisfies Messages;
