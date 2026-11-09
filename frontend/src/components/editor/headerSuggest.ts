// The Headers table's autocomplete (HTTP-07): header names in the key
// column, values for the row's header in the value column.

import { suggestHeaderNames, suggestHeaderValues } from "../../lib/headerSuggestions";
import { t } from "../../i18n";
import type { KeyValueColumn, KeyValueSuggest } from "../common/KeyValueTable";

export const HEADER_SUGGEST: Partial<Record<KeyValueColumn, KeyValueSuggest>> = {
  key: (text) => suggestHeaderNames(text),
  value: (text, row) => suggestHeaderValues(text, row.key),
};

/** The suggestion lists' names, read in the language in use each time. */
export const HEADER_SUGGEST_LABELS: Partial<Record<KeyValueColumn, string>> = {
  get key() {
    return t("editor.headerSuggest.names");
  },
  get value() {
    return t("editor.headerSuggest.values");
  },
};
