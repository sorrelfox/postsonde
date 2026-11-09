// Quick assertions (TEST-04): "Assert equals this value" on a value of the
// response, picked from its body (a JSON value, with its JSONPath), a
// header or the status code. The assertion is added to the end of the
// request's Tests sub-tab, auto-saved like any edit (one undo step), and
// the sub-tab opens on its row (commands/assertionJump.ts). See
// docs/assertions.md#quick-assertions.
import { t } from "../i18n";
import { describeAssertion, newAssertion, type AssertionDTO } from "../lib/assertions";
import { expectedOf, type JsonValueAt } from "../lib/jsonPathAt";
import type { RequestId, Stores } from "../stores";
import { jumpToAssertion } from "./assertionJump";
import { editRequest } from "./autosave";
import { showToast } from "./toasts";

/** What a quick assertion checks: target, expression, operator, expected value. */
export type QuickAssertion = Pick<AssertionDTO, "target" | "expr" | "op" | "expected">;

/**
 * Adds `a` (enabled) to the end of request `id`'s assertions, opens its
 * Tests sub-tab at the new row and says so in a toast. Does nothing for a
 * request without a draft. Returns the new row's index, or null.
 */
export function addQuickAssertion(stores: Stores, id: RequestId, a: QuickAssertion): number | null {
  const draft = stores.drafts.getState().byId[id];
  if (draft === undefined) return null;
  const current = draft.request.assertions;
  const list = Array.isArray(current) ? (current as AssertionDTO[]) : [];
  const added = newAssertion({ ...a });
  editRequest(stores, id, { assertions: [...list, added] }, { history: "separate" });
  const index = list.length;
  jumpToAssertion(stores, id, index);
  showToast({ message: t("commands.quickAssert.added", { assertion: describeAssertion(added) }) });
  return index;
}

/** The quick assertions on a JSON value of the body: equals it, exists, is of its type. */
export function jsonAssertions(v: JsonValueAt): { equals: QuickAssertion; exists: QuickAssertion; type: QuickAssertion } {
  return {
    equals: { target: "jsonpath", expr: v.path, op: "equals", expected: expectedOf(v) },
    exists: { target: "jsonpath", expr: v.path, op: "exists", expected: "" },
    type: { target: "jsonpath", expr: v.path, op: "is_type", expected: v.kind },
  };
}

/** The quick assertions on a response header: equals its value, exists. */
export function headerAssertions(name: string, value: string): { equals: QuickAssertion; exists: QuickAssertion } {
  return {
    equals: { target: "header", expr: name, op: "equals", expected: value },
    exists: { target: "header", expr: name, op: "exists", expected: "" },
  };
}

/** The quick assertion on the status code. */
export function statusAssertion(code: number): QuickAssertion {
  return { target: "status", expr: "", op: "equals", expected: String(code) };
}
