// English messages of src/components/messagelog/ (namespace "messagelog"): t("messagelog.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // Directions of a message
  "direction.sent": "Sent",
  "direction.received": "Received",
  "direction.system": "System",

  // Sizes (format.ts formatSize)
  "size.bytes": "{size} B",
  "size.kb": "{size} KB",
  "size.mb": "{size} MB",
  byteCount: { one: "{n} byte", other: "{n} bytes" },

  // A message as a copied or saved entry (format.ts messageEntryText)
  "entry.binaryPayload": "Binary payload, Base64:",

  // MessageLog
  "log.label": "Messages",
  "log.entry": "Message",
  "log.search": "Search",
  "log.searchRegex": "Search (regex)",
  "log.searchLabel": "Search messages",
  "log.regex": "Regular expression",
  "log.noMatches": "No matches",
  "log.matchOf": "{index} of {count}",
  "log.matches": { one: "{n} match", other: "{n} matches" },
  "log.previousMatch": "Previous match",
  "log.previousMatchTitle": "Previous match (Shift+Enter)",
  "log.nextMatch": "Next match",
  "log.nextMatchTitle": "Next match (Enter)",
  "log.onlyMatching": "Only matching",
  "log.onlyMatchingTitle": "Show only messages that match the search",
  "log.direction": "Direction",
  "log.kind": "Kind",
  "log.allKinds": "All kinds",
  "log.showKinds": "Show kinds",
  "log.binaryAs": "Show binary messages as",
  "log.binaryAsTitle": "How binary messages are shown",
  "log.relativeTime": "Relative time",
  "log.relativeTimeTitle": "Show time since the first message",
  "log.save": "Save…",
  "log.saveLabel": "Save the shown messages…",
  "log.clear": "Clear",
  "log.invalidRegex": "Invalid regular expression: {error}",
  "log.column.time": "Time",
  "log.column.direction": "Direction",
  "log.column.kind": "Kind",
  "log.column.size": "Size",
  "log.column.payload": "Payload",
  "log.dropped": {
    one: "{n} older message dropped (the log keeps the last {max})",
    other: "{n} older messages dropped (the log keeps the last {max})",
  },
  "log.empty": "No messages yet.",
  "log.noneMatch": "No messages match the filters.",
  "log.jumpToLatest": "Jump to latest",
  "log.jumpToLatestNew": "Jump to latest ({n} new)",
  "log.countFiltered": "{shown} of {n} messages",
  "log.count": { one: "{n} message", other: "{n} messages" },
  "log.following": "Following",
  "log.paused": "Paused",
  "log.kindBadge": "{kind} · {badge}",

  // MessageDetail
  "detail.label": "Message {number}",
  "detail.showAs": "Show payload as",
  "detail.hexdump": "Hexdump",
  "detail.format.text": "Text",
  "detail.copied.hex": "Copied hex",
  "detail.copied.base64": "Copied Base64",
  "detail.copyFailed": "Copy failed",
  "detail.copyEntry": "Copy entry",
  "detail.copyEntryTitle": "Copy the message with its details, secrets masked",
  "detail.copyHex": "Copy hex",
  "detail.copyBase64": "Copy Base64",
  "detail.copyPayload": "Copy payload",
  "detail.close": "Close message details",
  "detail.closeTitle": "Close (Esc)",
  "detail.gone": "This message is no longer in the log.",
  "detail.payload": "Payload",
  "detail.noPayload": "No payload.",
  "detail.truncated": "… showing the first {size}; Copy takes all of it",

  // MessageComposer
  "composer.label": "Message",
  "composer.format": "Message format",
  "composer.format.text": "Text",
  "composer.beautify": "Beautify",
  "composer.beautifyTitle": "Beautify JSON ({shortcut})",
  "composer.position": "Line {line}, column {column}",
} as const satisfies Messages;
