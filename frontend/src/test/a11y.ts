// A lightweight accessibility audit of rendered DOM for tests (UI-11;
// docs/frontend.md#accessibility). No axe-core: a handful of structural
// rules that catch the mistakes this app can make, checked on jsdom.
//
//   expectAccessible();                 // audits document.body
//   expect(auditA11y(container)).toEqual([]);
//
// It checks what jsdom can know without layout or styles: accessible names,
// tab order, id references, ARIA roles and their required context. Colour
// contrast is checked on the tokens (styles/contrast.test.ts), focus
// indicators on the CSS (styles/focusStyles.test.ts).
import { expect } from "vitest";

export interface A11yViolation {
  rule: string;
  /** A short description of the element: tag, role, id, class and name. */
  element: string;
  message: string;
}

/** Roles that must have an accessible name (ARIA 1.2 "name required", plus the widgets). */
const NAME_REQUIRED = new Set([
  "button",
  "link",
  "checkbox",
  "radio",
  "switch",
  "tab",
  "menuitem",
  "menuitemcheckbox",
  "menuitemradio",
  "option",
  "treeitem",
  "textbox",
  "searchbox",
  "combobox",
  "listbox",
  "slider",
  "spinbutton",
  "tree",
  "treegrid",
  "grid",
  "dialog",
  "alertdialog",
  "tabpanel",
  "progressbar",
  "meter",
  "radiogroup",
  "region",
  "form",
  "img",
]);

/** Roles whose name can come from their content. */
const NAME_FROM_CONTENT = new Set([
  "button",
  "link",
  "checkbox",
  "radio",
  "switch",
  "tab",
  "menuitem",
  "menuitemcheckbox",
  "menuitemradio",
  "option",
  "treeitem",
  "cell",
  "gridcell",
  "columnheader",
  "rowheader",
  "row",
  "heading",
  "tooltip",
]);

/** Every role an element may carry (WAI-ARIA 1.2, plus the abstract-free document structure roles). */
const VALID_ROLES = new Set([
  "alert", "alertdialog", "application", "article", "banner", "blockquote", "button", "caption", "cell",
  "checkbox", "code", "columnheader", "combobox", "complementary", "contentinfo", "definition", "deletion",
  "dialog", "directory", "document", "emphasis", "feed", "figure", "form", "generic", "grid", "gridcell",
  "group", "heading", "img", "insertion", "link", "list", "listbox", "listitem", "log", "main", "marquee",
  "math", "menu", "menubar", "menuitem", "menuitemcheckbox", "menuitemradio", "meter", "navigation", "none",
  "note", "option", "paragraph", "presentation", "progressbar", "radio", "radiogroup", "region", "row",
  "rowgroup", "rowheader", "scrollbar", "search", "searchbox", "separator", "slider", "spinbutton", "status",
  "strong", "subscript", "superscript", "switch", "tab", "table", "tablist", "tabpanel", "term", "textbox", "time",
  "timer", "toolbar", "tooltip", "tree", "treegrid", "treeitem",
]);

/** A role's required context: one of these roles must be an ancestor (or own it with aria-owns). */
const REQUIRED_CONTEXT: Readonly<Record<string, readonly string[]>> = {
  tab: ["tablist"],
  treeitem: ["tree", "group", "treegrid"],
  option: ["listbox", "group"],
  menuitem: ["menu", "menubar", "group"],
  menuitemcheckbox: ["menu", "menubar", "group"],
  menuitemradio: ["menu", "menubar", "group"],
  row: ["grid", "table", "treegrid", "rowgroup"],
  rowgroup: ["grid", "table", "treegrid"],
  cell: ["row"],
  gridcell: ["row"],
  columnheader: ["row"],
  rowheader: ["row"],
  listitem: ["list", "directory"],
};

/** A role's required owned elements: when it has any descendant with a role, one must be of these. */
const REQUIRED_CHILDREN: Readonly<Record<string, readonly string[]>> = {
  tablist: ["tab"],
  tree: ["treeitem", "group"],
  listbox: ["option", "group"],
  menu: ["menuitem", "menuitemcheckbox", "menuitemradio", "group", "separator"],
  grid: ["row", "rowgroup"],
  table: ["row", "rowgroup"],
  treegrid: ["row", "rowgroup"],
  row: ["cell", "gridcell", "columnheader", "rowheader"],
  radiogroup: ["radio"],
};

/** Attributes holding id references (space-separated lists). */
const ID_REF_ATTRIBUTES = [
  "aria-labelledby",
  "aria-describedby",
  "aria-controls",
  "aria-owns",
  "aria-activedescendant",
  "aria-errormessage",
  "aria-details",
  "aria-flowto",
];

