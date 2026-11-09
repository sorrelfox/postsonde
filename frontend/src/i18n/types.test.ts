// Compile-time checks of t(): `npm run typecheck` fails when a line marked
// as an expected error below stops being one, that is when t() no longer
// catches an unknown key or a missing, extra or mistyped parameter.
import { expect, it } from "vitest";
import type { MessageKey } from "./catalog";
import { t } from "./index";
import type { PlaceholdersOf } from "./messages";

function typeChecks(dynamicKey: MessageKey & ("shell.iconRail.showSidebar" | "shell.iconRail.hideSidebar")) {
  t("shell.statusBar.ready");
  t("shell.titleBar.theme.switch", { theme: "Dark", next: "System" });
  t("shell.autosave.failed", { count: 2 });
  t(dynamicKey);
  // @ts-expect-error: no such key
  t("shell.statusBar.nope");
  // @ts-expect-error: no such namespace
  t("nope.statusBar.ready");
  // @ts-expect-error: a placeholder is missing
  t("shell.titleBar.theme.switch", { theme: "Dark" });
  // @ts-expect-error: the parameters are missing
  t("shell.titleBar.theme.switch");
  // @ts-expect-error: a parameter the message does not have
  t("shell.titleBar.theme.switch", { theme: "Dark", next: "System", extra: 1 });
  // @ts-expect-error: a plural message needs its count
  t("shell.autosave.failed");
  // @ts-expect-error: the count is a number
  t("shell.autosave.failed", { count: "2" });
  // @ts-expect-error: a message without placeholders takes no parameters
  t("shell.statusBar.ready", { x: 1 });

  const names: PlaceholdersOf<"Use {{name}} or {a} and {b_2}, not { c } or {d-e}"> = "a";
  const other: PlaceholdersOf<"Use {{name}} or {a} and {b_2}, not { c } or {d-e}"> = "b_2";
  // @ts-expect-error: {{name}} is a template variable, not a placeholder
  const notOne: PlaceholdersOf<"Use {{name}}"> = "name";
  return [names, other, notOne];
}

it("checks message keys and parameters at compile time (npm run typecheck)", () => {
  expect(typeof typeChecks).toBe("function");
});
