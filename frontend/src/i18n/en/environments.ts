// English messages of src/components/environments/ (namespace "environments"): t("environments.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // EnvironmentsPanel: the sidebar view
  "panel.new": "New environment",
  "panel.newName": "New environment name",
  "panel.create": "Create",
  "panel.loadFailed": "The environments could not be loaded.",
  "panel.retry": "Retry",
  "panel.list": "Environments",
  "panel.empty.title": "No environments",
  "panel.empty.body":
    "Environments hold variables such as baseUrl and tokens, so one request works against local, staging and production.",
  "panel.row.title": "{name} ({file}). Double-click to make it active.",
  "panel.active": "Active",
  "panel.problems": {
    one: "1 file in environments/ could not be loaded:",
    other: "{count} files in environments/ could not be loaded:",
  },

  // The inline name form (new, rename)
  "nameForm.placeholder": "Name, e.g. Staging",

  // EnvironmentDetails: the selected environment
  "details.label": "Environment {name}",
  "details.nameLabel": "Environment name",
  "details.rename": "Rename",
  "details.delete": "Delete",
  "details.title": "Variables · {name}",
  "details.activeTitle": "Active. Click to use no environment.",
  "details.deactivate": "{name} is active. Deactivate",
  "details.activate": "Activate",
  "details.readOnly": "Saved by a newer version of PostSonde: shown read-only.",
  "details.variablesLabel": "Variables of {name}",
  "details.secretNote":
    "Secret values (the lock) and local values (the screen) are kept in PostSonde's local data on this computer, never in the project folder.",
  "delete.title": "Delete environment “{name}”?",
  "delete.body": "Its file {file} is removed from the project.",
  "delete.confirm": "Delete environment",
  "delete.done": "Deleted environment “{name}”",

  // Export (EXP-05)
  "export.postsonde": "Export…",
  "export.postsondeTitle": "Export as a PostSonde environment file, without secret or local values",
  "export.postman": "Export for Postman…",
  "export.postmanTitle": "Export as a Postman environment, without secret or local values",
  "export.done": {
    "=0": "Exported “{name}” to {path}",
    one: "Exported “{name}” to {path}, without the value of its secret variable",
    other: "Exported “{name}” to {path}, without the values of its {count} secret variables",
  },

  // Global variables (ENV-02)
  "globals.rowTitle":
    "The base environment: variables every environment inherits and every request of the project can use (postsonde.json). An environment, collection or folder variable of the same name overrides them.",
  "globals.label": "Global variables",
  "globals.titleName": "Global",
  "globals.loading": "Loading…",
  "globals.readOnly":
    "postsonde.json cannot be changed by this version of PostSonde: shown read-only.",
  "globals.note":
    "The base environment: every environment inherits these, and a variable of the same name in an environment overrides them. They are saved in postsonde.json; secret values (the lock) and local values (the screen) stay on this computer.",

  // Base URL (COL-08): the baseUrl variable, where a relative request URL goes. {name}: "baseUrl"; {url}: its value.
  "baseUrl.own": "Base URL: {url}. Requests whose URL starts with / (such as /users) are sent here: the {name} variable.",
  "baseUrl.inherited":
    "Base URL: {url}, the {name} of the global variables. Add a variable {name} here to send requests whose URL starts with / to another server.",
  "baseUrl.none":
    "No base URL. Add a variable {name} with the server's address (http or https, with the host) to send requests whose URL starts with / (such as /users) to this environment's server.",
  "baseUrl.noneGlobal":
    "No base URL. A variable {name} here, the server's address, is where requests whose URL starts with / are sent, in every environment that does not set its own.",

  // Precedence (ENV-06)
  "precedence.label": "Precedence, lowest first",
  "precedence.listLabel": "Variable precedence, lowest first",
  "precedence.note":
    "A later scope wins when the same name is defined in several. Within a scope, a variable's local value wins over its shared value.",
  "precedence.scope.global": "Global",
  "precedence.scope.environment": "Environment",
  "precedence.scope.collection": "Collection",
  "precedence.scope.folder": "Folder",
  "precedence.scope.runtime": "Runtime",
  "precedence.title.global":
    "Global variables: postsonde.json, for every request",
  "precedence.title.environment": "The active environment",
  "precedence.title.collection":
    "Collection variables: the top-level folder's Variables tab",
  "precedence.title.folder":
    "Folder variables: each folder's Variables tab, the nearest folder winning",
  "precedence.title.runtime":
    "Runtime variables: set while you work, in the Runtime section or from a response. A folder run's own variables win over them.",

  // Runtime variables (ENV-15)
  "runtime.rowTitle":
    "Values set while you work. They win over every environment, collection, folder and global variable of the same name.",
  "runtime.label": "Runtime variables",
  "runtime.titleName": "Runtime",
  "runtime.loading": "Loading…",
  "runtime.empty": "No runtime variables. Set one below; values taken from responses will appear here too.",
  "runtime.listLabel": "Runtime variables",
  "runtime.clearAll": "Clear all",
  "runtime.clear.title": "Clear all runtime variables?",
  "runtime.clear.body":
    "Every runtime variable of this project is removed, those kept after closing the project too.",
  "runtime.clear.confirm": "Clear all",
  "runtime.edit": "Edit {name}",
  "runtime.editTitle": "Edit {name}",
  "runtime.emptyValue": "empty",
  "runtime.kept": "Kept",
  "runtime.keptTitle": "Kept in PostSonde's local data: back when the project opens again",
  "runtime.session": "Session",
  "runtime.sessionTitle": "For this session: gone when the project is closed",
  "runtime.showValue": "Show the value of {name}",
  "runtime.hideValue": "Hide the value of {name}",
  "runtime.secret": "Secret: {name}",
  "runtime.secretOn": "Secret: masked in the editor, the timeline, the history and exports. Click to make it a plain value.",
  "runtime.secretOff": "Make secret: mask the value in the editor, the timeline, the history and exports",
  "runtime.persist": "Keep {name} after closing the project",
  "runtime.persistOn": "Kept after closing the project. Click to keep it for this session only.",
  "runtime.persistOff": "Keep after closing the project, in PostSonde's local data (never in the project)",
  "runtime.delete": "Delete {name}",
  "runtime.deleteTitle": "Delete this runtime variable",
  "runtime.form.addTitle": "Set a runtime variable",
  "runtime.form.editTitle": "Edit {name}",
  "runtime.form.name": "Name",
  "runtime.form.value": "Value",
  "runtime.form.secret": "Secret: mask the value everywhere",
  "runtime.form.persist": "Keep after closing the project",
  "runtime.form.set": "Set",
  "runtime.form.save": "Save",
  "runtime.note":
    "Runtime variables win over every other scope; a folder run's own variables win over them during the run. They are kept in PostSonde's local data on this computer, never in the project folder: for this session only, or kept (the pin) until you delete them.",

  // EnvironmentColorPicker (ENV-08)
  "color.label": "Colour",
  "color.none": "No colour",
  "color.custom": "Custom colour",
  "color.customValue": "Custom colour {color}",
  "color.preset.red": "Red",
  "color.preset.orange": "Orange",
  "color.preset.yellow": "Yellow",
  "color.preset.green": "Green",
  "color.preset.teal": "Teal",
  "color.preset.blue": "Blue",
  "color.preset.purple": "Purple",
  "color.preset.pink": "Pink",
  "color.preset.grey": "Grey",

  // VariablesEditor
  "variables.placeholder.name": "name",
  "variables.showValue": "Show value",
  "variables.hideValue": "Hide value",
  "variables.showValueRow": "Show value, row {row}",
  "variables.hideValueRow": "Hide value, row {row}",
  "variables.secretRow": "Secret, row {row}",
  "variables.secretOn":
    "Secret: the value is kept on this computer, never in the project. Click to make it a plain value.",
  "variables.secretOff":
    "Make secret: keep the value on this computer, never in the project",
  "variables.secretLocal": "Remove the local value to make this variable secret",
  // Local values (SEC-02)
  "variables.localRow": "Local value, row {row}",
  "variables.localOn":
    "Has a local value: this computer sends it instead of the shared value. Click to remove it.",
  "variables.localOff":
    "Add a local value: sent from this computer instead of the shared value, never saved in the project",
  "variables.local.title": "Local values · this computer only",
  "variables.local.unnamed": "(no name)",
  "variables.local.placeholder": "local value",
  "variables.local.remove": "Remove the local value of {name}",
  "variables.local.removeTitle": "Remove the local value: send the shared value again",
  "variables.local.shared": "Shared value: {value}",
  "variables.local.sharedEmpty": "Shared value: empty",
  "variables.local.note":
    "A local value replaces the shared value on this computer only. It is kept in PostSonde's local data, never in the project: teammates keep the shared value. It is not a secret: use the lock for passwords and tokens.",
  // Table and raw JSON editors (ENV-07). {label}: the table's name, "Variables of Staging".
  "variables.mode.label": "Show {label} as",
  "variables.mode.table": "Table",
  "variables.mode.json": "JSON",
  "variables.mode.tableBlocked": "Fix the JSON to show the table again",
  "variables.raw.label": "{label} as JSON",
  // {mask}: what a secret value is shown as.
  "variables.raw.note":
    "Each change that is valid JSON is saved. Secret values show as {mask}: leave it to keep the value, or type a new one. Members: name, value, localValue, secret, enabled, description.",
  // The base environment (ENV-05): the global variables an environment inherits.
  "variables.base.title": "Inherited from the base environment (Global variables)",
  "variables.base.empty": "empty",
  "variables.base.overridden": "Overridden here",
  "variables.base.override": "Override",
  "variables.base.overrideName": "Override {name} in this environment",
  "variables.base.overrideTitle": "Add a variable of this name to this environment, starting from the base value",
  "variables.base.note":
    "Every environment inherits the global variables. A variable of the same name in this environment overrides the base value; remove or disable it to inherit again.",

  // EnvironmentSwitcher (title bar)
  "switcher.label": "Environment",
  "switcher.none": "No environment",
  "switcher.title": "Environment: {name} ({shortcut})",
} as const satisfies Messages;
