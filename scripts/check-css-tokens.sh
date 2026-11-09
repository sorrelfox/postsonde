#!/usr/bin/env bash
# Fails if any stylesheet or TS/TSX source under frontend/src hard-codes a
# colour instead of using a design token. Literal colours belong in
# frontend/src/styles/tokens.css only.
#
# A pragmatic grep, not a CSS parser. It flags:
#   - hex colours (#abc, #aabbcc, #aabbccdd) after ':', whitespace, a quote,
#     '(' or ','
#   - rgb()/rgba()/hsl()/hsla()/hwb()/lab()/lch()/oklab()/oklch()
#   - CSS named colours as the value of a colour property, in CSS
#     (color:, background:, border-color: …) or in React style objects
#     (color:, backgroundColor:, borderColor: …)
# transparent, currentColor and inherit are allowed.
#
# Usage: scripts/check-css-tokens.sh   (from anywhere; exits 1 on findings)

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
src="$root/frontend/src"
allow="$src/styles/tokens.css"

if [[ ! -d "$src" ]]; then
  echo "check-css-tokens: $src not found" >&2
  exit 2
fi

files=()
while IFS= read -r -d '' f; do
  [[ "$f" == "$allow" ]] && continue
  files+=("$f")
done < <(find "$src" -type f \( -name '*.css' -o -name '*.ts' -o -name '*.tsx' \) -print0 | sort -z)

if [[ ${#files[@]} -eq 0 ]]; then
  echo "check-css-tokens: no files to check"
  exit 0
fi

hex='(^|[[:space:]:"'"'"'(,])#([0-9a-fA-F]{3,4}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})([^0-9a-zA-Z_-]|$)'
func='(^|[^a-zA-Z0-9_-])(rgba?|hsla?|hwb|lab|lch|oklab|oklch)[[:space:]]*\('

# Colour-bearing properties, CSS (kebab-case) and React style objects
# (camelCase), followed by ':' and a value on the same line.
props='(^|[^a-zA-Z0-9_-])(color|background|background-color|backgroundColor|border|border-(top|right|bottom|left|block|inline)(-(start|end))?(-color)?|border-color|borderColor|border(Top|Right|Bottom|Left)(Color)?|outline|outline-color|outlineColor|box-shadow|boxShadow|text-shadow|textShadow|text-decoration(-color)?|textDecoration(Color)?|caret-color|caretColor|accent-color|accentColor|column-rule(-color)?|fill|stroke|stop-color|stopColor|flood-color|lighting-color|scrollbar-color|scrollbarColor)[[:space:]]*:'

# CSS named colours (CSS Color 4), minus the allowed keywords.
named='aliceblue|antiquewhite|aqua|aquamarine|azure|beige|bisque|black|blanchedalmond|blue|blueviolet|brown|burlywood|cadetblue|chartreuse|chocolate|coral|cornflowerblue|cornsilk|crimson|cyan|darkblue|darkcyan|darkgoldenrod|darkgray|darkgreen|darkgrey|darkkhaki|darkmagenta|darkolivegreen|darkorange|darkorchid|darkred|darksalmon|darkseagreen|darkslateblue|darkslategray|darkslategrey|darkturquoise|darkviolet|deeppink|deepskyblue|dimgray|dimgrey|dodgerblue|firebrick|floralwhite|forestgreen|fuchsia|gainsboro|ghostwhite|gold|goldenrod|gray|green|greenyellow|grey|honeydew|hotpink|indianred|indigo|ivory|khaki|lavender|lavenderblush|lawngreen|lemonchiffon|lightblue|lightcoral|lightcyan|lightgoldenrodyellow|lightgray|lightgreen|lightgrey|lightpink|lightsalmon|lightseagreen|lightskyblue|lightslategray|lightslategrey|lightsteelblue|lightyellow|lime|limegreen|linen|magenta|maroon|mediumaquamarine|mediumblue|mediumorchid|mediumpurple|mediumseagreen|mediumslateblue|mediumspringgreen|mediumturquoise|mediumvioletred|midnightblue|mintcream|mistyrose|moccasin|navajowhite|navy|oldlace|olive|olivedrab|orange|orangered|orchid|palegoldenrod|palegreen|paleturquoise|palevioletred|papayawhip|peachpuff|peru|pink|plum|powderblue|purple|rebeccapurple|red|rosybrown|royalblue|saddlebrown|salmon|sandybrown|seagreen|seashell|sienna|silver|skyblue|slateblue|slategray|slategrey|snow|springgreen|steelblue|tan|teal|thistle|tomato|turquoise|violet|wheat|white|whitesmoke|yellow|yellowgreen'
named_value="${props}[^;{}]*(^|[^a-zA-Z0-9_-])(${named})([^a-zA-Z0-9_-]|$)"

status=0
report() {
  local label="$1" pattern="$2" out
  # Drop var(--token) references first so token names such as --tan or
  # --m-red can never look like a colour. Then match per line.
  out="$(
    for f in "${files[@]}"; do
      awk -v file="${f#"$root"/}" '{ print file ":" NR ":" $0 }' "$f"
    done | sed -E 's/var\(--[A-Za-z0-9_-]+\)/var()/g' |
      grep -E -- "^[^:]+:[0-9]+:.*${pattern}" || true
  )"
  if [[ -n "$out" ]]; then
    echo "check-css-tokens: $label (use a token from frontend/src/styles/tokens.css):" >&2
    echo "$out" >&2
    status=1
  fi
}

report "hex colour" "$hex"
report "colour function" "$func"
report "named colour" "$named_value"

if [[ $status -eq 0 ]]; then
  echo "check-css-tokens: OK (${#files[@]} files, no hard-coded colours outside tokens.css)"
fi
exit $status
