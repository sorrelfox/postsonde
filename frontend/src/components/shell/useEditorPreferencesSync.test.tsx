import { afterEach, describe, expect, it } from "vitest";
import { act, render } from "@testing-library/react";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { currentEditorPreferences, resetEditorPreferencesForTests } from "../editor/preferences";
import { useEditorPreferencesSync } from "./useEditorPreferencesSync";

function Sync() {
  useEditorPreferencesSync();
  return null;
}

function renderSync(stores: Stores) {
  return render(
    <StoreProvider stores={stores}>
      <Sync />
    </StoreProvider>,
  );
}

afterEach(() => {
  resetEditorPreferencesForTests();
});

describe("useEditorPreferencesSync", () => {
  it("hands the store's editor settings to the editors, also when a hand edit changes them", () => {
    const stores = createStores();
    stores.settings.dispatch({ type: "settings/load", settings: { editorTabSize: 4 } });
    renderSync(stores);
    expect(currentEditorPreferences().editorTabSize).toBe(4);

    act(() => stores.settings.dispatch({ type: "settings/load", settings: { editorFontSize: 16, editorWordWrap: true } }));
    expect(currentEditorPreferences()).toMatchObject({ editorTabSize: 4, editorFontSize: 16, editorWordWrap: true });
    expect(document.documentElement.style.getPropertyValue("--fs-editor")).toBe("16px");
  });

  it("does not touch the editors for other settings", () => {
    const stores = createStores();
    renderSync(stores);
    const before = currentEditorPreferences();
    act(() => stores.settings.dispatch({ type: "settings/setTheme", theme: "dark" }));
    expect(currentEditorPreferences()).toBe(before);
  });
});