const NATIVELY_FOCUSABLE = "a[href], button, input:not([type='hidden']), select, textarea, iframe, summary, [contenteditable]:not([contenteditable='false'])";

function isDisabled(el: Element): boolean {
  return (el as HTMLButtonElement).disabled === true;
}

/** Whether `el` is removed from the accessibility tree: hidden, aria-hidden or inert, itself or an ancestor. */
export function isHiddenFromAT(el: Element): boolean {
  for (let cur: Element | null = el; cur !== null; cur = cur.parentElement) {
    if (cur.getAttribute("aria-hidden") === "true") return true;
    if (cur.hasAttribute("hidden") || cur.hasAttribute("inert")) return true;
    if (cur instanceof HTMLElement && cur.style.display === "none") return true;
    if (cur.tagName === "DIALOG" && !(cur as HTMLDialogElement).open) return true;
    // A <datalist>'s options are suggestions of its input, not content.
    if (cur.tagName === "DATALIST") return true;
  }
  return false;
}

/** Whether `el` takes focus with Tab (ignores layout, which jsdom lacks). */
export function isTabbable(el: Element): boolean {
  if (!(el instanceof HTMLElement)) return false;
  if (isDisabled(el)) return false;
  const tabindex = el.getAttribute("tabindex");
  if (tabindex !== null && Number(tabindex) < 0) return false;
  if (tabindex === null && !el.matches(NATIVELY_FOCUSABLE)) return false;
  if (el instanceof HTMLInputElement && el.type === "radio" && !el.checked && el.name !== "") {
    // Only the checked radio of a group is in the Tab order (or the first when none is).
    const group = el.form ?? el.ownerDocument;
    const checked = group.querySelector(`input[type='radio'][name='${CSS.escape(el.name)}']:checked`);
    if (checked !== null) return false;
  }
  for (let cur: Element | null = el; cur !== null; cur = cur.parentElement) {
    if (cur.hasAttribute("hidden") || cur.hasAttribute("inert")) return false;
    if (cur instanceof HTMLElement && (cur.style.display === "none" || cur.style.visibility === "hidden")) return false;
  }
  return true;
}

/** The element's role: its first valid explicit role, else the implicit one of its tag (a subset). */
export function roleOf(el: Element): string | null {
  const explicit = el.getAttribute("role");
  if (explicit !== null) {
    for (const token of explicit.trim().split(/\s+/)) {
      if (VALID_ROLES.has(token)) return token;
    }
  }
  const tag = el.tagName.toLowerCase();
  switch (tag) {
    case "button":
      return "button";
    case "a":
      return el.hasAttribute("href") ? "link" : null;
    case "textarea":
      return "textbox";
    case "select":
      return (el as HTMLSelectElement).multiple || (el as HTMLSelectElement).size > 1 ? "listbox" : "combobox";
    case "option":
      return "option";
    case "input": {
      const type = (el as HTMLInputElement).type;
      if (type === "hidden") return null;
      if (type === "checkbox") return "checkbox";
      if (type === "radio") return "radio";
      if (type === "range") return "slider";
      if (type === "number") return "spinbutton";
      if (type === "button" || type === "submit" || type === "reset" || type === "image") return "button";
      if (type === "search") return el.hasAttribute("list") ? "combobox" : "searchbox";
      if (type === "file" || type === "color" || type === "date" || type === "time" || type === "datetime-local") return "textbox";
      return el.hasAttribute("list") ? "combobox" : "textbox";
    }
    case "ul":
    case "ol":
      return "list";
    case "li":
      return el.parentElement?.matches("ul, ol, menu") === true ? "listitem" : null;
    case "table":
      return "table";
    case "tr":
      return "row";
    case "td":
      return "cell";
    case "th":
      return "columnheader";
    case "thead":
    case "tbody":
    case "tfoot":
      return "rowgroup";
    case "dialog":
      return "dialog";
    case "progress":
      return "progressbar";
    case "meter":
      return "meter";
    case "nav":
      return "navigation";
    case "main":
      return "main";
    case "header":
      return el.closest("main, article, aside, nav, section") === null ? "banner" : null;
    case "footer":
      return el.closest("main, article, aside, nav, section") === null ? "contentinfo" : null;
    case "aside":
      return "complementary";
    case "section":
      return el.hasAttribute("aria-label") || el.hasAttribute("aria-labelledby") ? "region" : null;
    case "form":
      return el.hasAttribute("aria-label") || el.hasAttribute("aria-labelledby") ? "form" : null;
    case "h1":
    case "h2":
    case "h3":
    case "h4":
    case "h5":
    case "h6":
      return "heading";
    case "img":
      return el.getAttribute("alt") === "" ? "presentation" : "img";
    default:
      return null;
  }
}

