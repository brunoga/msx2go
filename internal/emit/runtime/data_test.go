package z80

import (
	"bytes"
	"testing"
)

// The .dat file is what a build without the data compiled in reads, and what
// a build with it writes back out on -extract. The two have to be the same
// bytes or the two ways of shipping a game are not interchangeable, which is
// the whole point of having both.
func TestPackedBlocksSurviveTheRoundTrip(t *testing.T) {
	blocks := []Block{
		{Name: "sound_streams", Off: 0x3D39, Data: []byte{1, 2, 3, 4}},
		{Name: "level_data", Off: 0x1D9F, Data: bytes.Repeat([]byte{0xAB}, 300)},
		{Name: "", Off: 0, Data: []byte{0xFF}},
	}
	got, err := Unpack(Pack(blocks))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(blocks) {
		t.Fatalf("unpacked %d blocks, want %d", len(got), len(blocks))
	}
	// Pack sorts by offset, which is what makes the file's SHA-1 depend on
	// the content rather than on the order the emitter happened to walk in.
	for i := 1; i < len(got); i++ {
		if got[i-1].Off > got[i].Off {
			t.Errorf("blocks came back out of order: %d after %d",
				got[i].Off, got[i-1].Off)
		}
	}
	for _, want := range blocks {
		var found *Block
		for i := range got {
			if got[i].Off == want.Off {
				found = &got[i]
			}
		}
		if found == nil {
			t.Errorf("block at %04X went missing", want.Off)
			continue
		}
		if found.Name != want.Name || !bytes.Equal(found.Data, want.Data) {
			t.Errorf("block at %04X came back as %q/%d bytes, want %q/%d",
				want.Off, found.Name, len(found.Data),
				want.Name, len(want.Data))
		}
	}
}

// Holes are what the pruning leaves behind, and the image has to put the
// blocks back exactly where they were or every address in the translation is
// off by however much is missing.
func TestImagePlacesBlocksAtTheirOffsets(t *testing.T) {
	info := Info{Size: 16, Fill: 0xFF}
	img := info.Image([]Block{
		{Off: 2, Data: []byte{1, 2, 3}},
		{Off: 10, Data: []byte{9}},
	})
	want := []byte{
		0xFF, 0xFF, 1, 2, 3, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 9, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	}
	if !bytes.Equal(img, want) {
		t.Errorf("image is\n %v\nwant\n %v", img, want)
	}
}
