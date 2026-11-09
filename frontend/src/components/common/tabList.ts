// Keyboard support for a WAI-ARIA tab list (UI-11), shared by the tab
// strips that have no handler of their own: put `onKeyDown={onTabListKeyDown}`
// on the element with role="tablist". See docs/frontend.md#accessibility.
import type { KeyboardEvent } from "react";

/** The tabs of `list`: its role="tab" descendants that are not in a nested tab list and not disabled. */
function tabsOf(list: HTMLElement): HTMLElement[] {
  return Array.from(list.querySelectorAll<HTMLElement>("[role='tab']")).filter(
    (tab) =>
      tab.closest("[role='tablist']") === list &&
      tab.getAttribute("aria-disabled") !== "true" &&
      !(tab as HTMLButtonElement).disabled,
  );
}

/**
 * Roving focus with selection following focus: Left/Right (Up/Down with
 * aria-orientation="vertical") move to the previous or next tab, wrapping,
 * Home and End to the first and last. The tab that gets focus is clicked,
 * so the tab strip's own onClick selects it. Keys with Alt, Ctrl or Meta,
 * and keys a child already handled, are left alone.
 */
export function onTabListKeyDown(e: KeyboardEvent<HTMLElement>): void {
  if (e.defaultPrevented || e.altKey || e.ctrlKey || e.metaKey) return;
  const list = e.currentTarget;
  const target = e.target instanceof HTMLElement ? e.target.closest<HTMLElement>("[role='tab']") : null;
  if (target === null || target.closest("[role='tablist']") !== list) return;
  const vertical = list.getAttribute("aria-orientation") === "vertical";
  const prev = vertical ? "ArrowUp" : "ArrowLeft";
  const next = vertical ? "ArrowDown" : "ArrowRight";
  const tabs = tabsOf(list);
  const at = tabs.indexOf(target);
  if (tabs.length === 0 || at === -1) return;
  let to: number;
  if (e.key === prev) to = (at - 1 + tabs.length) % tabs.length;
  else if (e.key === next) to = (at + 1) % tabs.length;
  else if (e.key === "Home") to = 0;
  else if (e.key === "End") to = tabs.length - 1;
  else return;
  e.preventDefault();
  const tab = tabs[to];
  if (tab === undefined) return;
  tab.focus();
  if (tab.getAttribute("aria-selected") !== "true") tab.click();
}
