// The shape of a message catalogue and the types that check t() calls
// (UI-10, docs/frontend.md#localization).
//
// A namespace module (en/<area>.ts) is a flat object of messages:
//
//   export default {
//     "statusBar.ready": "Ready",
//     "theme.label": "Theme: {name}",
//     "autosave.saving": { one: "Saving {count} request…", other: "Saving {count} requests…" },
//   } as const satisfies Messages;
//
// A message is a string with {name} placeholders, or a plural message: one
// string per plural category of Intl.PluralRules (zero, one, two, few,
// many, other; `other` is required), and optionally exact counts ("=0"),
// chosen by the `count` parameter. `{{` and `}}` are literal text (the
// {{variable}} syntax of requests), never a placeholder.
//
// `as const` keeps each message's literal type, so t() knows which
// parameters a message takes.

/** The plural categories of Intl.PluralRules. */
export type PluralCategory = "zero" | "one" | "two" | "few" | "many" | "other";

/** A message chosen by a count: `other` and any other category or exact count ("=0"). */
export type PluralMessage = { readonly other: string } & {
  readonly [C in Exclude<PluralCategory, "other">]?: string;
} & { readonly [exact: `=${number}`]: string | undefined };

export type Message = string | PluralMessage;

/** A namespace module: message ids to messages. */
export type Messages = Readonly<Record<string, Message>>;

type IdentChar =
  | "a" | "b" | "c" | "d" | "e" | "f" | "g" | "h" | "i" | "j" | "k" | "l" | "m"
  | "n" | "o" | "p" | "q" | "r" | "s" | "t" | "u" | "v" | "w" | "x" | "y" | "z"
  | "A" | "B" | "C" | "D" | "E" | "F" | "G" | "H" | "I" | "J" | "K" | "L" | "M"
  | "N" | "O" | "P" | "Q" | "R" | "S" | "T" | "U" | "V" | "W" | "X" | "Y" | "Z"
  | "0" | "1" | "2" | "3" | "4" | "5" | "6" | "7" | "8" | "9" | "_";

/** True when S is a placeholder name: letters, digits and "_" (PLACEHOLDER in format.ts). */
type IsIdent<S extends string> = S extends `${infer C}${infer R}`
  ? C extends IdentChar
    ? R extends ""
      ? true
      : IsIdent<R>
    : false
  : false;

/** The placeholder names in a message string: "Hi {name}" → "name". */
export type PlaceholdersOf<S extends string> = S extends `${string}{${infer P}}${infer Rest}`
  ? (IsIdent<P> extends true ? P : never) | PlaceholdersOf<Rest>
  : never;

/** A placeholder's value: text, or a number (written with the locale's digits, no grouping). */
export type ParamValue = string | number;

type StringsOf<M> = M extends string ? M : M extends PluralMessage ? Extract<M[keyof M], string> : never;

/** The parameters a message takes: its placeholders, and `count` for a plural message. */
export type ParamsOf<M> = M extends string
  ? { readonly [P in PlaceholdersOf<M>]: ParamValue }
  : M extends PluralMessage
    ? { readonly count: number } & { readonly [P in Exclude<PlaceholdersOf<StringsOf<M>>, "count">]: ParamValue }
    : never;
