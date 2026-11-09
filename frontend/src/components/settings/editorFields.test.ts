import { describe, expect, it } from "vitest";
import { isValidFontFamily } from "../../stores";
import { fontOptions, fontOptionsFor, parseFontSize, tabSizeOptionsFor } from "./editorFields";

describe("editor fields", () => {
  it("offers only font names the settings file accepts", () => {
    for (const o of fontOptions()) expect(isValidFontFamily(o.value)).toBe(true);
    expect(fontOptions()[0]).toEqual({ value: "JetBrains Mono", label: "JetBrains Mono (built in)" });
  });

  it("adds a font set by hand to the menu", () => {
    expect(fontOptionsFor("Menlo")).toEqual(fontOptions());
    expect(fontOptionsFor("Iosevka").slice(-1)[0]).toEqual({ value: "Iosevka", label: "Iosevka (from settings.json)" });
  });

  it("adds a tab size set by hand to the buttons, in order", () => {
    expect(tabSizeOptionsFor(4)).toEqual([2, 4, 8]);
    expect(tabSizeOptionsFor(3)).toEqual([2, 3, 4, 8]);
    expect(tabSizeOptionsFor(1)).toEqual([1, 2, 4, 8]);
  });

  it("parses a whole number of pixels in the bounds", () => {
    expect(parseFontSize("14")).toEqual({ ok: true, px: 14 });
    expect(parseFontSize(" 16px ")).toEqual({ ok: true, px: 16 });
    expect(parseFontSize("8")).toEqual({ ok: true, px: 8 });
    expect(parseFontSize("32")).toEqual({ ok: true, px: 32 });
    for (const bad of ["", "7", "33", "12.5", "-12", "big"]) {
      expect(parseFontSize(bad)).toEqual({ ok: false, error: "Use a whole number of pixels from 8 to 32." });
    }
  });
});
