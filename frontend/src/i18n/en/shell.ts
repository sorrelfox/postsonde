// English messages of src/components/shell/ (namespace "shell"): t("shell.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // TitleBar
  "titleBar.closeProject": "Close project",
  "titleBar.theme.systemApplied": "System ({applied})",
  "titleBar.theme.custom": "custom ({file})",
  "titleBar.theme.locked": "Theme: {theme}. Set by your administrator",
  "titleBar.theme.leaveCustom": "Theme: custom ({file}). Switch to the built-in {next} theme",
  "titleBar.theme.switch": "Theme: {theme}. Switch to {next}",

  // StatusBar
  "statusBar.label": "Status bar",
  "statusBar.ready": "Ready",
  "statusBar.devVersion": "dev",

  // AutoSaveStatus
  "autosave.failed": { one: "Save failed", other: "{count} saves failed" },
  "autosave.dismiss": "Dismiss",
  "autosave.unsaved": { one: "{count} unsaved", other: "{count} unsaved" },
  "autosave.saving": "Saving…",
  "autosave.savedAt": "Saved at {time}",
  "autosave.saved": "Auto-saved",

  // PolicyStatus
  "policy.managed": "Managed",
  "policy.notApplied": "Policy not applied",
  "policy.tooltip.managed": "Settings managed by your administrator.",
  "policy.tooltip.locked": "Locked: {keys}",
  "policy.tooltip.tls": "TLS certificate verification is required for every request.",
  "policy.tooltip.file": "Policy file: {path}",

  // FocusModeButton
  "focusMode.label": "Focus mode",
  "focusMode.leave": "Leave focus mode ({shortcut})",

  // IconRail and the sidebar views
  "iconRail.label": "Sidebar views",
  "iconRail.showSidebar": "Show sidebar",
  "iconRail.hideSidebar": "Hide sidebar",
  "iconRail.withShortcut": "{label} ({shortcut})",
  "iconRail.settings": "Settings",
  "view.collections": "Collections",
  "view.collections.emptyTitle": "No project open",
  "view.collections.emptyBody": "Open a project folder to see its folders and requests here.",
  "view.history": "History",
  "view.history.emptyTitle": "No history yet",
  "view.history.emptyBody": "Requests you send show up here, newest first.",
  "view.environments": "Environments",
  "view.environments.emptyTitle": "No environments",
  "view.environments.emptyBody":
    "Environments hold variables such as baseUrl and tokens, so one request works against local, staging and production.",
  "view.servers": "Servers",
  "view.servers.emptyTitle": "No servers",
  "view.servers.emptyBody": "Run a local server on this computer and watch every request it receives.",

  // Sidebar
  "sidebar.newFolder": "New folder",
  "sidebar.importCurl": "Import cURL",
  "sidebar.importFile": "Import file",
  "sidebar.importFileTitle": "Import file…",
  "sidebar.newRequest": "New request",
  "sidebar.newRequestTitle": "New request ({shortcut})",
  "sidebar.filter.placeholder": "Filter by name or method",
  "sidebar.filter.label": "Filter collections",
  "sidebar.filter.title": "Filter by name, or by method first: “post user” ({shortcut})",
  "sidebar.filter.clear": "Clear the filter",
  "sidebar.filter.clearTitle": "Clear the filter (Esc)",

  // WorkArea
  "workArea.resize": "Resize request and response panes",
  "workArea.request": "Request",
  "workArea.response": "Response",
  "workArea.noResponse": "No response yet",
  "workArea.noResponseBody": "Send a request to see its status, headers and body here.",

  // FileDrop
  "fileDrop.noProject": "Open a project to import files into it.",
  "fileDrop.readOnly": "This project is read-only.",
  "fileDrop.title": "Drop to import",
  "fileDrop.hint": "Each file opens in the Import dialog with its detected format. Nothing is written before you import.",

  // The key hints under the quick switcher and the command palette
  "keys.escape": "Esc",
  "keys.enter": "Enter",
  "keys.move": "move",
  "keys.open": "open",
  "keys.run": "run",
  "keys.close": "close",
  "keys.commands": "commands",

  // QuickSwitcher
  "quickSwitcher.label": "Jump to request, folder or environment",
  "quickSwitcher.labelWithShortcut": "Jump to request, folder or environment ({shortcut})",
  "quickSwitcher.results": "Results",
  "quickSwitcher.folder": "Folder",
  "quickSwitcher.activeEnvironment": "Active environment",
  "quickSwitcher.environment": "Environment",
  "quickSwitcher.item": "Item",
  "quickSwitcher.option": "{name}, {kind}",
  "quickSwitcher.optionInPath": "{name}, {kind}, in {path}",
  "quickSwitcher.noMatches": "No matches",
  "quickSwitcher.count": { one: "{count} result", other: "{count} results" },
  "quickSwitcher.empty": "No matching requests, folders or environments",
  "quickSwitcher.switchFailed": "Could not switch to {name}: {error}",

  // CommandPalette
  "palette.label": "Run a command",
  "palette.title": "Commands",
  "palette.placeholder": "Type a command",
  "palette.declined": "“{command}” has nothing to do right now.",
  "palette.unavailable": "“{command}” is not available here.",
  "palette.noMatches": "No matching commands",
  "palette.count": { one: "{count} command", other: "{count} commands" },
  "palette.countUnavailable": "{available}, {count} not available here",
  "palette.reason": "{status}. {command}: {reason}",
  "palette.empty": "No command matches “{query}”.",
  "palette.keysOr": "{first} or {second}",
  "palette.optionKeys": "{label}, {keys}",
  "palette.optionNote": "{label}, {note}",

  // ShortcutHelp
  "shortcutHelp.title": "Keyboard shortcuts",
  "shortcutHelp.filter": "Filter shortcuts",
  "shortcutHelp.plannedIn": "Planned in {issue}",
  "shortcutHelp.comingSoon": "Coming soon",
  "shortcutHelp.or": "or",
  "shortcutHelp.empty": "No shortcut matches “{query}”.",

  // TokenPreview (the design-token preview, a placeholder for the request pane)
  "tokenPreview.label": "Design token preview",
  "tokenPreview.intro": "Design tokens. The request editor goes here.",
  "tokenPreview.surfaces": "Surfaces & lines",
  "tokenPreview.text": "Text & accent",
  "tokenPreview.methods": "Methods & status",
  "tokenPreview.syntax": "Syntax",
  // Encrypted secrets (SEC-03): the unlock dialog and the status bar's lock.
  "secrets.unlock.title": "Unlock stored secrets",
  "secrets.unlock.help":
    "Your stored passwords, tokens and keys are encrypted. Type your master password to use them in requests.",
  "secrets.unlock.password": "Master password",
  "secrets.unlock.confirm": "Unlock",
  "secrets.unlock.later": "Not now",
  "secrets.unlock.forgot": "Forgot password?",
  "secrets.unlock.keychain": "Unlock with {name}",
  "secrets.unlock.keychainProblem": "{name} could not unlock them: {error}",
  "secrets.unlock.migration": "A change that was interrupted is finished after unlocking.",
  "secrets.status.locked": "Locked",
  "secrets.status.unlocked": "Unlocked",
  "secrets.status.lockedLabel": "Stored secrets are locked. Unlock",
  "secrets.status.unlockedLabel": "Stored secrets are unlocked. Lock now",
} as const satisfies Messages;
