import { useState, type MouseEvent, type ReactNode } from "react";
import type { QuickAssertion } from "../../commands/quickAssert";
import { useT } from "../../i18n/react";
import { ContextMenu } from "../common/ContextMenu";

/** One item of the quick-assertion menu: its label and the assertion it adds. */
export interface QuickAssertItem {
  readonly id: string;
  readonly label: string;
  readonly assertion: QuickAssertion;
}

export interface QuickAssertMenu {
  /** Whether the view can add assertions (it was given onQuickAssert). */
  readonly enabled: boolean;
  /** Opens the menu at the event with `items`, instead of the browser's. */
  open: (e: MouseEvent, items: readonly QuickAssertItem[]) => void;
  /** The menu while it is open, to render. */
  readonly element: ReactNode;
}

/**
 * The context menu of quick assertions (TEST-04) on a value of the
 * response: right-click (or the context-menu key on the focused value)
 * offers "Assert … equals this value" and its kin, and the chosen one goes
 * to `onQuickAssert`. Without it the browser's menu is left alone.
 */
export function useQuickAssertMenu(onQuickAssert: ((a: QuickAssertion) => void) | undefined): QuickAssertMenu {
  const t = useT();
  const [menu, setMenu] = useState<{ x: number; y: number; items: readonly QuickAssertItem[] } | null>(null);
  const open = (e: MouseEvent, items: readonly QuickAssertItem[]) => {
    if (onQuickAssert === undefined || items.length === 0) return;
    e.preventDefault();
    let x = e.clientX;
    let y = e.clientY;
    if (x === 0 && y === 0 && e.currentTarget instanceof Element) {
      // Opened from the keyboard: under the element.
      const rect = e.currentTarget.getBoundingClientRect();
      x = rect.left;
      y = rect.bottom;
    }
    setMenu({ x, y, items });
  };
  const element =
    menu === null || onQuickAssert === undefined ? null : (
      <ContextMenu
        label={t("response.quickAssert.menu")}
        x={menu.x}
        y={menu.y}
        items={menu.items.map((item) => ({ id: item.id, label: item.label, onSelect: () => onQuickAssert(item.assertion) }))}
        onClose={() => setMenu(null)}
      />
    );
  return { enabled: onQuickAssert !== undefined, open, element };
}
