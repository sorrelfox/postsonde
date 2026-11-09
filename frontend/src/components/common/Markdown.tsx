import { memo, useMemo, type KeyboardEvent, type ReactNode } from "react";
import { parseMarkdown, type Block, type Inline, type LinkTarget } from "../../lib/markdown";
import { t } from "../../i18n";
import { useT } from "../../i18n/react";
import styles from "./Markdown.module.css";

// Renders Markdown notes (COL-07) as React elements, from the data
// lib/markdown.ts parses. Never through dangerouslySetInnerHTML: every
// piece of source text ends up as a text node, so raw HTML in a note shows
// as typed.
//
// Nothing rendered here loads anything:
// - links have no href, so neither a click nor the webview's own link
//   handling can navigate the app; an http(s) or mailto link calls
//   `onOpenLink`, which asks and then opens the system browser
//   (commands/links.ts). Other links (javascript:, relative paths,
//   anchors) are inert text with the destination in their tooltip;
// - images are never <img>: they show as a placeholder with the alt text,
//   a link to the image's URL when it is http(s);
// - there is no <iframe>, <link>, <script>, <style> or <object> output.

export interface MarkdownProps {
  /** The Markdown source. */
  source: string;
  /** Called with an http(s) or mailto URL when its link is activated. Without it, links are inert. */
  onOpenLink?: (url: string) => void;
  className?: string;
}

type Open = ((url: string) => void) | undefined;

/** Markdown source as React elements; parsed again only when `source` changes. */
export const Markdown = memo(function Markdown({ source, onOpenLink, className }: MarkdownProps) {
  // Re-renders in a new language; the render functions below call t().
  useT();
  const blocks = useMemo(() => parseMarkdown(source), [source]);
  return (
    <div className={className !== undefined ? `${styles.markdown} ${className}` : styles.markdown} data-markdown="">
      {renderBlocks(blocks, onOpenLink, false)}
    </div>
  );
});

function renderBlocks(blocks: Block[], open: Open, tight: boolean): ReactNode[] {
  return blocks.map((b, i) => renderBlock(b, i, open, tight));
}

function renderBlock(b: Block, key: number, open: Open, tight: boolean): ReactNode {
  switch (b.type) {
    case "heading": {
      const H = `h${b.level}` as const;
      return <H key={key}>{renderInlines(b.children, open)}</H>;
    }
    case "paragraph":
      // In a tight list an item's paragraphs have no spacing of their own.
      return tight ? (
        <span key={key}>
          {renderInlines(b.children, open)}
        </span>
      ) : (
        <p key={key}>{renderInlines(b.children, open)}</p>
      );
    case "code":
      return (
        <pre key={key} data-lang={b.lang || undefined}>
          <code>{b.text}</code>
        </pre>
      );
    case "quote":
      return <blockquote key={key}>{renderBlocks(b.children, open, false)}</blockquote>;
    case "hr":
      return <hr key={key} />;
    case "list": {
      const items = b.items.map((item, i) => (
        <li key={i} className={item.checked !== null ? styles.task : undefined}>
          {item.checked !== null && (
            <input
              type="checkbox"
              className={styles.checkbox}
              checked={item.checked}
              readOnly
              disabled
              aria-label={item.checked ? t("common.markdown.done") : t("common.markdown.notDone")}
            />
          )}
          {renderBlocks(item.children, open, b.tight)}
        </li>
      ));
      return b.ordered ? (
        <ol key={key} start={b.start !== 1 ? b.start : undefined}>
          {items}
        </ol>
      ) : (
        <ul key={key}>{items}</ul>
      );
    }
    case "table":
      return (
        <div key={key} className={styles.tableWrap}>
          <table>
            <thead>
              <tr>
                {b.head.map((cell, c) => (
                  <th key={c} style={b.align[c] ? { textAlign: b.align[c] ?? undefined } : undefined}>
                    {renderInlines(cell, open)}
                  </th>
                ))}
              </tr>
            </thead>
            {b.rows.length > 0 && (
              <tbody>
                {b.rows.map((row, r) => (
                  <tr key={r}>
                    {row.map((cell, c) => (
                      <td key={c} style={b.align[c] ? { textAlign: b.align[c] ?? undefined } : undefined}>
                        {renderInlines(cell, open)}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            )}
          </table>
        </div>
      );
  }
}

function renderInlines(nodes: Inline[], open: Open): ReactNode[] {
  return nodes.map((n, i) => renderInline(n, i, open));
}

function renderInline(n: Inline, key: number, open: Open): ReactNode {
  switch (n.type) {
    case "text":
      return n.text;
    case "code":
      return <code key={key}>{n.text}</code>;
    case "em":
      return <em key={key}>{renderInlines(n.children, open)}</em>;
    case "strong":
      return <strong key={key}>{renderInlines(n.children, open)}</strong>;
    case "del":
      return <del key={key}>{renderInlines(n.children, open)}</del>;
    case "break":
      return <br key={key} />;
    case "link":
      return (
        <NoteLink key={key} target={n.target} title={n.title} open={open}>
          {/* A link inside a link cannot be reached or announced: an image link inside one is inert text (UI-11). */}
          {renderInlines(n.children, undefined)}
        </NoteLink>
      );
    case "image": {
      // Never an <img>: the image is not loaded, its alt text stands in.
      const label = n.alt !== "" ? n.alt : "image";
      const content = (
        <>
          <span className={styles.imageMark} aria-hidden="true">
            ▣
          </span>
          {label}
        </>
      );
      return (
        <span key={key} className={styles.image} data-image-url={n.target.url}>
          {n.target.kind === "external" ? (
            <NoteLink target={n.target} title={t("common.markdown.imageNotLoaded", { url: n.target.url })} open={open}>
              {content}
            </NoteLink>
          ) : (
            <span title={t("common.markdown.imageNotLoaded", { url: n.target.url })}>{content}</span>
          )}
        </span>
      );
    }
  }
}

function NoteLink({ target, title, open, children }: { target: LinkTarget; title: string; open: Open; children: ReactNode }) {
  const tooltip = title !== "" && title !== target.url ? `${title}\n${target.url}` : target.url;
  if (target.kind !== "external" || open === undefined) {
    return (
      <span className={styles.inertLink} title={target.kind === "external" ? tooltip : t("common.markdown.notOpened", { url: target.url })}>
        {children}
      </span>
    );
  }
  const activate = () => open(target.url);
  const onKeyDown = (e: KeyboardEvent<HTMLAnchorElement>) => {
    if (e.key === "Enter") {
      e.preventDefault();
      activate();
    }
  };
  return (
    <a
      className={styles.link}
      role="link"
      tabIndex={0}
      title={tooltip}
      data-url={target.url}
      onClick={(e) => {
        e.preventDefault();
        activate();
      }}
      onKeyDown={onKeyDown}
    >
      {children}
    </a>
  );
}
