package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testBlockSize = 4096
	testNodeSize  = 4096
	testImageSize = 16 * testBlockSize
	testCatalog   = 4 * testBlockSize // the catalog's first allocation block
)

// testImage builds a minimal bare HFS+ volume: both volume headers and a
// two-node catalog (header node, one leaf with a folder and a file
// record), every date set to the given value except the backup dates,
// which stay 0 ("never").
func testImage(t *testing.T, date uint32) string {
	t.Helper()
	img := make([]byte, testImageSize)
	be := binary.BigEndian

	vh := make([]byte, vhSize)
	copy(vh, "H+")
	be.PutUint16(vh[2:], 4)
	be.PutUint32(vh[vhCreateDate:], date)
	be.PutUint32(vh[vhModifyDate:], date)
	be.PutUint32(vh[vhCheckedDate:], date)
	be.PutUint32(vh[vhBlockSize:], testBlockSize)
	be.PutUint32(vh[0x2C:], testImageSize/testBlockSize)
	copy(vh[vhFinderInfoUUID:], "RANDOMID")
	fork := vh[vhCatalogFork:]
	be.PutUint64(fork[0:], 2*testNodeSize)
	be.PutUint32(fork[12:], 2)
	be.PutUint32(fork[16:], testCatalog/testBlockSize)
	be.PutUint32(fork[20:], 2)
	copy(img[vhOffset:], vh)
	copy(img[testImageSize-1024:], vh)

	head := img[testCatalog:]
	head[8] = 1 // header node
	be.PutUint32(head[14+10:], 1)
	be.PutUint16(head[14+18:], testNodeSize)

	leaf := img[testCatalog+testNodeSize : testCatalog+2*testNodeSize]
	leaf[8] = 0xFF // leaf node (-1)
	be.PutUint16(leaf[10:], 2)
	off := 14
	for i, typ := range []uint16{recFolder, recFile} {
		be.PutUint16(leaf[testNodeSize-2*(i+1):], uint16(off))
		be.PutUint16(leaf[off:], 6) // key: parentID + empty name
		data := off + 2 + 6
		be.PutUint16(leaf[data:], typ)
		for d := 0; d < recDates-1; d++ { // backupDate (the 5th) stays 0
			be.PutUint32(leaf[data+recDatesOffset+4*d:], date)
		}
		be.PutUint32(leaf[data+recDateAdded:], date-hfsEpochOffset)
		off = data + 248
	}

	path := filepath.Join(t.TempDir(), "raw.cdr")
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNormalizeHFS(t *testing.T) {
	const sde = 1793049178
	want := uint32(sde + hfsEpochOffset)
	a := testImage(t, 3900000000)
	b := testImage(t, 3900000777)
	for _, p := range []string{a, b} {
		var out bytes.Buffer
		if err := run([]string{"hfs", "-epoch", "1793049178", "-seed", "PostSonde 0.1.0", p}, &out); err != nil {
			t.Fatal(err)
		}
		// 3 dates in each volume header, 4 + date added in each record.
		if !strings.Contains(out.String(), "set 16 dates") {
			t.Errorf("output %q, want 16 dates", out.String())
		}
	}
	ia, _ := os.ReadFile(a)
	ib, _ := os.ReadFile(b)
	if !bytes.Equal(ia, ib) {
		t.Fatal("images with different dates differ after normalizing")
	}

	be := binary.BigEndian
	for _, off := range []int{vhOffset, testImageSize - 1024} {
		vh := ia[off:]
		if got := be.Uint32(vh[vhCreateDate:]); got != want {
			t.Errorf("volume header at %d: createDate %d, want %d", off, got, want)
		}
		if got := be.Uint32(vh[vhBackupDate:]); got != 0 {
			t.Errorf("volume header at %d: backupDate %d, want 0 (unset)", off, got)
		}
		uuid := vh[vhFinderInfoUUID : vhFinderInfoUUID+8]
		if !bytes.Equal(uuid, derive("PostSonde 0.1.0", "hfs-volume-uuid")[:8]) {
			t.Errorf("volume header at %d: UUID %x not derived from the seed", off, uuid)
		}
	}
	data := ia[testCatalog+testNodeSize+14+8:]
	if got := be.Uint32(data[recDatesOffset:]); got != want {
		t.Errorf("folder createDate %d, want %d", got, want)
	}
	if got := be.Uint32(data[recDateAdded:]); got != sde {
		t.Errorf("folder date added %d, want %d", got, sde)
	}
}

func TestNormalizeHFSRejectsOtherImages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gpt.cdr")
	if err := os.WriteFile(path, make([]byte, testImageSize), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"hfs", "-epoch", "1", "-seed", "x", path}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "-layout NONE") {
		t.Fatalf("got %v, want the no-HFS+-header error", err)
	}
}

func TestNormalizeUDIF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.dmg")
	img := make([]byte, 4096)
	koly := img[len(img)-kolySize:]
	copy(koly, "koly")
	copy(koly[kolySegmentID:], "0123456789abcdef")
	copy(koly[kolySegmentID+16:], "rest")
	if err := os.WriteFile(path, img, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"udif", "-seed", "PostSonde 0.1.0", path}, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	koly = got[len(got)-kolySize:]
	if !bytes.Equal(koly[kolySegmentID:kolySegmentID+16], derive("PostSonde 0.1.0", "udif-segment-id")[:16]) {
		t.Errorf("SegmentID %x not derived from the seed", koly[kolySegmentID:kolySegmentID+16])
	}
	if string(koly[kolySegmentID+16:kolySegmentID+20]) != "rest" || string(koly[:4]) != "koly" {
		t.Error("bytes outside the SegmentID changed")
	}

	if err := os.WriteFile(path, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"udif", "-seed", "x", path}, io.Discard); err == nil {
		t.Error("an image without a koly trailer was accepted")
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"hfs"}, {"udif", "-seed", "x"}, {"other", "-seed", "x", "f"}} {
		if err := run(args, io.Discard); err == nil || !strings.HasPrefix(err.Error(), "usage:") {
			t.Errorf("run(%q) = %v, want the usage error", args, err)
		}
	}
}
