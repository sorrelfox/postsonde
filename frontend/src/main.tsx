import React from "react";
import ReactDOM from "react-dom/client";
// Initialises the Wails runtime (IPC, events, window controls).
import "@wailsio/runtime";
import App from "./App";
import { installGoSettings } from "./commands/settings";
import { setLocale, storedLocalePreference } from "./i18n";
import { followOSTheme } from "./lib/osTheme";
import "./styles/tokens.css";
import "./styles/base.css";
import "./styles/fonts.css";
import { applyCustomTheme, storedCustomTheme } from "./styles/customTheme";
import { applyTheme, storedThemePreference } from "./styles/theme";

// Settings are saved in the Go settings file (docs/settings.md).
installGoSettings();

// index.html already set data-theme before first paint from the theme cached
// in localStorage; this re-applies it and, for 'system', follows OS theme
// changes live (prefers-color-scheme, plus what Go reports: lib/osTheme.ts).
// The settings file then has the final say. A cached custom theme (UI-05)
// is applied the same way, until the theme files have been read again.
applyCustomTheme(storedCustomTheme());
applyTheme(storedThemePreference());
followOSTheme();

// The UI language (UI-10) cached from the last run, until the settings file
// has loaded; "system" (the default) is the OS language.
setLocale(storedLocalePreference());

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
