# Instrument Sans: source

- **Upstream:** <https://github.com/Instrument/instrument-sans> (branch `master`)
- **Commit:** `7fa22308a3d0c94ee2b3cd537a1196b65db34a3e` (2023-06-14). The
  repo has no release tags. google/fonts (`ofl/instrumentsans/METADATA.pb`)
  builds from the same commit.
- **Font version:** 1.000 (name table: `Version 1.000; ttfautohint (v1.8.4.7-5d5b);gftools[0.9.28]`)
- **Downloaded:** 2026-09-15
- **License:** SIL Open Font License 1.1, see `OFL.txt` (from the repo root at
  the same commit).

## Files

Copied unmodified from `fonts/webfonts/` at the commit above. There was no
conversion.

| File | Weight | Bytes | SHA-256 |
|---|---|---|---|
| `InstrumentSans-Regular.woff2` | 400 | 34628 | `f28af62faa9eec1e5482cf6e2a3e06bc865fa2fa6937bd56c14d2be23c9b4c46` |
| `InstrumentSans-Medium.woff2` | 500 | 35580 | `65d25fc111c40fd6b481d9db860af365730752228dfd8a246e79648d8a01f02a` |
| `InstrumentSans-SemiBold.woff2` | 600 | 35812 | `04235f235483213906d69cafb7a87ee197adaffacb081c8a7f809d00f9b353cd` |
| `OFL.txt` | | | `9e27a72ed30eb49a08678f6a5d6ed98ec7ba5368f541637ee0683ec9134ef966` |

Download URL pattern:
`https://raw.githubusercontent.com/Instrument/instrument-sans/7fa22308a3d0c94ee2b3cd537a1196b65db34a3e/fonts/webfonts/<file>`

## Static, not variable

Upstream also ships a variable `InstrumentSans[wdth,wght].woff2` (88784
bytes, which is 17 KB smaller than the three static files together). We use
the static files anyway. They are the exact weights the mockup uses, they need
no `font-weight` range, and they don't carry the width axis we don't use.
Italics are left out because the mockup doesn't use them.