function textOf(node: Node, root: boolean): string {
  if (node.nodeType === Node.TEXT_NODE) return node.textContent ?? "";
  if (!(node instanceof Element)) return "";
  if (!root && (node.getAttribute("aria-hidden") === "true" || node.hasAttribute("hidden"))) return "";
  if (!root) {
    const label = node.getAttribute("aria-label");
    if (label !== null && label.trim() !== "") return ` ${label} `;
  }
  if (node.tagName === "IMG") return node.getAttribute("alt") ?? "";
  if (node.tagName.toLowerCase() === "title" && node.parentElement?.tagName.toLowerCase() === "svg") return node.textContent ?? "";
  if (node instanceof HTMLInputElement && !root) return node.value;
  let out = "";
  for (const child of node.childNodes) out += textOf(child, false);
  return node instanceof HTMLElement && getComputedDisplayIsBlock(node) ? ` ${out} ` : out;
}

function getComputedDisplayIsBlock(el: HTMLElement): boolean {
  return /^(DIV|P|LI|TD|TH|TR|H[1-6])$/.test(el.tagName);
}

function byIds(doc: Document, ids: string): Element[] {
  return ids
    .trim()
    .split(/\s+/)
    .filter((id) => id !== "")
    .map((id) => doc.getElementById(id))
    .filter((el): el is HTMLElement => el !== null);
}

/** A simplified accessible name computation (accname 1.2, the parts this app uses). */
export function accessibleName(el: Element): string {
  const doc = el.ownerDocument;
  const clean = (s: string) => s.replace(/\s+/g, " ").trim();
  const labelledby = el.getAttribute("aria-labelledby");
  if (labelledby !== null) {
    const text = clean(byIds(doc, labelledby).map((ref) => textOf(ref, true)).join(" "));
    if (text !== "") return text;
  }
  const label = el.getAttribute("aria-label");
  if (label !== null && clean(label) !== "") return clean(label);
  if (el instanceof HTMLInputElement || el instanceof HTMLSelectElement || el instanceof HTMLTextAreaElement) {
    const labels = Array.from(el.labels ?? []);
    const text = clean(labels.map((l) => textOf(l, true)).join(" "));
    if (text !== "") return text;
    if (el instanceof HTMLInputElement && ["button", "submit", "reset"].includes(el.type)) return clean(el.value);
  }
  if (el.tagName === "IMG") {
    const alt = el.getAttribute("alt");
    if (alt !== null && clean(alt) !== "") return clean(alt);
  }
  if (el.tagName === "TABLE") {
    const caption = (el as HTMLTableElement).caption;
    if (caption !== null && clean(caption.textContent ?? "") !== "") return clean(caption.textContent ?? "");
  }
  if (el.tagName === "FIELDSET") {
    const legend = el.querySelector(":scope > legend");
    if (legend !== null && clean(legend.textContent ?? "") !== "") return clean(legend.textContent ?? "");
  }
  const role = roleOf(el);
  if (role !== null && NAME_FROM_CONTENT.has(role)) {
    const text = clean(textOf(el, true));
    if (text !== "") return text;
  }
  const title = el.getAttribute("title");
  if (title !== null && clean(title) !== "") return clean(title);
  return "";
}

/** The placeholder, which browsers use as a last-resort name but which disappears once the field has text. */
function placeholderOf(el: Element): string {
  return (el.getAttribute("placeholder") ?? el.getAttribute("aria-placeholder") ?? "").trim();
}

function describe(el: Element): string {
  const parts = [el.tagName.toLowerCase()];
  const role = el.getAttribute("role");
  if (role !== null) parts.push(`[role=${role}]`);
  if (el.id !== "") parts.push(`#${el.id}`);
  const cls = el.getAttribute("class");
  if (cls !== null && cls.trim() !== "") parts.push(`.${cls.trim().split(/\s+/)[0]}`);
  const name = accessibleName(el);
  if (name !== "") parts.push(` "${name.slice(0, 60)}"`);
  return parts.join("");
}

function hasContext(el: Element, roles: readonly string[]): boolean {
  const doc = el.ownerDocument;
  for (let cur = el.parentElement; cur !== null; cur = cur.parentElement) {
    const role = roleOf(cur);
    if (role !== null && roles.includes(role)) return true;
    if (role !== null && role !== "none" && role !== "presentation" && role !== "generic") {
      // A structural role in between (a group inside a tree is fine; that is covered by `roles`).
      if (!["group", "rowgroup"].includes(role)) return false;
    }
  }
  if (el.id !== "") {
    for (const owner of doc.querySelectorAll(`[aria-owns~="${CSS.escape(el.id)}"]`)) {
      const role = roleOf(owner);
      if (role !== null && roles.includes(role)) return true;
    }
  }
  return false;
}

