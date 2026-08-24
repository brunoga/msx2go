package z80

import (
	"bytes"
	"testing"
)

// format makes an empty FAT12 floppy of the shape MSX disks use, so the
// reader and the writer can be tested without a real image on disk.
func format(t *testing.T, sectors int) *Disk {
	t.Helper()
	img := make([]byte, sectors*512)
	b := img[:512]
	copy(b[3:11], "MSX2GO ")
	b[11], b[12] = 0x00, 0x02 // 512 bytes a sector
	b[13] = 2                 // two sectors a cluster
	b[14], b[15] = 1, 0       // one reserved
	b[16] = 2                 // two FATs
	b[17], b[18] = 112, 0     // 112 directory entries
	b[19], b[20] = byte(sectors), byte(sectors>>8)
	b[21] = 0xF9
	b[22], b[23] = 3, 0 // three sectors a FAT
	d, err := NewDisk(img)
	if err != nil {
		t.Fatalf("formatting: %v", err)
	}
	return d
}

func TestDiskWritesWhatItReads(t *testing.T) {
	d := format(t, 720)
	// Long enough to need several clusters, and not a whole number of
	// them, because the last one is where an off-by-one shows.
	data := make([]byte, 3000)
	for i := range data {
		data[i] = byte(i*7 + 1)
	}
	if err := d.Save("LEVEL.DAT", data); err != nil {
		t.Fatalf("saving: %v", err)
	}
	got, ok := d.Open("LEVEL.DAT")
	if !ok {
		t.Fatal("the file that was just written is not there")
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read back %d bytes, wrote %d", len(got), len(data))
	}
	if !d.Dirty() {
		t.Error("the image was written to and does not say so")
	}
	// Reading the image afresh has to find it too: that is the difference
	// between writing the file and writing the file system.
	again, err := NewDisk(d.Image())
	if err != nil {
		t.Fatalf("remounting: %v", err)
	}
	if got, ok := again.Open("LEVEL.DAT"); !ok || !bytes.Equal(got, data) {
		t.Error("a freshly mounted image does not have the file")
	}
}

func TestDiskReplacesAndDeletes(t *testing.T) {
	d := format(t, 720)
	big := bytes.Repeat([]byte{0xAA}, 4096)
	small := bytes.Repeat([]byte{0x55}, 100)
	if err := d.Save("A.BIN", big); err != nil {
		t.Fatal(err)
	}
	if err := d.Save("B.BIN", big); err != nil {
		t.Fatal(err)
	}
	// Replacing with something smaller has to free what it no longer
	// needs, or a disk fills up by being written to.
	if err := d.Save("A.BIN", small); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Open("A.BIN"); !bytes.Equal(got, small) {
		t.Error("A.BIN was not replaced")
	}
	if got, _ := d.Open("B.BIN"); !bytes.Equal(got, big) {
		t.Error("replacing A.BIN disturbed B.BIN")
	}
	if len(d.Files()) != 2 {
		t.Errorf("directory has %d files, want 2", len(d.Files()))
	}
	if err := d.Delete("A.BIN"); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Open("A.BIN"); ok {
		t.Error("A.BIN survived being deleted")
	}
	if got, _ := d.Open("B.BIN"); !bytes.Equal(got, big) {
		t.Error("deleting A.BIN disturbed B.BIN")
	}
	// The freed clusters have to come back, so a disk written to over and
	// over does not run out.
	for i := 0; i < 20; i++ {
		if err := d.Save("C.BIN", big); err != nil {
			t.Fatalf("save %d: %v", i, err)
		}
	}
}

func TestDiskReadsItsGeometryRatherThanAssumingIt(t *testing.T) {
	// A 320K single-sided disk, which is what King's Valley Plus is, and
	// which a reader that assumes 720K finds nothing on.
	d := format(t, 640)
	if d.data != 1+2*3+7 {
		t.Errorf("first data sector %d, want %d", d.data, 1+2*3+7)
	}
	if err := d.Save("X", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got, ok := d.Open("X"); !ok || string(got) != "hello" {
		t.Errorf("read back %q", got)
	}
}

// TestFCBReusedForAnotherFile is King's Valley Plus's overlays: the program
// loads GAME.USR and later EDIT.USR through the same file control block, and
// what it gets the second time has to be the second file.
func TestFCBReusedForAnotherFile(t *testing.T) {
	d := format(t, 720)
	game := bytes.Repeat([]byte{0x11}, 600)
	edit := bytes.Repeat([]byte{0x22}, 600)
	if err := d.Save("GAME.USR", game); err != nil {
		t.Fatal(err)
	}
	if err := d.Save("EDIT.USR", edit); err != nil {
		t.Fatal(err)
	}
	m := New(nil, Mapper{})
	m.Disk = d
	var fcb, dma uint16 = 0xDCB9, 0xB92D
	open := func(name string) []byte {
		for i := 0; i < 11; i++ {
			m.Mem[fcb+1+uint16(i)] = ' '
		}
		base, ext, _ := cut(name, ".")
		for i := 0; i < len(base); i++ {
			m.Mem[fcb+1+uint16(i)] = base[i]
		}
		for i := 0; i < len(ext); i++ {
			m.Mem[fcb+9+uint16(i)] = ext[i]
		}
		m.C, m.D, m.E = 0x0F, byte(fcb>>8), byte(fcb)
		m.dos()
		if m.A != 0 {
			t.Fatalf("opening %s: A=%02X", name, m.A)
		}
		// From the first record: a program sets this itself, and it
		// still holds the last file's position.
		for i := 0; i < 4; i++ {
			m.Mem[fcb+33+uint16(i)] = 0
		}
		// Set the transfer address, then read five records of 128.
		m.C, m.D, m.E = 0x1A, byte(dma>>8), byte(dma)
		m.dos()
		m.C, m.D, m.E = 0x27, byte(fcb>>8), byte(fcb)
		m.setHL(5)
		m.dos()
		out := make([]byte, 600)
		for i := range out {
			out[i] = m.Mem[dma+uint16(i)]
		}
		return out
	}
	if got := open("GAME.USR"); !bytes.Equal(got, game) {
		t.Fatalf("GAME.USR read back wrong: %02X...", got[:4])
	}
	// The program never closes it -- overlays rarely do -- and reuses the
	// same control block for the other file.
	if got := open("EDIT.USR"); !bytes.Equal(got, edit) {
		t.Errorf("EDIT.USR read back the previous file: %02X..., want %02X",
			got[:4], edit[0])
	}
}

// cut splits on the first sep, like strings.Cut.
func cut(s, sep string) (string, string, bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}
