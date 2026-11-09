// English messages of src/components/common/ (namespace "common"):
// t("common.<id>"). Other areas may use these (OK, Cancel, the theme
// names) but add their own messages to their own namespace. See
// docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // Words used across the app
  ok: "OK",
  cancel: "Cancel",
  close: "Close",
  dismiss: "Dismiss",
  "theme.system": "System",
  "theme.light": "Light",
  "theme.dark": "Dark",

  // Dialog
  "dialog.closeTitle": "Close (Esc)",

  // Toasts
  "toast.dismiss": "Dismiss notification",
  "toast.region": "Notifications",

  // SplitPane
  "splitPane.resize": "Resize panes",

  // KeyValueTable
  "keyValue.key": "Key",
  "keyValue.value": "Value",
  "keyValue.description": "Description",
  "keyValue.enabled": "Enabled",
  "keyValue.delete": "Delete",
  "keyValue.cell": "{column}, row {row}",
  "keyValue.deleteRow": "Delete row {row}",
  "keyValue.addRow": "+ Add row",
  "keyValue.placeholder.key": "key",
  "keyValue.placeholder.value": "value",
  "keyValue.placeholder.description": "description",
  "keyValue.suggestions": "Suggestions",
  "keyValue.bulk": "Bulk edit",
  "keyValue.bulk.label": "Bulk edit {table}",
  "keyValue.bulk.toTable": "Back to the table",
  "keyValue.bulk.toText": "Edit the rows as key: value text",
  "keyValue.bulk.editor": "{table}, bulk edit",
  "keyValue.bulk.placeholder": "key: value\n// disabled: row",

  // PromptHost ({{$prompt}} values, DYN-07)
  "prompt.titleRun": "Values for this run",
  "prompt.titleOne": "Value for this send",
  "prompt.titleMany": "Values for this send",
  "prompt.start": "Start",
  "prompt.send": "Send",
  "prompt.helpRun": "Asked once: every send of the run uses these values. They are not saved.",
  "prompt.helpSend": "Used for this send only. They are not saved.",

  // LogActions
  "log.entry": "Entry",
  "log.copyEntry": "Copy entry",
  "log.copyEntryJson": "Copy entry as JSON",
  "log.saveThe": "Save the {what}…",
  "log.saveText": "Save as text…",
  "log.saveJson": "Save as JSON…",

  // FileAccessNotice (PRV-10)
  "fileAccess.outside":
    "“{name}” is outside the directories allowed for file access, so PostSonde will refuse to read it.",
  "fileAccess.outsideMany": {
    one: "{count} chosen file is outside the directories allowed for file access, so PostSonde will refuse to read it.",
    other:
      "{count} chosen files are outside the directories allowed for file access, so PostSonde will refuse to read them.",
  },
  "fileAccess.allow": "Allow “{folder}”",
  "fileAccess.allowTitle": "Add {path} to the allowed directories (Settings, Privacy, Local files)",
  "fileAccess.allowed": "“{folder}” is allowed now.",
  "fileAccess.locked": "The allowed directories are managed by your administrator, so this folder cannot be added here.",
  "fileAccess.dismiss": "Dismiss",

  // Markdown (notes)
  "markdown.done": "Done",
  "markdown.notDone": "Not done",
  "markdown.imageNotLoaded": "Image not loaded: {url}",
  "markdown.notOpened": "Not opened: {url}",
} as const satisfies Messages;
