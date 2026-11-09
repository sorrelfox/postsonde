import type { ReactNode } from "react";

/**
 * `text` with the characters at `hits` (ascending indices, from
 * lib/fuzzy.ts) wrapped in <mark className={className}>, one mark per run
 * of consecutive characters. Used by the quick switcher and the tree filter.
 */
export function Highlighted({ text, hits, className }: { text: string; hits: readonly number[]; className?: string }) {
  if (hits.length === 0) return <>{text}</>;
  const parts: ReactNode[] = [];
  let from = 0;
  let i = 0;
  while (i < hits.length) {
    let j = i;
    while (j + 1 < hits.length && hits[j + 1] === hits[j] + 1) j++;
    const start = hits[i];
    const end = hits[j] + 1;
    if (start > from) parts.push(text.slice(from, start));
    parts.push(
      <mark key={start} className={className}>
        {text.slice(start, end)}
      </mark>,
    );
    from = end;
    i = j + 1;
  }
  if (from < text.length) parts.push(text.slice(from));
  return <>{parts}</>;
}
