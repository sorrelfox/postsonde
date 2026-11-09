// Undo and redo keys of editable CodeMirror editors (NAV-07): CodeMirror's
// historyKeymap plus redo on ⇧⌘Z and ⌘Y (Ctrl+Shift+Z and Ctrl+Y) on every
// platform, the same keys as the request editor's draft history
// (registry request.undo / request.redo, commands/undoRedo.ts).

import { historyKeymap, redo } from "@codemirror/commands";
import type { KeyBinding } from "@codemirror/view";

export const historyKeys: readonly KeyBinding[] = [
  { key: "Mod-Shift-z", run: redo, preventDefault: true },
  { key: "Mod-y", run: redo, preventDefault: true },
  ...historyKeymap,
];
