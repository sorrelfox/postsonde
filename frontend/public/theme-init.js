// Sets data-theme on <html> before first paint so the wrong theme never
// flashes. index.html loads it as a classic, render-blocking script: the
// Content Security Policy (script-src 'self', docs/privacy.md) forbids
// inline scripts, so it lives in public/ and Vite copies it into dist/ as is.
// Mirrors resolveTheme() in src/styles/theme.ts (same storage key).
//
// A custom theme (UI-05) cached by src/styles/customTheme.ts is applied too:
// its base wins over the preference, and each colour is set with
// style.setProperty, only for known token names (the THEME_TOKENS pattern)
// and normalised hex values, as customTheme.ts checks them.
(function () {
  var pref = "system";
  try {
    pref = localStorage.getItem("postsonde.theme") || "system";
  } catch (e) {}
  var theme =
    pref === "dark" || pref === "light"
      ? pref
      : window.matchMedia &&
          window.matchMedia("(prefers-color-scheme: dark)").matches
        ? "dark"
        : "light";
  var root = document.documentElement;
  try {
    var custom = JSON.parse(localStorage.getItem("postsonde.customTheme") || "null");
    if (custom && (custom.base === "dark" || custom.base === "light") && custom.vars instanceof Array) {
      var name = /^--(bg[0-3]|line2?|text[23]?|accent(-ink|-dim)?|m-(get|post|put|patch|del|gql|dir|ws)|ok|warn|bad|sel|scrim|tk-(kw|field|arg|var|str|num|type|dir|punct|cm|key|bool|plain|err))$/;
      var value = /^#[0-9a-f]{6}([0-9a-f]{2})?$/;
      for (var i = 0; i < custom.vars.length; i++) {
        var v = custom.vars[i];
        if (v && name.test(v.name) && value.test(v.value)) root.style.setProperty(v.name, v.value);
      }
      theme = custom.base;
    }
  } catch (e) {}
  root.setAttribute("data-theme", theme);
})();
