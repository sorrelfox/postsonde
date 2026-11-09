import type { CSSProperties } from "react";
import { useT } from "../../i18n/react";
import styles from "./TokenPreview.module.css";

// A compact preview of the design tokens, shown in the request pane
// until the request editor is mounted there. Handy for checking both
// themes by eye.

const SURFACES = ["bg0", "bg1", "bg2", "bg3", "line", "line2"];
const TEXTS = ["text", "text2", "text3", "accent", "accent-dim", "sel"];
const METHODS: readonly [string, string][] = [
  ["GET", "m-get"],
  ["POST", "m-post"],
  ["PUT", "m-put"],
  ["PATCH", "m-patch"],
  ["DEL", "m-del"],
  ["GQL", "m-gql"],
  ["DIR", "m-dir"],
  ["WS", "m-ws"],
];
const STATUS = ["ok", "warn", "bad"];

const fg = (token: string): CSSProperties => ({ color: `var(--${token})` });

function Tk({ t, children }: { t: string; children: string }) {
  return <span style={fg(`tk-${t}`)}>{children}</span>;
}

function Swatch({ token }: { token: string }) {
  return (
    <span className={styles.swatch}>
      <span className={styles.chip} style={{ background: `var(--${token})` }} />
      --{token}
    </span>
  );
}

export function TokenPreview() {
  const t = useT();
  return (
    <section className={styles.preview} aria-label={t("shell.tokenPreview.label")}>
      <p className={styles.intro}>{t("shell.tokenPreview.intro")}</p>

      <div>
        <p className={styles.caption}>{t("shell.tokenPreview.surfaces")}</p>
        <div className={styles.swatches}>
          {SURFACES.map((t) => (
            <Swatch key={t} token={t} />
          ))}
        </div>
      </div>

      <div>
        <p className={styles.caption}>{t("shell.tokenPreview.text")}</p>
        <div className={styles.swatches}>
          {TEXTS.map((t) => (
            <Swatch key={t} token={t} />
          ))}
        </div>
      </div>

      <div>
        <p className={styles.caption}>{t("shell.tokenPreview.methods")}</p>
        <div className={styles.row}>
          {METHODS.map(([label, token]) => (
            <span key={token} className={styles.method} style={fg(token)}>
              {label}
            </span>
          ))}
        </div>
        <div className={styles.row}>
          {STATUS.map((t) => (
            <span key={t} className={styles.status} style={fg(t)}>
              <span className={styles.dot} />
              --{t}
            </span>
          ))}
        </div>
      </div>

      <div>
        <p className={styles.caption}>{t("shell.tokenPreview.syntax")}</p>
        <pre className={styles.code}>
          <Tk t="cm"># Order status</Tk>
          {"\n"}
          <Tk t="kw">query</Tk> <Tk t="field">Order</Tk>
          <Tk t="punct">(</Tk>
          <Tk t="var">$id</Tk>
          <Tk t="punct">:</Tk> <Tk t="type">ID!</Tk>
          <Tk t="punct">)</Tk> <Tk t="dir">@cached</Tk> <Tk t="punct">{"{"}</Tk> <Tk t="field">status</Tk>{" "}
          <Tk t="err">totl</Tk> <Tk t="punct">{"}"}</Tk>
          {"\n"}
          <Tk t="punct">{"{ "}</Tk>
          <Tk t="key">"name"</Tk>
          <Tk t="punct">: </Tk>
          <Tk t="str">"Studio Monitor 5"</Tk>
          <Tk t="punct">, </Tk>
          <Tk t="key">"stock"</Tk>
          <Tk t="punct">: </Tk>
          <Tk t="num">12</Tk>
          <Tk t="punct">, </Tk>
          <Tk t="key">"active"</Tk>
          <Tk t="punct">: </Tk>
          <Tk t="bool">true</Tk>
          <Tk t="punct">{" }"}</Tk>
        </pre>
      </div>
    </section>
  );
}
