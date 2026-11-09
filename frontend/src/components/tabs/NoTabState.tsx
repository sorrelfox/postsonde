import { openRequest } from "../../commands/tabs";
import { useT } from "../../i18n/react";
import { shallowEqual, useCollection, useStores, type RequestNode } from "../../stores";
import { IconChevron, IconPlus, IconTrash } from "../common/icons";
import styles from "./NoTabState.module.css";
import { tabBadge } from "./tabBadge";
import { useScratchActions } from "./useScratchActions";

/**
 * The request pane with no tab open: New scratch request (an HTTP one, or
 * any kind from the menu next to it) and the project's scratch requests
 * (COL-06), each with Open, Save to… and Delete. This list, the tabs and the
 * tab bar's "+" are where scratch requests are found; they are not in the
 * Collections tree.
 */
export function NoTabState() {
  const t = useT();
  const stores = useStores();
  const scratch = useScratchActions();
  const nodes = useCollection(
    (s) => s.scratchIds.map((id) => s.nodes[id]).filter((n): n is RequestNode => n?.kind === "request"),
    shallowEqual,
  );

  return (
    <div className={styles.panel}>
      <div className={styles.intro}>
        <p className={styles.title}>{t("tabs.noTab.title")}</p>
        <p className={styles.body}>{t("tabs.noTab.body")}</p>
        <div className={styles.actions}>
          <button type="button" className={styles.primary} onClick={() => scratch.create("http")}>
            <IconPlus size={14} />
            {t("tabs.noTab.newScratch")}
          </button>
          <button
            type="button"
            className={styles.more}
            aria-label={t("tabs.noTab.newScratchOther")}
            aria-haspopup="menu"
            title={t("tabs.noTab.otherTypes")}
            onClick={(e) => {
              const rect = e.currentTarget.getBoundingClientRect();
              scratch.openNewMenu(rect.left, rect.bottom);
            }}
          >
            <IconChevron direction="down" size={12} strokeWidth={2.2} />
          </button>
        </div>
      </div>

      {nodes.length > 0 && (
        <section className={styles.list} aria-labelledby="postsonde-scratch-heading">
          <h2 className={styles.heading} id="postsonde-scratch-heading">
            {t("tabs.noTab.list")}
          </h2>
          <ul className={styles.items}>
            {nodes.map((node) => {
              const badge = tabBadge(node);
              return (
                <li key={node.id} className={styles.item}>
                  <button
                    type="button"
                    className={styles.open}
                    aria-label={t("tabs.noTab.open", { name: node.name })}
                    onClick={() => openRequest(stores, node.id)}
                  >
                    <span className={styles.badge} data-tone={badge.tone}>
                      {badge.label}
                    </span>
                    <span className={styles.name}>{node.name}</span>
                  </button>
                  <button
                    type="button"
                    className={styles.action}
                    disabled={!scratch.canSave}
                    aria-label={t("tabs.noTab.saveToLabel", { name: node.name })}
                    onClick={() => scratch.saveTo(node.id)}
                  >
                    {t("tabs.noTab.saveTo")}
                  </button>
                  <button
                    type="button"
                    className={styles.icon}
                    aria-label={t("tabs.noTab.deleteLabel", { name: node.name })}
                    title={t("tabs.noTab.delete")}
                    onClick={() => scratch.remove(node.id)}
                  >
                    <IconTrash size={13} />
                  </button>
                </li>
              );
            })}
          </ul>
        </section>
      )}
      {scratch.elements}
    </div>
  );
}
