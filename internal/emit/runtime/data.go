package z80

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// The cartridge's data, and only its data.
//
// Sixty per cent of a game's ROM is code, and that code has been translated
// into Go: shipping the image whole would ship every instruction twice, once
// as the Go that runs and once as bytes that never will. So msx2go keeps only
// the bytes a translated instruction does not cover -- which it can be exact
// about, because the tracer proved which those are -- and emits them as named
// blocks.
//
// What is left out is a hole. Reading one means the pruning was wrong about
// this cartridge, and a build with the msxcheck tag says so, loudly, at the
// address it happened. See holeAt.

// Block is one run of cartridge bytes the code reads, at its offset in the
// original image.
type Block struct {
	// Name is what the block is, where anything knows: "level_data",
	// "sound_streams". Blocks nothing has named are called after their
	// address.
	Name string
	// Off is the offset in the original image, and Data the bytes.
	Off  int
	Data []byte
}

// Info is what msx2go recorded about the cartridge the code was translated
// from. The generated rom_meta.go declares one of these as Cartridge.
type Info struct {
	// Name is the cartridge's short name, and the file its data is looked
	// for under: <Name>.dat.
	Name string
	// Machine is "msx1", "msx2" or "msx2plus".
	Machine string
	// Mapper is the paging the translation was made under.
	Mapper Mapper
	// Size is the original image's size, which the data blocks are placed
	// into by offset. Fill is what stands in the holes.
	Size int
	Fill byte
	// MainThread says this cartridge's game loop is its INIT, still
	// running, rather than an interrupt handler called once a frame. It is
	// decided at generation time by booting the image and watching, and it
	// changes two things: INIT runs interpreted, so it can be stopped at
	// an instruction boundary and resumed from a PC; and the image is kept
	// whole, because an interpreter executes from memory and pruning
	// removes exactly the bytes the translation covers.
	MainThread bool
	// SHA1 is of the blocks as Pack writes them, so that data loaded from
	// disk can be checked against what was generated.
	SHA1 string
	// Floppy says the image is a disk rather than a cartridge: there is no
	// header and no INIT, and what starts it is the BASIC loader the disk
	// boots through. See diskboot.go.
	Floppy bool
	// Run names the BASIC program the floppy starts with, for a disk
	// that has more than one and no AUTOEXEC.BAS to choose. Recorded at
	// generation time from the -run the conversion was made with.
	Run string
	// DiskSizes is how the image splits into floppies, for a game that
	// shipped on more than one. Empty means the single image every
	// other field already describes. See Info.Floppies.
	DiskSizes []int
	// TransBase, TransSize and TransSHA1 describe the RAM a disk's
	// translation was made from: the code the loader put there, hashed
	// the moment the loader finished. At the same moment at run time
	// the same bytes must hash the same, or the floppy has been edited
	// and the translation is a lie -- the machine interprets instead,
	// which is always correct and merely slower.
	TransBase uint16
	TransSize int
	TransSHA1 string
}

// Start begins whatever the image is: a cartridge at its INIT, a disk at the
// program its BASIC loader ends by jumping to. base is where a cartridge is
// mapped, which the generated module declares and a disk ignores.
func (i Info) Start(m *M, base uint16) error {
	if i.Floppy {
		// Until the loader has finished and its work is verified, the
		// translation describes code that is not in memory yet -- the
		// loader's own inline programs live at the same addresses.
		// Boot interpreted; checkTranslation turns the translation on.
		m.transStale = i.TransSHA1 != ""
		if err := m.BootDisk(m.Disk, i.Run); err != nil {
			return err
		}
		i.checkTranslation(m)
		return nil
	}
	return m.Boot(base)
}

// checkTranslation compares the loader's work against what the translation
// was made from. See Info.TransSHA1.
func (i Info) checkTranslation(m *M) {
	if i.TransSHA1 == "" {
		return
	}
	end := int(i.TransBase) + i.TransSize
	if end > len(m.Mem) {
		end = len(m.Mem)
	}
	sum := sha1.Sum(m.Mem[i.TransBase:end])
	if hex.EncodeToString(sum[:]) == i.TransSHA1 {
		m.transStale = false
		return
	}
	fmt.Fprintf(os.Stderr, "%s: the loaded program is not the one "+
		"that was translated; running it interpreted\n", i.Name)
}

// Embedded reports whether the data was compiled in -- that is, whether this
// binary was built with the msxdata tag.
func (i Info) Embedded() bool { return len(dataBlocks) > 0 }

// Blocks finds the cartridge's data.
//
// Compiled in, that is the whole of it. Otherwise <Name>.dat is looked for in,
// in order: the path given (a file or a directory), the user's data directory,
// beside the executable, and the working directory. Failing is a message
// naming every place it looked, because "not found" without the list is the
// least useful error a program can print.
func (i Info) Blocks(path string) ([]Block, string, error) {
	if len(dataBlocks) > 0 {
		return dataBlocks, "(built in)", nil
	}
	if runtime.GOOS == "js" {
		return nil, "", fmt.Errorf(
			"%s: this is a browser build with no data compiled in; "+
				"rebuild with -tags msxdata", i.Name)
	}
	var tried []string
	for _, cand := range i.candidates(path) {
		tried = append(tried, cand)
		raw, err := os.ReadFile(cand)
		if err != nil {
			continue
		}
		if err := i.verify(raw); err != nil {
			return nil, "", fmt.Errorf("%s: %w", cand, err)
		}
		b, err := Unpack(raw)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", cand, err)
		}
		return b, cand, nil
	}
	return nil, "", fmt.Errorf(
		"%s: no cartridge data found. Looked in:\n  %s\n"+
			"Put %s.dat in one of those, pass -data, or rebuild with "+
			"-tags msxdata.",
		i.Name, strings.Join(tried, "\n  "), i.Name)
}