/** The nearest descendants of `el` that have a role other than none/presentation/generic. */
function ownedRoles(el: Element): string[] {
  const out: string[] = [];
  const walk = (node: Element) => {
    for (const child of node.children) {
      if (isHiddenFromAT(child)) continue;
      const role = roleOf(child);
      if (role === null || role === "none" || role === "presentation" || role === "generic") {
        walk(child);
      } else {
        out.push(role);
      }
    }
  };
  walk(el);
  return out;
}

/**
 * Audits the DOM under `root` (default: the whole document) and returns
 * the violations, empty when there are none. Elements hidden from
 * assistive technology (aria-hidden, hidden, inert, display: none) are
 * skipped, except that a focusable element inside aria-hidden is itself a
 * violation.
 */
export function auditA11y(root: Element = document.body): A11yViolation[] {
  const doc = root.ownerDocument;
  const out: A11yViolation[] = [];
  const add = (rule: string, el: Element, message: string) => out.push({ rule, element: describe(el), message });

  const all = [root, ...root.querySelectorAll("*")];

  // Duplicate ids: references and label[for] resolve to the first one.
  const seen = new Map<string, Element>();
  for (const el of doc.querySelectorAll("[id]")) {
    if (el.id === "") continue;
    const first = seen.get(el.id);
    if (first !== undefined && (root.contains(el) || root.contains(first))) add("duplicate-id", el, `id "${el.id}" is used more than once`);
    else seen.set(el.id, el);
  }

  for (const el of all) {
    // Positive tabindex breaks the document's Tab order.
    const tabindex = el.getAttribute("tabindex");
    if (tabindex !== null && Number(tabindex) > 0) add("tabindex-positive", el, `tabindex="${tabindex}" puts it out of the document order`);

    // id references point at elements that exist.
    for (const attr of ID_REF_ATTRIBUTES) {
      const value = el.getAttribute(attr);
      if (value === null) continue;
      for (const id of value.trim().split(/\s+/)) {
        if (id !== "" && doc.getElementById(id) === null) add("idref-missing", el, `${attr} points at "${id}", which does not exist`);
      }
    }
    if (el instanceof HTMLLabelElement && el.htmlFor !== "" && doc.getElementById(el.htmlFor) === null) {
      add("idref-missing", el, `label for="${el.htmlFor}" points at an element that does not exist`);
    }

    // Roles are real roles.
    const explicit = el.getAttribute("role");
    if (explicit !== null && !explicit.trim().split(/\s+/).some((r) => VALID_ROLES.has(r))) {
      add("role-invalid", el, `role="${explicit}" is not a WAI-ARIA role`);
    }

    // Focusable content hidden from assistive technology.
    if (el.getAttribute("aria-hidden") === "true") {
      for (const inner of [el, ...el.querySelectorAll("*")]) {
        if (isTabbable(inner)) add("aria-hidden-focusable", inner, "is in the Tab order but inside aria-hidden");
      }
    }

    if (isHiddenFromAT(el)) continue;
    const role = roleOf(el);
    if (role === null) continue;

    if (NAME_REQUIRED.has(role) && accessibleName(el) === "") {
      if (placeholderOf(el) !== "") add("label-placeholder-only", el, `a ${role} is named only by its placeholder, which goes away as you type`);
      else if (!(role === "option" && el.getAttribute("value") === "")) {
        // An empty <option value=""> is a select's "none" choice; it has no text by design.
        add("name-missing", el, `a ${role} needs an accessible name (text, aria-label or aria-labelledby)`);
      }
    }

    const context = REQUIRED_CONTEXT[role];
    if (context !== undefined && explicit !== null && !hasContext(el, context)) {
      add("role-context", el, `a ${role} must be inside ${context.join(" or ")}`);
    }

    const children = REQUIRED_CHILDREN[role];
    if (children !== undefined && explicit !== null) {
      const owned = ownedRoles(el).filter((r) => !children.includes(r));
      if (owned.length > 0) add("role-children", el, `a ${role} may only own ${children.join(", ")}, not ${[...new Set(owned)].join(", ")}`);
    }

    // Composite widgets with roving focus: exactly one of their items is a Tab stop.
    if (role === "tablist" || role === "tree") {
      const itemRole = role === "tablist" ? "tab" : "treeitem";
      const items = Array.from(el.querySelectorAll(`[role='${itemRole}']`)).filter((item) => !isHiddenFromAT(item));
      const stops = items.filter((item) => isTabbable(item));
      if (items.length > 0 && stops.length !== 1) {
        add("roving-tabindex", el, `a ${role} should have exactly one ${itemRole} in the Tab order, not ${stops.length}`);
      }
    }
    if (role === "tab" && el.getAttribute("aria-selected") === null) add("aria-state", el, "a tab needs aria-selected");
    if (role === "treeitem" && el.getAttribute("aria-selected") === null && el.getAttribute("aria-checked") === null) {
      add("aria-state", el, "a treeitem needs aria-selected (or aria-checked)");
    }
    if (role === "tabpanel") {
      const by = el.getAttribute("aria-labelledby");
      const tabs = by === null ? [] : byIds(doc, by).filter((ref) => roleOf(ref) === "tab");
      if (tabs.length === 0) add("tabpanel-label", el, "a tabpanel should be aria-labelledby its tab");
    }

    // Interactive controls do not nest other Tab stops (a screen reader cannot reach them).
    if (["button", "link", "tab", "option", "treeitem", "menuitem", "switch", "checkbox"].includes(role) && el.matches(":not(input)")) {
      for (const inner of el.querySelectorAll("*")) {
        if (isTabbable(inner)) add("nested-interactive", inner, `is a Tab stop inside a ${role}`);
      }
    }
  }
  return out;
}

