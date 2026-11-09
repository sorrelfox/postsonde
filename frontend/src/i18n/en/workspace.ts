// English messages of src/components/workspace/ (namespace "workspace"): t("workspace.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // WelcomeScreen (no project open)
  "welcome.tagline":
    "A project is a folder on disk. Open one, or start a new one in an empty folder.",
  "welcome.open": "Open folder…",
  "welcome.openHint": "A folder with a postsonde.json file",
  "welcome.create": "Create project…",
  "welcome.createHint": "Choose or make a folder for it",
  "welcome.form.label": "Create project",
  "welcome.form.submit": "Create",
  "welcome.createHere": "Create a project in this folder",
  "welcome.openInstead": "Open it instead",
  "welcome.recent": "Recent projects",
  "welcome.recent.notFound": "Not found",
  "welcome.recent.remove": "Remove from recent projects",
  "welcome.recent.removeNamed": "Remove {name} from recent projects",
  "welcome.recent.openInNewWindow": "Open in new window",
  "welcome.recent.openInNewWindowNamed": "Open {name} in a new window",
  "welcome.note":
    "Project files hold no secrets and can be committed. Secrets, history and tokens stay in PostSonde's local data on this computer, never in the project folder.",

  // Shared by the welcome screen and the new project dialog
  "form.projectName": "Project name",

  // WorkspaceMenu (title bar project switcher)
  "menu.button": "Project: {name}. Switch project",
  "menu.label": "Projects",
  "menu.recentNotFound": "{name} (not found)",
  "menu.open": "Open folder…",
  "menu.new": "New project…",
  "menu.revealFinder": "Reveal in Finder",
  "menu.revealFileManager": "Show in file manager",
  "menu.manageRecent": "Manage recent projects…",
  "menu.newWindow": "Open in new window",
  "menu.close": "Close project",
  "menu.createHere": "Create project here",

  // NewProjectDialog
  "newProject.title": "New project",
  "newProject.create": "Create",
} as const satisfies Messages;
