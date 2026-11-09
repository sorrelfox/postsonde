import { useState } from "react";
import { useT } from "../../i18n/react";
import { CodeEditor } from "./CodeEditor";
import styles from "./EditorPreview.module.css";

// Skeleton preview of the shared editor: an editable JSON request body
// (switchable to a GraphQL sample) next to a read-only JSON response
// view, with sample data. Mounted into the
// app shell's panes until the real request and response panes exist.

// Sample documents, not UI text.
// i18n-ignore-start
const REQUEST_BODY = `{
  "name": "Studio Monitor 5",
  "sku": "SM-5-BLK",
  "price": 349.0,
  "stock": 12,
  "active": true,
  "discontinued": null,
  "tags": ["audio", "monitor", "studio"],
  "dimensions": { "width": 180, "height": 260, "depth": 215 }
}
`;

const GRAPHQL_BODY = `# Product with its variants (GraphQL highlighting)
query GetProduct($id: ID!, $withVariants: Boolean = true) {
  product(id: $id) {
    ...ProductFields
    price(currency: EUR) { amount }
    variants(first: 10) @include(if: $withVariants) {
      sku
      colour: color
    }
  }
}

fragment ProductFields on Product {
  id
  name
  description(format: """
    plain
  """)
}
`;

const REQUEST_SAMPLES = {
  json: { label: "JSON", body: REQUEST_BODY },
  graphql: { label: "GraphQL", body: GRAPHQL_BODY },
} as const;

const RESPONSE_BODY =`${JSON.stringify(
  {
    data: {
      id: "prd_01J8Z6Q4M2",
      name: "Studio Monitor 5",
      price: { amount: 349, currency: "EUR" },
      stock: 12,
      active: true,
      discontinued: null,
      tags: ["audio", "monitor", "studio"],
      variants: [
        { sku: "SM-5-BLK", colour: "black", stock: 8 },
        { sku: "SM-5-WHT", colour: "white", stock: 4 },
      ],
      createdAt: "2026-09-15T09:30:00Z",
    },
    meta: { requestId: "req_7c1e", durationMs: 132 },
  },
  null,
  2,
)}
`;
// i18n-ignore-end

export function EditorPreview() {
  const t = useT();
  const [lang, setLang] = useState<keyof typeof REQUEST_SAMPLES>("json");
  const [chars, setChars] = useState(REQUEST_BODY.length);
  const sample = REQUEST_SAMPLES[lang];
  const other = lang === "json" ? "graphql" : "json";

  return (
    <section className={styles.preview} aria-label={t("editor.preview.editor")}>
      <div className={styles.pane}>
        <header className={styles.header}>
          <span>{t("editor.body.label")}</span>
          <span className={styles.meta}>
            <button
              type="button"
              className={styles.toggle}
              onClick={() => {
                setLang(other);
                setChars(REQUEST_SAMPLES[other].body.length);
              }}
              title={t("editor.preview.showSample", { language: REQUEST_SAMPLES[other].label })}
            >
              {sample.label}
            </button>{" "}
            · {t("editor.preview.chars", { count: chars })}
          </span>
        </header>
        <div className={styles.editor}>
          <CodeEditor
            value={sample.body}
            docKey={lang}
            language={lang}
            ariaLabel={t("editor.body.label")}
            onChange={(doc) => setChars(doc.length)}
          />
        </div>
      </div>
      <div className={styles.pane}>
        <header className={styles.header}>
          <span>{t("editor.preview.response")}</span>
          <span className={styles.meta}>{t("editor.preview.responseMeta")}</span>
        </header>
        <div className={styles.editor}>
          <CodeEditor
            value={RESPONSE_BODY}
            language="json"
            ariaLabel={t("editor.preview.responseBody")}
            readOnly
          />
        </div>
      </div>
      <p className={styles.hints}>{t("editor.preview.editorHints")}</p>
    </section>
  );
}
