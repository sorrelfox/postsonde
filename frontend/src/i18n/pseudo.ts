// The pseudo-locale (UI-10): every English message, accented, about 30%
// longer and in brackets, "[Šéñð ŕéqûéšţ one two]". Built from the English
// catalogue when it is selected, never written by hand, so it always has
// every message. Text that shows up unchanged in the UI with it on did not
// come from the catalogue; text cut off or overflowing does not leave room
// for longer translations. Placeholders and `{{…}}` stay as they are.
import { isPluralMessage } from "./format";
import type { Message, Messages } from "./messages";

/** The pseudo-locale's tag: English, in the private-use region XA, as Android and Chrome name theirs. */
export const PSEUDO_LOCALE = "en-XA";

const ACCENTED: Readonly<Record<string, string>> = {
  a: "á", b: "ƀ", c: "ç", d: "ð", e: "é", f: "ƒ", g: "ĝ", h: "ĥ", i: "î", j: "ĵ", k: "ķ", l: "ļ", m: "ɱ",
  n: "ñ", o: "ö", p: "þ", q: "ǫ", r: "ŕ", s: "š", t: "ţ", u: "û", v: "ṽ", w: "ŵ", x: "ẋ", y: "ý", z: "ž",
  A: "Å", B: "Ɓ", C: "Ç", D: "Ð", E: "É", F: "Ƒ", G: "Ĝ", H: "Ĥ", I: "Î", J: "Ĵ", K: "Ķ", L: "Ļ", M: "Ṁ",
  N: "Ñ", O: "Ö", P: "Þ", Q: "Ǫ", R: "Ŕ", S: "Š", T: "Ţ", U: "Û", V: "Ṽ", W: "Ŵ", X: "Ẋ", Y: "Ý", Z: "Ž",
};

const PADDING = ["one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"];

/** Share of extra length: translations often run about 30% longer than English. */
const EXPANSION = 0.3;

/** Opening and closing marks of every pseudo message. */
export const PSEUDO_OPEN = "[";
export const PSEUDO_CLOSE = "]";

function accent(text: string): string {
  let out = "";
  for (const ch of text) out += ACCENTED[ch] ?? ch;
  return out;
}

/** What pseudo strings keep as it is: placeholders, and `{{…}}` (template variable syntax). */
const KEEP = /\{\{.*?\}\}|\{[A-Za-z0-9_]+\}/g;

/** One pseudo string: letters outside placeholders accented, padding words, brackets. */
export function pseudoString(text: string): string {
  let out = "";
  let visible = 0;
  let last = 0;
  for (const match of text.matchAll(KEEP)) {
    const index = match.index ?? 0;
    const between = text.slice(last, index);
    out += accent(between) + match[0];
    visible += [...between].length;
    last = index + match[0].length;
  }
  const rest = text.slice(last);
  out += accent(rest);
  visible += [...rest].length;

  const target = Math.max(1, Math.ceil(visible * EXPANSION));
  let padding = "";
  for (let i = 0; padding.length < target; i++) padding += ` ${PADDING[i % PADDING.length]}`;
  return `${PSEUDO_OPEN}${out}${padding}${PSEUDO_CLOSE}`;
}

/** The pseudo version of a message, each plural form on its own. */
export function pseudoMessage(message: Message): Message {
  if (!isPluralMessage(message)) return pseudoString(message);
  const out: Record<string, string> = {};
  for (const [form, text] of Object.entries(message)) {
    if (typeof text === "string") out[form] = pseudoString(text);
  }
  return out as unknown as Message;
}

/** The pseudo version of a catalogue: every namespace, every message. */
export function pseudoCatalogue<C extends Readonly<Record<string, Messages>>>(catalogue: C): C {
  const out: Record<string, Record<string, Message>> = {};
  for (const [namespace, messages] of Object.entries(catalogue)) {
    const ns: Record<string, Message> = {};
    for (const [id, message] of Object.entries(messages)) ns[id] = pseudoMessage(message);
    out[namespace] = ns;
  }
  return out as unknown as C;
}
