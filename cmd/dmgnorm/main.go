// Command dmgnorm makes the macOS .dmg reproducible
// (docs/reproducible-builds.md). scripts/package-macos.sh runs it; it is a
// build tool, not part of the app.
//
// hdiutil writes the build time and fresh random UUIDs into every image,
// so two builds of the same PostSonde.app never give the same .dmg. The
// script builds the image as a bare HFS+ volume (no partition map, so no
// GPT disk and partition GUIDs) and then:
//
//	dmgnorm hfs  -epoch <SOURCE_DATE_EPOCH> -seed <text> <raw image>
//	dmgnorm udif -seed <text> <.dmg>
//
// hfs rewrites, in place, every date in the HFS+ volume headers (primary
// and alternate) and in the catalog's file and folder records to the
// epoch, and the volume UUID (finderInfo[6..7]) to one derived from the
// seed. udif sets the SegmentID UUID in the trailer (the "koly" block) of
// a UDIF image, which hdiutil convert fills at random and no checksum
// covers, to one derived from the seed.
//
// Standard library only; works on any OS.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
)

// hfsEpochOffset is the number of seconds from the HFS+ epoch (1904-01-01
// UTC) to the Unix epoch.
const hfsEpochOffset = 2082844800

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "dmgnorm:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	usage := errors.New("usage: dmgnorm hfs -epoch <unix seconds> -seed <text> <raw image> | dmgnorm udif -seed <text> <.dmg>")
	if len(args) < 1 || (args[0] != "hfs" && args[0] != "udif") {
		return usage
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	epoch := fs.String("epoch", "", "SOURCE_DATE_EPOCH")
	seed := fs.String("seed", "", "text the UUIDs are derived from")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 || *seed == "" {
		return usage
	}
	path := fs.Arg(0)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()

	switch args[0] {
	case "hfs":
		sde, err := strconv.ParseInt(*epoch, 10, 64)
		if err != nil || sde < 0 || sde+hfsEpochOffset > 0xFFFFFFFF {
			return fmt.Errorf("invalid -epoch %q", *epoch)
		}
		st, err := f.Stat()
		if err != nil {
			return err
		}
		n, err := normalizeHFS(f, st.Size(), uint32(sde+hfsEpochOffset), derive(*seed, "hfs-volume-uuid")[:8])
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fmt.Fprintf(out, "dmgnorm: %s: set %d dates and the volume UUID\n", path, n)
	case "udif":
		if *epoch != "" {
			return usage
		}
		if err := normalizeUDIF(f, derive(*seed, "udif-segment-id")[:16]); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		fmt.Fprintf(out, "dmgnorm: %s: set the UDIF SegmentID\n", path)
	default:
		return usage
	}
	return f.Close()
}

// derive returns 32 bytes for a fixed UUID: SHA-256 of purpose and seed.
func derive(seed, purpose string) []byte {
	sum := sha256.Sum256([]byte("postsonde-dmgnorm\x00" + purpose + "\x00" + seed))
	return sum[:]
}

// HFS+ volume header layout (Apple TN1150). The header is 512 bytes at
// offset 1024 of the volume; the alternate copy is 1024 bytes before the
// end of the volume.
const (
	vhOffset         = 1024
	vhSize           = 512
	vhCreateDate     = 0x10
	vhModifyDate     = 0x14
	vhBackupDate     = 0x18
	vhCheckedDate    = 0x1C
	vhBlockSize      = 0x28
	vhFinderInfoUUID = 0x50 + 6*4 // finderInfo[6] and [7]: the volume UUID
	vhCatalogFork    = 0x110
)

type rw interface {
	io.ReaderAt
	io.WriterAt
}

