// XML and HTML highlighting for the response viewer (HTTP-31).
//
// No @codemirror/lang-xml or lang-html is installed, so this is a small
// CodeMirror StreamLanguage tokenizer, like graphql.ts. It only colours
// markup; well-formedness is checked and XML is indented in Go
// (xmldom.Indent). HTML is read leniently: unquoted attribute values, and
// the text of <script> and <style> left plain up to their end tag.
//
// Token -> tag -> colour (theme.ts):
//   tagName        element names              --tk-key
//   attributeName  attribute names            --tk-arg
//   string         attribute values           --tk-str
//   angleBracket   < > </ />  and =           --tk-punct
//   comment        <!-- ... -->               --tk-cm
//   meta           <?...?>, <!DOCTYPE ...>, CDATA markers  --tk-dir
//   character      &entities; and &#refs;     --tk-num
//
// Folding follows indentation (foldByIndent), which fits the indented XML
// Go returns and most HTML.

import {
  LanguageSupport,
  StreamLanguage,
  foldService,
  type StreamParser,
  type StringStream,
} from "@codemirror/language";
import type { EditorState } from "@codemirror/state";

export interface MarkupState {
  /** Inside a tag, after its name: attributes follow. */
  inTag: boolean;
  /** The next name is the tag's name. */
  expectName: boolean;
  /** The tag being read, lower-case, for HTML raw text elements. */
  tagName: string;
  /** Inside an attribute value that spans lines: its quote. */
  quote: string;
  /** Inside a multi-line comment, CDATA section or declaration: its end. */
  until: string;
  /** Style of the text up to `until`. */
  untilStyle: string;
  /** HTML: inside <script> or <style>, the element's name. */
  rawText: string;
  /** After "=": an attribute value follows. */
  expectValue: boolean;
  /** The tag being read is an end tag. */
  closing: boolean;
}

const NAME = /^[A-Za-z_:][-\w.:]*/;
const RAW_TEXT = new Set(["script", "style"]);

function markupParser(html: boolean): StreamParser<MarkupState> {
  return {
    name: html ? "html" : "xml",
    startState: () => ({
      inTag: false,
      expectName: false,
      tagName: "",
      quote: "",
      until: "",
      untilStyle: "",
      rawText: "",
      closing: false,
      expectValue: false,
    }),
    copyState: (s) => ({ ...s }),
    token(stream: StringStream, state: MarkupState): string | null {
      if (state.until !== "") {
        const style = state.untilStyle;
        const end = stream.string.indexOf(state.until, stream.pos);
        if (end < 0) {
          stream.skipToEnd();
        } else {
          stream.pos = end + state.until.length;
          state.until = "";
        }
        return style;
      }
      if (state.quote !== "") {
        const end = stream.string.indexOf(state.quote, stream.pos);
        if (end < 0) {
          stream.skipToEnd();
        } else {
          stream.pos = end + 1;
          state.quote = "";
        }
        return "string";
      }
      if (state.rawText !== "") {
        const close = new RegExp(`</${state.rawText}\\b`, "i");
        const rest = stream.string.slice(stream.pos);
        const m = close.exec(rest);
        if (m === null) {
          stream.skipToEnd();
          return null;
        }
        if (m.index > 0) {
          stream.pos += m.index;
          return null;
        }
        state.rawText = "";
      }
      return state.inTag ? inTag(stream, state, html) : inContent(stream, state);
    },
    languageData: {
      commentTokens: { block: { open: "<!--", close: "-->" } },
    },
  };
}

function inContent(stream: StringStream, state: MarkupState): string | null {
  if (stream.match("<!--")) {
    state.until = "-->";
    state.untilStyle = "comment";
    return "comment";
  }
  if (stream.match("<![CDATA[")) {
    state.until = "]]>";
    state.untilStyle = "meta";
    return "meta";
  }
  if (stream.match("<?")) {
    state.until = "?>";
    state.untilStyle = "meta";
    return "meta";
  }
  if (stream.match(/^<![A-Za-z]/)) {
    state.until = ">";
    state.untilStyle = "meta";
    return "meta";
  }
  if (stream.match(/^<\/?(?=[A-Za-z_:])/)) {
    state.closing = stream.current() === "</";
    state.inTag = true;
    state.expectName = true;
    return "angleBracket";
  }
  if (stream.match(/^&(#\d+|#x[0-9A-Fa-f]+|[A-Za-z][\w.-]*);/)) return "character";
  stream.next();
  stream.eatWhile(/[^<&]/);
  return null;
}

function inTag(stream: StringStream, state: MarkupState, html: boolean): string | null {
  if (stream.eatSpace()) return null;
  if (state.expectName) {
    state.expectName = false;
    if (stream.match(NAME)) {
      state.tagName = stream.current().toLowerCase();
      return "tagName";
    }
  }
  if (stream.match("/>") || stream.match(">")) {
    state.inTag = false;
    const selfClosing = stream.current() === "/>";
    if (html && !state.closing && !selfClosing && RAW_TEXT.has(state.tagName)) state.rawText = state.tagName;
    return "angleBracket";
  }
  if (stream.eat("=")) {
    state.expectValue = true;
    return "angleBracket";
  }
  const value = state.expectValue;
  state.expectValue = false;
  const q = stream.peek();
  if (q === '"' || q === "'") {
    stream.next();
    const end = stream.string.indexOf(q, stream.pos);
    if (end < 0) {
      stream.skipToEnd();
      state.quote = q;
    } else {
      stream.pos = end + 1;
    }
    return "string";
  }
  if (value) {
    // An unquoted value: valid in HTML, an error in XML.
    stream.eatWhile(/[^\s>"'=`<]/);
    if (stream.current() === "") stream.next();
    return html ? "string" : "invalid";
  }
  if (stream.match(NAME)) return "attributeName";
  stream.next();
  return "invalid";
}

/** Folds a line with the following lines that are indented deeper. */
export function foldByIndent(state: EditorState, lineStart: number): { from: number; to: number } | null {
  const line = state.doc.lineAt(lineStart);
  const indent = (text: string): number => text.length - text.trimStart().length;
  if (line.text.trim() === "") return null;
  const base = indent(line.text);
  let last = line;
  for (let n = line.number + 1; n <= state.doc.lines; n++) {
    const next = state.doc.line(n);
    if (next.text.trim() === "") continue;
    if (indent(next.text) <= base) break;
    last = next;
  }
  return last.number > line.number ? { from: line.to, to: last.to } : null;
}

export const xmlLanguage = StreamLanguage.define(markupParser(false));
export const htmlLanguage = StreamLanguage.define(markupParser(true));

export function xml(): LanguageSupport {
  return new LanguageSupport(xmlLanguage, foldService.of(foldByIndent));
}

export function html(): LanguageSupport {
  return new LanguageSupport(htmlLanguage, foldService.of(foldByIndent));
}
