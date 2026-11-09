// Calls into the Go ThemesService (internal/app/services/themes): the custom
// theme files in the data directory's themes folder (UI-05), validated in Go,
// and opening that folder. See docs/themes.md.
import {
  ThemesService,
  type ThemeDTO,
  type ThemeProblemDTO,
  type ThemesDTO,
  type ThemeVarDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/themes";
import type { CancellablePromise } from "./calls";

export type { ThemeDTO, ThemeProblemDTO, ThemesDTO, ThemeVarDTO };

/** Every theme file in the themes folder, read again now, with its problems. */
export function listThemes(): CancellablePromise<ThemesDTO> {
  return ThemesService.List();
}

/** Creates the themes folder if needed and opens it in the file manager. */
export function openThemesFolder(): CancellablePromise<void> {
  return ThemesService.OpenFolder();
}
