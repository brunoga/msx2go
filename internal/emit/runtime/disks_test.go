package z80

import (
	"io"
	"testing"
)

// fcbAt lays a file control block down in memory: the drive byte, then the
// name padded to eight and the extension to three, which is what a program
// hands the disk calls.
func fcbAt(m *M, at uint16, drive byte, name, ext string) uint16 {
	m.Mem[at+fcbDrive] = drive
	for i := 0; i < 8; i++ {
		c := byte(' ')
		if i < len(name) {
			c = name[i]
		}
		m.Mem[at+fcbName+uint16(i)] = c
	}
	for i := 0; i < 3; i++ {
		c := byte(' ')
		if i < len(ext) {
			c = ext[i]
		}
		m.Mem[at+fcbExt+uint16(i)] = c
	}
	return at
}

// open runs the real BDOS open and reports whether the file was found.
func open(m *M, fcb uint16) bool {
	m.C = 0x0F
	m.setDE(fcb)
	m.dos()
	return m.A == 0
}

// TestTwoFloppiesInOneDrive is the three-disk game: several images, one
// drive, and the player swapping between them. What the disk calls see has
// to follow the swap.
func TestTwoFloppiesInOneDrive(t *testing.T) {
	one, two := format(t, 720), format(t, 720)
	if err := one.Save("FIRST.BIN", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := two.Save("SECOND.BIN", []byte("two")); err != nil {
		t.Fatal(err)
	}

	m := New(nil, Mapper{})
	m.AddDisk(one)
	m.AddDisk(two)
	if m.Images() != 2 {
		t.Fatalf("images = %d, want 2", m.Images())
	}
	if m.Mounted(0) != 0 {
		t.Fatalf("the first image should be in the drive, got %d",
			m.Mounted(0))
	}

	first := fcbAt(m, 0x8000, 0, "FIRST", "BIN")
	second := fcbAt(m, 0x8100, 0, "SECOND", "BIN")
	if !open(m, first) {
		t.Error("the first disk's file is not there with the first disk in")
	}
	if open(m, second) {
		t.Error("the second disk's file is there with the first disk in")
	}

	// Insert disk two, which is all "insert disk 2" means.
	if n := m.SwapNext(0); n != 1 {
		t.Fatalf("swap put image %d in the drive, want 1", n)
	}
	if open(m, first) {
		t.Error("the first disk's file survived the swap")
	}
	if !open(m, second) {
		t.Error("the second disk's file is missing after the swap")
	}

	// And round, because a player asked for disk 2 may be asked for
	// disk 1 again later.
	if n := m.SwapNext(0); n != 0 {
		t.Fatalf("swapping round gave image %d, want 0", n)
	}
	if !open(m, first) {
		t.Error("the first disk did not come back")
	}
}

// TestTwoDrives is the other shape: both floppies in at once, and the calls
// saying which they mean -- by selecting a drive, or by naming one in the
// file control block.
func TestTwoDrives(t *testing.T) {
	one, two := format(t, 720), format(t, 720)
	if err := one.Save("FIRST.BIN", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := two.Save("SECOND.BIN", []byte("two")); err != nil {
		t.Fatal(err)
	}
	m := New(nil, Mapper{})
	m.AddDisk(one)
	m.AddDisk(two)
	if !m.Insert(1, 1) { // drive B gets the second image
		t.Fatal("inserting into drive B")
	}
	if m.Drives() != 2 {
		t.Fatalf("drives = %d, want 2", m.Drives())
	}

	// Named in the block: 1 is A and 2 is B, whatever is selected.
	if !open(m, fcbAt(m, 0x8000, 1, "FIRST", "BIN")) {
		t.Error("drive A named in the block did not find its file")
	}
	if !open(m, fcbAt(m, 0x8100, 2, "SECOND", "BIN")) {
		t.Error("drive B named in the block did not find its file")
	}
	if open(m, fcbAt(m, 0x8200, 2, "FIRST", "BIN")) {
		t.Error("drive B found drive A's file")
	}

	// Selected instead: BDOS function 0Eh, and 19h reports it back.
	m.C, m.E = 0x0E, 1
	m.dos()
	if m.CurrentDrive() != 1 {
		t.Fatalf("selected drive %d, want 1", m.CurrentDrive())
	}
	m.C = 0x19
	m.dos()
	if m.A != 1 {
		t.Errorf("function 19h says drive %d, want 1", m.A)
	}
	if !open(m, fcbAt(m, 0x8300, 0, "SECOND", "BIN")) {
		t.Error("the selected drive's file is not there")
	}
	if open(m, fcbAt(m, 0x8400, 0, "FIRST", "BIN")) {
		t.Error("the selected drive found the other one's file")
	}

	// A drive with nothing in it cannot be selected.
	if m.SelectDrive(2) {
		t.Error("selected a drive that has no floppy in it")
	}
}

// TestSwapSurvivesASnapshot pins that which floppy is in the drive is part
// of where the player got to, not a setting of the harness.
func TestSwapSurvivesASnapshot(t *testing.T) {
	one, two := format(t, 720), format(t, 720)
	m := New(nil, Mapper{})
	m.AddDisk(one)
	m.AddDisk(two)
	m.SwapNext(0) // disk two is in

	var buf []byte
	{
		var b bytesBuffer
		if err := m.SaveState(&b, "x"); err != nil {
			t.Fatal(err)
		}
		buf = b.b
	}

	restored := New(nil, Mapper{})
	restored.AddDisk(one)
	restored.AddDisk(two)
	if restored.Mounted(0) != 0 {
		t.Fatalf("a fresh machine starts with image %d",
			restored.Mounted(0))
	}
	if err := restored.LoadState(&bytesBuffer{b: buf}, "x"); err != nil {
		t.Fatal(err)
	}
	if restored.Mounted(0) != 1 {
		t.Errorf("after restoring, image %d is in the drive, want 1",
			restored.Mounted(0))
	}
	if restored.Disk != two {
		t.Error("Disk does not point at the floppy that was in the drive")
	}
}

// bytesBuffer is a tiny reader-writer, so the snapshot test needs no files.
type bytesBuffer struct {
	b []byte
	r int
}

func (b *bytesBuffer) Write(p []byte) (int, error) {
	b.b = append(b.b, p...)
	return len(p), nil
}

func (b *bytesBuffer) Read(p []byte) (int, error) {
	if b.r >= len(b.b) {
		return 0, io.EOF
	}
	n := copy(p, b.b[b.r:])
	b.r += n
	return n, nil
}