// normalizeHFS patches the HFS+ volume that fills the image of the given
// size (no partition map).
func normalizeHFS(f rw, size int64, date uint32, uuid []byte) (int, error) {
	vh := make([]byte, vhSize)
	if _, err := f.ReadAt(vh, vhOffset); err != nil {
		return 0, fmt.Errorf("reading the volume header: %w", err)
	}
	if sig := string(vh[0:2]); sig != "H+" && sig != "HX" {
		return 0, fmt.Errorf("no HFS+ volume header at offset %d (signature %q); build the image with -layout NONE -fs HFS+", vhOffset, sig)
	}
	blockSize := int64(binary.BigEndian.Uint32(vh[vhBlockSize:]))
	if blockSize < 512 || blockSize&(blockSize-1) != 0 {
		return 0, fmt.Errorf("invalid block size %d", blockSize)
	}

	n, err := normalizeCatalog(f, vh, blockSize, date)
	if err != nil {
		return 0, err
	}

	// Primary and alternate volume header. The volume header's createDate
	// is in local time, the others in UTC; all become the epoch. The
	// alternate header is 1024 bytes before the end of the volume (the whole
	// image), which can lie past the last allocation block.
	if size < 3*1024 {
		return 0, fmt.Errorf("image too small (%d bytes)", size)
	}
	altOffset := size - 1024
	for _, off := range []int64{vhOffset, altOffset} {
		h := make([]byte, vhSize)
		if _, err := f.ReadAt(h, off); err != nil {
			return 0, fmt.Errorf("reading the volume header at %d: %w", off, err)
		}
		if sig := string(h[0:2]); sig != "H+" && sig != "HX" {
			return 0, fmt.Errorf("no HFS+ volume header at offset %d", off)
		}
		for _, d := range []int{vhCreateDate, vhModifyDate, vhBackupDate, vhCheckedDate} {
			n += setDate(h[d:], date)
		}
		copy(h[vhFinderInfoUUID:vhFinderInfoUUID+8], uuid)
		if _, err := f.WriteAt(h, off); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// setDate overwrites a non-zero big-endian date; zero means "never" (an
// unset backup date) and stays.
func setDate(b []byte, date uint32) int {
	if binary.BigEndian.Uint32(b) == 0 {
		return 0
	}
	binary.BigEndian.PutUint32(b, date)
	return 1
}

// catalogFile reads and writes the catalog B-tree through the extents of
// its fork in the volume header. Images this small never need the
// extents overflow file; a catalog that would is an error.
type catalogFile struct {
	f       rw
	extents [][2]int64 // byte offset, byte length
	size    int64
}

func newCatalogFile(f rw, vh []byte, blockSize int64) (*catalogFile, error) {
	fork := vh[vhCatalogFork:]
	c := &catalogFile{f: f, size: int64(binary.BigEndian.Uint64(fork[0:]))}
	totalBlocks := int64(binary.BigEndian.Uint32(fork[12:]))
	var covered int64
	for i := 0; i < 8; i++ {
		start := int64(binary.BigEndian.Uint32(fork[16+8*i:]))
		count := int64(binary.BigEndian.Uint32(fork[20+8*i:]))
		if count == 0 {
			break
		}
		c.extents = append(c.extents, [2]int64{start * blockSize, count * blockSize})
		covered += count
	}
	if covered != totalBlocks || covered*blockSize < c.size {
		return nil, fmt.Errorf("the catalog file spills into the extents overflow file (%d of %d blocks in the volume header)", covered, totalBlocks)
	}
	return c, nil
}

// at maps a catalog offset to a volume offset; a node never straddles
// extents because extents are whole allocation blocks and nodes divide them.
func (c *catalogFile) at(off, n int64) (int64, error) {
	for _, e := range c.extents {
		if off < e[1] {
			if off+n > e[1] {
				return 0, fmt.Errorf("catalog node at %d straddles an extent", off)
			}
			return e[0] + off, nil
		}
		off -= e[1]
	}
	return 0, fmt.Errorf("catalog offset %d beyond the catalog file", off)
}

func (c *catalogFile) readNode(off int64, node []byte) (int64, error) {
	v, err := c.at(off, int64(len(node)))
	if err != nil {
		return 0, err
	}
	_, err = c.f.ReadAt(node, v)
	return v, err
}

// Catalog record types and the offsets of their five dates (createDate,
// contentModDate, attributeModDate, accessDate, backupDate) and of the
// "date added" in the extended Finder info (FndrExtendedFileInfo /
// FndrExtendedDirInfo date_added, Unix seconds), the same in
// HFSPlusCatalogFolder and HFSPlusCatalogFile.
const (
	recFolder      = 1
	recFile        = 2
	recDatesOffset = 12
	recDates       = 5
	recDateAdded   = 64 + 4
	nodeKindLeaf   = -1
)

func normalizeCatalog(f rw, vh []byte, blockSize int64, date uint32) (int, error) {
	c, err := newCatalogFile(f, vh, blockSize)
	if err != nil {
		return 0, err
	}
	// The header node (node 0): a 14-byte node descriptor, then the header
	// record with firstLeafNode at +10 and nodeSize at +18.
	head := make([]byte, 512)
	if _, err := c.readNode(0, head); err != nil {
		return 0, fmt.Errorf("reading the catalog header node: %w", err)
	}
	if kind := int8(head[8]); kind != 1 {
		return 0, fmt.Errorf("catalog node 0 is not a header node (kind %d)", kind)
	}
	firstLeaf := binary.BigEndian.Uint32(head[14+10:])
	nodeSize := int64(binary.BigEndian.Uint16(head[14+18:]))
	if nodeSize < 512 || nodeSize&(nodeSize-1) != 0 {
		return 0, fmt.Errorf("invalid catalog node size %d", nodeSize)
	}

	n := 0
	node := make([]byte, nodeSize)
	seen := map[uint32]bool{}
	for id := firstLeaf; id != 0; id = binary.BigEndian.Uint32(node[0:]) {
		if seen[id] {
			return 0, fmt.Errorf("catalog leaf chain loops at node %d", id)
		}
		seen[id] = true
		v, err := c.readNode(int64(id)*nodeSize, node)
		if err != nil {
			return 0, fmt.Errorf("reading catalog node %d: %w", id, err)
		}
		if kind := int8(node[8]); kind != nodeKindLeaf {
			return 0, fmt.Errorf("catalog node %d in the leaf chain is not a leaf (kind %d)", id, kind)
		}
		records := int(binary.BigEndian.Uint16(node[10:]))
		for i := 0; i < records; i++ {
			// Record offsets are stored backwards from the end of the node.
			off := int64(binary.BigEndian.Uint16(node[nodeSize-2*int64(i+1):]))
			if off+2 > nodeSize {
				return 0, fmt.Errorf("catalog node %d record %d out of range", id, i)
			}
			keyLen := int64(binary.BigEndian.Uint16(node[off:]))
			data := off + 2 + keyLen
			if data+2 > nodeSize {
				return 0, fmt.Errorf("catalog node %d record %d out of range", id, i)
			}
			switch binary.BigEndian.Uint16(node[data:]) {
			case recFolder, recFile:
				if data+recDateAdded+4 > nodeSize {
					return 0, fmt.Errorf("catalog node %d record %d truncated", id, i)
				}
				for d := int64(0); d < recDates; d++ {
					n += setDate(node[data+recDatesOffset+4*d:], date)
				}
				n += setDate(node[data+recDateAdded:], date-hfsEpochOffset)
			}
		}
		if _, err := f.WriteAt(node, v); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// UDIF trailer ("koly" block, the last 512 bytes of a .dmg): the 16-byte
// SegmentID is at offset 64.
const (
	kolySize      = 512
	kolySegmentID = 64
)

type rws interface {
	rw
	Stat() (os.FileInfo, error)
}

func normalizeUDIF(f rws, id []byte) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() < kolySize {
		return errors.New("too small for a UDIF image")
	}
	off := st.Size() - kolySize
	koly := make([]byte, kolySize)
	if _, err := f.ReadAt(koly, off); err != nil {
		return err
	}
	if string(koly[0:4]) != "koly" {
		return errors.New("no UDIF trailer (koly) at the end of the file")
	}
	copy(koly[kolySegmentID:kolySegmentID+16], id)
	_, err = f.WriteAt(koly, off)
	return err
}