/** Fails the test with the list of violations under `root`. */
export function expectAccessible(root: Element = document.body): void {
  const violations = auditA11y(root);
  expect(violations.map((v) => `${v.rule}: ${v.element}: ${v.message}`)).toEqual([]);
}

/** The Tab stops under `root`, in Tab order (tabindex 0 and native controls, document order). */
export function tabStops(root: Element = document.body): HTMLElement[] {
  return Array.from(root.querySelectorAll<HTMLElement>("*")).filter((el) => isTabbable(el) && !isHiddenFromAT(el));
}

/**
 * Moves focus as Tab (or Shift+Tab) would, since jsdom has no default Tab
 * action: dispatches keydown on the focused element and, unless a handler
 * prevented it (a focus trap), focuses the next Tab stop, wrapping. Returns
 * the focused element.
 */
export function pressTab(shift = false): Element | null {
  const doc = document;
  const from = doc.activeElement ?? doc.body;
  const event = new KeyboardEvent("keydown", { key: "Tab", code: "Tab", shiftKey: shift, bubbles: true, cancelable: true });
  const proceed = from.dispatchEvent(event);
  if (proceed) {
    const stops = tabStops(doc.body);
    if (stops.length > 0) {
      const at = stops.indexOf(from as HTMLElement);
      let next: HTMLElement | undefined;
      if (at === -1) {
        // From outside the Tab order: the first stop after it in document order.
        const after = stops.find((s) => from.compareDocumentPosition(s) & Node.DOCUMENT_POSITION_FOLLOWING);
        const before = [...stops].reverse().find((s) => from.compareDocumentPosition(s) & Node.DOCUMENT_POSITION_PRECEDING);
        next = shift ? (before ?? stops[stops.length - 1]) : (after ?? stops[0]);
      } else {
        next = stops[(at + (shift ? -1 : 1) + stops.length) % stops.length];
      }
      next?.focus();
    }
  }
  const to = doc.activeElement;
  to?.dispatchEvent(new KeyboardEvent("keyup", { key: "Tab", code: "Tab", shiftKey: shift, bubbles: true }));
  return to;
}

/** Presses Tab until `match` is focused (at most `limit` times) and returns it; throws otherwise. */
export function tabTo(match: (el: Element) => boolean, opts: { shift?: boolean; limit?: number } = {}): HTMLElement {
  const limit = opts.limit ?? 200;
  for (let i = 0; i < limit; i++) {
    const el = pressTab(opts.shift ?? false);
    if (el !== null && match(el)) return el as HTMLElement;
  }
  throw new Error(`tabTo: no Tab stop matched within ${limit} presses; focus is on ${describe(document.activeElement ?? document.body)}`);
}

/** A matcher for tabTo: an element with this role and accessible name (a string is exact, a RegExp tests). */
export function byRole(role: string, name?: string | RegExp): (el: Element) => boolean {
  return (el) => {
    if (roleOf(el) !== role) return false;
    if (name === undefined) return true;
    const n = accessibleName(el);
    return typeof name === "string" ? n === name : name.test(n);
  };
}
