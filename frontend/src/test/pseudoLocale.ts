// Helpers for tests that render the UI in the pseudo-locale (UI-10,
// docs/frontend.md#localization): every catalogue message then reads
// "[Šéñð ŕéǫûéšţ one]", so English text of a converted namespace that is
// still on screen came from somewhere else than t().
import { en, type Namespace } from "../i18n/catalog";
import type { Message } from "../i18n/messages";
import { PSEUDO_CLOSE, PSEUDO_OPEN } from "../i18n/pseudo";

/** Attributes whose text a user sees or hears. */
const TEXT_ATTRIBUTES = ["aria-label", "aria-description", "aria-roledescription", "aria-valuetext", "title", "placeholder", "alt"];

/** Fragments this long are looked for inside longer text too; shorter ones only as a whole text. */
const SUBSTRING_MIN = 16;

/**
 * Every piece of text a user can read or hear under the roots: text nodes
 * (trimmed) and text attributes, leaving out the subtrees in `exclude`.
 */
export function visibleTexts(roots: readonly Element[], exclude: readonly Element[] = []): string[] {
  const out: string[] = [];
  const skipped = (node: Node) => exclude.some((e) => e.contains(node));
  for (const root of roots) {
    const walker = root.ownerDocument.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    for (let n = walker.nextNode(); n !== null; n = walker.nextNode()) {
      const text = n.textContent?.trim() ?? "";
      if (text !== "" && !skipped(n)) out.push(text);
    }
    for (const el of [root, ...root.querySelectorAll("*")]) {
      if (skipped(el)) continue;
      for (const name of TEXT_ATTRIBUTES) {
        const value = el.getAttribute(name)?.trim();
        if (value) out.push(value);
      }
    }
  }
  return out;
}

export interface LeftoverOptions {
  /** Subtrees of areas not converted yet (a component of another area inside this one). */
  readonly exclude?: readonly Element[];
  /**
   * Text of areas not converted yet that shows inside these roots, taken out
   * of each text before it is checked: the shortcut registry's labels on the
   * Shortcuts page, say. Remove an entry when its area is converted.
   */
  readonly foreign?: readonly string[];
}

/** The English text of the namespaces' messages, split around their placeholders, pieces with letters only. */
export function englishFragments(namespaces: readonly Namespace[]): string[] {
  const out = new Set<string>();
  const add = (text: string) => {
    for (const piece of text.split(/\{\{.*?\}\}|\{[A-Za-z0-9_]+\}/)) {
      const trimmed = piece.trim();
      if (/\p{L}/u.test(trimmed)) out.add(trimmed);
    }
  };
  for (const ns of namespaces) {
    for (const message of Object.values(en[ns]) as Message[]) {
      if (typeof message === "string") add(message);
      else for (const form of Object.values(message)) if (typeof form === "string") add(form);
    }
  }
  return [...out];
}

/**
 * The texts under the roots that are English messages of the namespaces:
 * a text equal to a message fragment, or containing a long one. Empty when
 * everything those namespaces show went through t().
 */
export function englishLeftovers(
  roots: readonly Element[],
  namespaces: readonly Namespace[],
  { exclude = [], foreign = [] }: LeftoverOptions = {},
): string[] {
  const fragments = englishFragments(namespaces);
  const exact = new Set(fragments);
  const long = fragments.filter((f) => f.length >= SUBSTRING_MIN);
  // Longest first, so a foreign text is not cut up by a shorter one inside it.
  const others = [...new Set(foreign)].filter((f) => f !== "").sort((a, b) => b.length - a.length);
  const CUT = "\u0000";
  const found = new Set<string>();
  for (const seen of visibleTexts(roots, exclude)) {
    let text = seen;
    for (const f of others) text = text.split(f).join(CUT);
    const pieces = text.split(CUT).map((p) => p.trim());
    if (pieces.some((p) => exact.has(p)) || long.some((f) => text.includes(f))) found.add(seen);
  }
  return [...found];
}

/** Whether a text is (or holds) a pseudo-locale message. */
export function isPseudo(text: string): boolean {
  const open = text.indexOf(PSEUDO_OPEN);
  return open >= 0 && text.indexOf(PSEUDO_CLOSE, open) > open;
}
