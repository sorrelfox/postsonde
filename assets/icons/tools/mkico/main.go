// Command mkico packs square PNG images into a Windows .ico file.
//
// Each PNG is stored as-is (PNG-compressed ICO entries, supported since
// Windows Vista). Sizes must be square and between 1 and 256 px.
//
// Usage:
//
//	go run ./tools/mkico -o postsonde.ico png/postsonde-16.png png/postsonde-32.png ...
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image/png"
	"os"
	"sort"
)

type entry struct {
	size int
	data []byte
}

func main() {
	out := flag.String("o", "", "output .ico path")
	flag.Parse()
	if *out == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: mkico -o out.ico in1.png [in2.png ...]")
		os.Exit(2)
	}
	if err := run(*out, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "mkico:", err)
		os.Exit(1)
	}
}

func run(out string, inputs []string) error {
	entries := make([]entry, 0, len(inputs))
	for _, path := range inputs {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if cfg.Width != cfg.Height || cfg.Width < 1 || cfg.Width > 256 {
			return fmt.Errorf("%s: %dx%d is not a square size from 1 to 256", path, cfg.Width, cfg.Height)
		}
		entries = append(entries, entry{size: cfg.Width, data: data})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].size < entries[j].size })

	var buf bytes.Buffer
	le := binary.LittleEndian
	// ICONDIR: reserved, type (1 = icon), image count.
	binary.Write(&buf, le, [3]uint16{0, 1, uint16(len(entries))})
	offset := uint32(6 + 16*len(entries))
	for _, e := range entries {
		dim := uint8(e.size)
		if e.size == 256 {
			dim = 0 // 0 means 256 in the ICONDIRENTRY
		}
		// ICONDIRENTRY: width, height, palette colours, reserved,
		// colour planes, bits per pixel, data size, data offset.
		buf.Write([]byte{dim, dim, 0, 0})
		binary.Write(&buf, le, [2]uint16{1, 32})
		binary.Write(&buf, le, [2]uint32{uint32(len(e.data)), offset})
		offset += uint32(len(e.data))
	}
	for _, e := range entries {
		buf.Write(e.data)
	}
	return os.WriteFile(out, buf.Bytes(), 0o644)
}
