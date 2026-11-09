// English messages of src/components/cookies/ (namespace "cookies"): t("cookies.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // The status bar button
  "button.label": "Cookies",
  "button.title": "Manage the cookies of this project",

  // The cookie manager (CK-02)
  "manager.title": "Cookies",
  "manager.jar": "Cookie jar",
  "manager.jarCaption": "Jar",
  "manager.perEnvironment": "Separate cookies per environment",
  "manager.perEnvironment.title": "Keep a separate cookie jar for each environment of this project",
  "manager.filter.placeholder": "Filter by domain, name or value",
  "manager.filter.label": "Filter cookies",
  "manager.count": { one: "{count} cookie", other: "{count} cookies" },
  "manager.refresh": "Refresh",
  "manager.clearAll": "Clear all",
  "manager.add": "Add cookie",
  "manager.loading": "Loading cookies…",
  "manager.empty.title": "No cookies",
  "manager.empty.body": "Cookies that responses set are stored here and sent with the next matching request. Add one to send it yourself.",
  "manager.noMatch": "No cookie matches the filter.",

  // A domain group
  "group.add": "Add cookie to {domain}",
  "group.add.title": "Add cookie",
  "group.clear": "Clear cookies of {domain}",
  "group.clear.title": "Clear domain",
  "col.name": "Name",
  "col.value": "Value",
  "col.path": "Path",
  "col.expires": "Expires",
  "col.actions": "Actions",
  "row.noName": "(no name)",
  "row.subdomains": "+ subdomains",
  "row.subdomains.title": "Also sent to subdomains of {domain}",
  "row.session": "Session",
  "row.yes": "Yes",
  "row.binary": "This cookie has bytes the editor cannot show; it can only be deleted.",
  "row.edit": "Edit",
  "row.editNamed": "Edit {name}",
  "row.editUnnamed": "Edit cookie",
  "row.delete": "Delete",
  "row.deleteNamed": "Delete {name}",
  "row.deleteUnnamed": "Delete cookie",

  // Confirmations
  "delete.title": "Delete cookie?",
  "delete.message": "\"{name}\" for {domain} will no longer be sent.",
  "delete.confirm": "Delete",
  "clearDomain.title": "Clear cookies of {domain}?",
  "clearDomain.message": { one: "{count} cookie will be deleted.", other: "{count} cookies will be deleted." },
  "clearDomain.confirm": "Clear",
  "clearAll.title": "Clear all cookies?",
  "clearAll.message": "Every cookie of this jar will be deleted.",
  "clearAll.messageNamed": "Every cookie of the {jar} jar will be deleted.",
  "clearAll.confirm": "Clear all",

  // The add and edit form
  "form.addTitle": "Add cookie",
  "form.editTitle": "Edit cookie",
  "form.add": "Add cookie",
  "form.save": "Save",
  "form.back": "Back",
  "form.name": "Name",
  "form.path": "Path",
  "form.value": "Value",
  "form.domain": "Domain",
  "form.subdomains": "Also send to subdomains",
  "form.expires": "Expires",
  "form.session": "Session cookie (no expiry)",
  "form.sameSite.notSet": "Not set",
  "form.secure.help": "only over https and to localhost",
  "form.httpOnly.help": "hidden from scripts",
} as const satisfies Messages;