// candidates is the search path, in order.
func (i Info) candidates(path string) []string {
	file := i.Name + ".dat"
	var out []string
	add := func(dir string) {
		if dir != "" {
			out = append(out, filepath.Join(dir, file))
		}
	}
	if path != "" {
		// A path may name the file itself or the directory holding it.
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			out = append(out, path)
		} else {
			add(path)
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		add(filepath.Join(d, "msx2go", i.Name))
	}
	if exe, err := os.Executable(); err == nil {
		add(filepath.Dir(exe))
	}
	add(".")
	return out
}

// Image lays the blocks out at their offsets, so that the mapper can page over
// them exactly as it would over the original.
func (i Info) Image(blocks []Block) []byte {
	img := make([]byte, i.Size)
	if i.Fill != 0 {
		for j := range img {
			img[j] = i.Fill
		}
	}
	for _, b := range blocks {
		copy(img[b.Off:], b.Data)
	}
	return img
}

// Floppies splits the image into the floppies it holds: one for an
// ordinary disk game, three for one that shipped on three. They are
// concatenated in order, which is why the sizes have to be recorded --
// a floppy has no header saying where it ends.
func (i Info) Floppies(blocks []Block) [][]byte {
	img := i.Image(blocks)
	if len(i.DiskSizes) < 2 {
		return [][]byte{img}
	}
	out := make([][]byte, 0, len(i.DiskSizes))
	at := 0
	for _, n := range i.DiskSizes {
		if at+n > len(img) {
			break
		}
		out = append(out, img[at:at+n])
		at += n
	}
	return out
}

// Open loads the data and returns a machine ready for Boot.
func (i Info) Open(path string) (*M, error) {
	blocks, _, err := i.Blocks(path)
	if err != nil {
		return nil, err
	}
	if i.Floppy {
		// A disk machine is all RAM: the image is the floppy, not
		// something paged into the address space. A game that
		// shipped on several floppies carries all of them, and the
		// first one is the one in the drive. See disks.go.
		m := New(nil, Mapper{})
		for _, img := range i.Floppies(blocks) {
			d, err := NewDisk(img)
			if err != nil {
				return nil, err
			}
			m.AddDisk(d)
		}
		if m.Disk == nil {
			return nil, fmt.Errorf("%s: no floppy in the data", i.Name)
		}
		// The recorded shape is what booting settled into, not where
		// booting starts: the BASIC loader's inline BLOAD ,R decides
		// main-thread-ness as it runs, and every one of its checks
		// conditions on not being there yet. Pre-setting it made the
		// loader take the title program's clean return for a
		// take-over and stop at the loading screen.
		return m, nil
	}
	m := New(i.Image(blocks), i.Mapper)
	m.installHoles(i, blocks)
	m.MainThread = i.MainThread
	return m, nil
}

// Pack renders the blocks as the bytes of a .dat file: a four-byte count,
// then each block as offset, length, name and data. It is deliberately plain
// -- this is a sidecar for one program, not an archive format.
func Pack(blocks []Block) []byte {
	sorted := append([]Block(nil), blocks...)
	sort.Slice(sorted, func(a, b int) bool {
		return sorted[a].Off < sorted[b].Off
	})
	out := make([]byte, 4)
	binary.LittleEndian.PutUint32(out, uint32(len(sorted)))
	var n [4]byte
	for _, b := range sorted {
		binary.LittleEndian.PutUint32(n[:], uint32(b.Off))
		out = append(out, n[:]...)
		binary.LittleEndian.PutUint32(n[:], uint32(len(b.Data)))
		out = append(out, n[:]...)
		out = append(out, byte(len(b.Name)))
		out = append(out, b.Name...)
		out = append(out, b.Data...)
	}
	return out
}

// Unpack reads back what Pack wrote.
func Unpack(raw []byte) ([]Block, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("data file is %d bytes, too short", len(raw))
	}
	n := int(binary.LittleEndian.Uint32(raw))
	p := 4
	out := make([]Block, 0, n)
	for k := 0; k < n; k++ {
		if p+9 > len(raw) {
			return nil, fmt.Errorf("data file ends inside block %d", k)
		}
		off := int(binary.LittleEndian.Uint32(raw[p:]))
		length := int(binary.LittleEndian.Uint32(raw[p+4:]))
		nameLen := int(raw[p+8])
		p += 9
		if p+nameLen+length > len(raw) {
			return nil, fmt.Errorf("data file ends inside block %d", k)
		}
		out = append(out, Block{
			Name: string(raw[p : p+nameLen]),
			Off:  off,
			Data: raw[p+nameLen : p+nameLen+length],
		})
		p += nameLen + length
	}
	return out, nil
}

// verify checks a data file against what was recorded at generation time.
//
// A file from a different dump is refused by name rather than left to produce
// a game that is subtly wrong three levels in: the translation was made from
// *those* bytes and is only true of them.
func (i Info) verify(raw []byte) error {
	sum := sha1.Sum(raw)
	if got := hex.EncodeToString(sum[:]); got != i.SHA1 {
		return fmt.Errorf("data SHA-1 is %s, want %s for %s",
			got, i.SHA1, i.Name)
	}
	return nil
}

// Extract writes the data out, so a build that has it can hand it to one that
// does not.
func (i Info) Extract(dir string) (string, error) {
	blocks, _, err := i.Blocks("")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(dir, i.Name+".dat")
	if err := os.WriteFile(out, Pack(blocks), 0o644); err != nil {
		return "", err
	}
	return out, nil
}
