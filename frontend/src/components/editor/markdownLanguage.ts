// Markdown highlighting for the notes editor (COL-07).
//
// No @codemirror/lang-markdown is installed, so this is a small CodeMirror
// StreamLanguage tokenizer, like graphql.ts and markup.ts. It only colours
// the source line by line; what the note means is decided by the
// renderer's parser (lib/markdown.ts).
//
// Token -> tag -> style (theme.ts):
//   heading        heading lines                  bold, --tk-key
//   strong         **strong**, __strong__         bold
//   emphasis       *em*, _em_                     italic
//   strikethrough  ~~deleted~~                    line-through
//   monospace      `code`, fenced code lines      --tk-str
//   link, url      [text](url), bare http(s) URLs --tk-str
//   quote          > quote markers                --tk-cm
//   list           list markers, task boxes       --tk-punct
//   meta           code fences                    --tk-dir
//   contentSeparator  --- rules                   --tk-punct

import { LanguageSupport, StreamLanguage, type StreamParser, type StringStream } from "@codemirror/language";

interface MarkdownState {
  /** Inside a fenced code block: its fence ("```" or "~~~", possibly longer). */
  fence: string;
}

const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const HEADING = /^ {0,3}#{1,6}(?:[ \t]|$)/;
const QUOTE = /^ {0,3}>(?: ?>)*/;
const LIST = /^ *(?:[-+*]|\d{1,9}[.)])(?=[ \t]|$)(?:[ \t]+\[[ xX]\])?/;
const RULE = /^ {0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;

const parser: StreamParser<MarkdownState> = {
  name: "markdown",
  startState: () => ({ fence: "" }),
  copyState: (s) => ({ ...s }),
  token(stream: StringStream, state: MarkdownState): string | null {
    if (stream.sol()) {
      if (state.fence !== "") {
        const close = stream.match(FENCE, false) as RegExpMatchArray | null;
        const marker = close?.[1] ?? "";
        if (marker[0] === state.fence[0] && marker.length >= state.fence.length && stream.string.trim() === marker) {
          state.fence = "";
          stream.skipToEnd();
          return "meta";
        }
        stream.skipToEnd();
        return "monospace";
      }
      const open = stream.match(FENCE, false) as RegExpMatchArray | null;
      if (open !== null) {
        state.fence = open[1] ?? "```";
        stream.skipToEnd();
        return "meta";
      }
      if (stream.match(HEADING, false)) {
        stream.skipToEnd();
        return "heading";
      }
      if (stream.match(RULE)) return "contentSeparator";
      if (stream.match(QUOTE)) return "quote";
      if (stream.match(LIST)) return "list";
    }
    if (state.fence !== "") {
      stream.skipToEnd();
      return "monospace";
    }

    if (stream.match(/^\\./)) return null;
    if (stream.match(/^(`+)[^`]+?\1/)) return "monospace";
    if (stream.match(/^(\*\*|__)(?=\S).*?\S\1/)) return "strong";
    if (stream.match(/^(\*|_)(?=[^\s*_]).*?[^\s\\]\1/)) return "emphasis";
    if (stream.match(/^~~(?=\S).*?\S~~/)) return "strikethrough";
    if (stream.match(/^!?\[[^\]]*\](?:\([^)\s]*(?:\s+"[^"]*")?\)|\[[^\]]*\])/)) return "link";
    if (stream.match(/^https?:\/\/[^\s<>()]+/i)) return "url";
    stream.next();
    stream.eatWhile(/[^\\`*_~[!h]/);
    return null;
  },
};

export const markdownLanguage = StreamLanguage.define(parser);

/** Markdown highlighting for CodeMirror. */
export function markdown(): LanguageSupport {
  return new LanguageSupport(markdownLanguage);
}
