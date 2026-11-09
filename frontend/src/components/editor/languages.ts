// Language registry for the shared code editor. Each entry turns a
// language id into the CodeMirror extension that parses and highlights it.
//
// To add a language, add an entry here; the `EditorLanguage` type follows.
// GraphQL is a stream tokenizer: highlighting, bracket matching and
// folding. Parsing, validation and completion run in the Go engine and will
// be wired into this entry later. XML and HTML (markup.ts, HTTP-31) are
// stream tokenizers too, for the response viewer.

import type { Extension } from "@codemirror/state";
import { json } from "@codemirror/lang-json";
import { graphql } from "./graphql";
import { html, xml } from "./markup";
import { markdown } from "./markdownLanguage";

const LANGUAGES = {
  json: () => json(),
  graphql: () => graphql(),
  xml: () => xml(),
  html: () => html(),
  markdown: () => markdown(),
  text: () => [],
} satisfies Record<string, () => Extension>;

export type EditorLanguage = keyof typeof LANGUAGES;

// One extension value per language, so reconfiguring to the language that is
// already active can be detected by identity and skipped.
const cache = new Map<EditorLanguage, Extension>();

export function languageExtension(language: EditorLanguage): Extension {
  let ext = cache.get(language);
  if (ext === undefined) {
    ext = LANGUAGES[language]();
    cache.set(language, ext);
  }
  return ext;
}
