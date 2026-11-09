# PostSonde icons

The app icon is the signal mark from the title bar: a dot with two wave arcs on
each side. It sits on a rounded square in the app's dark palette
(accent `#4fd1a5`, background `#1f242c` to `#0e1013`, edge `#2f3641`).

## Sources

| File                    | Use                                                        |
| ----------------------- | ---------------------------------------------------------- |
| `postsonde.svg`         | Master icon, used for 48 px and up                         |
| `postsonde-small.svg`   | Small-size version with heavier strokes, used for 24–32 px |
| `postsonde-16.svg`      | 16 px version with the dot and one arc per side            |
| `tools/mkico/main.go`   | Stdlib Go tool that packs PNGs into a `.ico`               |
| `generate.sh`           | Rebuilds every generated file below                        |

## Generated files (committed)

- `png/postsonde-{16,24,32,48,64,128,256,512,1024}.png`: full-bleed PNGs
- `postsonde.icns`: macOS, 16–1024 px incl. @2x. The artwork follows the
  Apple grid (an 824/1024 body with a transparent margin)
- `postsonde.ico`: Windows, 16, 24, 32, 48, 64, 128 and 256 px (PNG entries)
- `linux/postsonde.png`: 512 px desktop icon
- `linux/hicolor/<size>/apps/postsonde.png` and `linux/hicolor/scalable/apps/postsonde.svg`:
  a hicolor theme tree made of plain copies, with no symlinks

The Wails scaffold copies what it needs from here into its `build/` directory.

The Windows installer's welcome and finish pages show
`build/windows/nsis/wizard.bmp` (164 × 314, 24-bit BMP): the 128 px
PNG on the background gradient. `generate.sh` doesn't make it; after an icon
change, rebuild it with ImageMagick 7 from the repository root:

```sh
magick -size 164x314 gradient:'#1f242c'-'#0e1013' \
  assets/icons/png/postsonde-128.png -geometry +18+64 -composite \
  -alpha off -type TrueColor BMP3:build/windows/nsis/wizard.bmp
```

## Regenerate

Edit the SVG sources, then run:

```sh
assets/icons/generate.sh
```

The script needs `rsvg-convert` (from `brew install librsvg`), `iconutil` (built
into macOS) and `go`. The PNG, ICO and Linux steps would also work on Linux,
but the `.icns` step needs macOS. Check the 16 and 32 px PNGs after any design
change, since those are the sizes that break first.
