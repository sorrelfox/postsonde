// A small scanner for `{{variable}}` syntax, matching the Go parser
// in internal/core/template (docs/templates.md) so the editor marks exactly
// what a send resolves: `{{name}}` with optional whitespace, `\{{` escapes
// (an odd run of backslashes makes the braces literal), the last two braces
// of a `{{{` run open the variable, and a `{{` with no `}}` before the end or
// the next `{{` is unterminated. It only locates references; resolution and
// hover come later.
//
// Offsets are UTF-16 indexes (JavaScript string positions). The syntax is
// ASCII-only, so they mark the same spans as the Go parser's byte offsets.

export type TemplateSpanKind = "var" | "unterminated";

export interface TemplateSpan {
  readonly kind: TemplateSpanKind;
  /** `src.slice(start, end)` is the raw text: `{{ name }}`, or `{{` when unterminated. */
  readonly start: number;
  readonly end: number;
  /** The name without surrounding whitespace ("" for `{{ }}` and unterminated). */
  readonly name: string;
}

const SPACES = " \t\r\n";

function isSpace(ch: string | undefined): boolean {
  return ch !== undefined && SPACES.includes(ch);
}

/** The `{{…}}` references and unterminated `{{` in `src`, in order. */
export function scanTemplate(src: string): TemplateSpan[] {
  const spans: TemplateSpan[] = [];
  let i = 0;
  while (i < src.length) {
    const ch = src[i];
    if (ch === "\\") {
      let n = 0;
      while (src[i + n] === "\\") n++;
      if (src.startsWith("{{", i + n) && n % 2 === 1) {
        i += n + 2; // escaped: literal braces
      } else {
        i += n; // plain backslashes, or pairs before a variable
      }
      continue;
    }
    if (ch !== "{" || !src.startsWith("{{", i) || src[i + 2] === "{") {
      i++;
      continue;
    }
    const close = src.indexOf("}}", i + 2);
    const reopen = src.indexOf("{{", i + 2);
    if (close < 0 || (reopen >= 0 && reopen < close)) {
      spans.push({ kind: "unterminated", start: i, end: i + 2, name: "" });
      i += 2;
      continue;
    }
    let a = i + 2;
    let b = close;
    while (a < b && isSpace(src[a])) a++;
    while (b > a && isSpace(src[b - 1])) b--;
    spans.push({ kind: "var", start: i, end: close + 2, name: src.slice(a, b) });
    i = close + 2;
  }
  return spans;
}

/** The `{{…}}` references in `src` (unterminated braces left out). */
export function templateVars(src: string): TemplateSpan[] {
  return scanTemplate(src).filter((s) => s.kind === "var");
}
