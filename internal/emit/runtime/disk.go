package z80

// Reading an MSX floppy image.
//
// A .dsk file is the sectors of a disk, in order, and nothing else: no
// header, no track marks, no interleave. Everything about the geometry is in
// the first sector's BIOS parameter block, which is why this reads that
// rather than assuming the 720K double-sided format -- King's Valley Plus is
// a 320K single-sided disk, and a reader that assumes otherwise finds its
// directory in the middle of the first file.
//
// The file system is FAT12 as MS-DOS defined it, which is what Disk BASIC
// and MSX-DOS both write.

import (
	"errors"
	"fmt"
	"strings"
)

// Disk is a mounted floppy image.
type Disk struct {
	img []byte

	bps  int // bytes per sector
	spc  int // sectors per cluster
	res  int // reserved sectors before the first FAT
	nfat int // how many copies of the FAT
	ndir int // root directory entries
	spf  int // sectors per FAT

	dirty bool // whether anything has been written back

	fat  int // sector the first FAT starts at
	root int // sector the root directory starts at
	data int // sector cluster two starts at
}

// DiskFile is one directory entry.
type DiskFile struct {
	Name string // "KING.USR", upper case, no padding
	Size int
	clus int
}

// NewDisk mounts an image, reading its geometry from the boot sector.
func NewDisk(img []byte) (*Disk, error) {
	if len(img) < 512 {
		return nil, errors.New("z80: disk image is shorter than a sector")
	}
	b := img[:512]
	d := &Disk{
		img:  img,
		bps:  int(b[11]) | int(b[12])<<8,
		spc:  int(b[13]),
		res:  int(b[14]) | int(b[15])<<8,
		nfat: int(b[16]),
		ndir: int(b[17]) | int(b[18])<<8,
		spf:  int(b[22]) | int(b[23])<<8,
	}
	if d.bps == 0 || d.spc == 0 || d.nfat == 0 || d.spf == 0 {
		return nil, fmt.Errorf("z80: no BIOS parameter block in this image "+
			"(bytes/sector %d, sectors/cluster %d, FATs %d)",
			d.bps, d.spc, d.nfat)
	}
	d.fat = d.res
	d.root = d.fat + d.nfat*d.spf
	dirSectors := (d.ndir*32 + d.bps - 1) / d.bps
	d.data = d.root + dirSectors
	return d, nil
}

// sector returns one sector, or nil past the end of the image.
func (d *Disk) sector(n int) []byte {
	off := n * d.bps
	if off < 0 || off+d.bps > len(d.img) {
		return nil
	}
	return d.img[off : off+d.bps]
}

// next follows the FAT12 chain. Twelve-bit entries are packed a byte and a
// half each, so which nibble of the shared byte belongs to this cluster
// depends on whether its number is odd.
func (d *Disk) next(clus int) int {
	off := d.fat*d.bps + clus*3/2
	if off+1 >= len(d.img) {
		return 0xFFF
	}
	v := int(d.img[off]) | int(d.img[off+1])<<8
	if clus&1 != 0 {
		return v >> 4
	}
	return v & 0xFFF
}

// Files lists the root directory, skipping deleted entries, volume labels
// and subdirectories.
func (d *Disk) Files() []DiskFile {
	var out []DiskFile
	for i := 0; i < d.ndir; i++ {
		e := d.dirent(i)
		if e == nil || e[0] == 0x00 {
			break
		}
		if e[0] == 0xE5 || e[11]&0x18 != 0 {
			continue
		}
		name := strings.TrimRight(string(e[0:8]), " ")
		if ext := strings.TrimRight(string(e[8:11]), " "); ext != "" {
			name += "." + ext
		}
		out = append(out, DiskFile{
			Name: strings.ToUpper(name),
			Size: int(e[28]) | int(e[29])<<8 | int(e[30])<<16 | int(e[31])<<24,
			clus: int(e[26]) | int(e[27])<<8,
		})
	}
	return out
}

// dirent returns one 32-byte root directory entry.
func (d *Disk) dirent(i int) []byte {
	off := d.root*d.bps + i*32
	if off+32 > len(d.img) {
		return nil
	}
	return d.img[off : off+32]
}

// Open reads a file whole. The name is matched without case, and without the
// trailing dot Disk BASIC writes for an extensionless file.
func (d *Disk) Open(name string) ([]byte, bool) {
	want := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(name), "."))
	for _, f := range d.Files() {
		if f.Name != want {
			continue
		}
		var out []byte
		for c := f.clus; c >= 2 && c < 0xFF0 && len(out) < f.Size; c = d.next(c) {
			for s := 0; s < d.spc; s++ {
				out = append(out, d.sector(d.data+(c-2)*d.spc+s)...)
			}
		}
		if len(out) > f.Size {
			out = out[:f.Size]
		}
		return out, true
	}
	return nil, false
}

// Writing.
//
// A disk with a level editor on it is not much use read-only, so the image
// can be written back. Files are replaced whole rather than edited in place:
// the caller hands over the finished contents, this frees whatever chain the
// file had, allocates a new one out of the free clusters and rewrites the
// directory entry. That is more copying than a real DOS does and exactly as
// correct, and it keeps the tricky part -- a half-updated FAT -- impossible.

// Dirty says whether anything has been written since the image was mounted.
func (d *Disk) Dirty() bool { return d.dirty }

// Image is the whole image, for saving beside the original.
func (d *Disk) Image() []byte { return d.img }

// setNext writes one FAT12 entry, in every copy of the FAT.
func (d *Disk) setNext(clus, val int) {
	for f := 0; f < d.nfat; f++ {
		off := (d.fat+f*d.spf)*d.bps + clus*3/2
		if off+1 >= len(d.img) {
			continue
		}
		v := int(d.img[off]) | int(d.img[off+1])<<8
		if clus&1 != 0 {
			v = v&0x000F | (val&0xFFF)<<4
		} else {
			v = v&0xF000 | val&0xFFF
		}
		d.img[off] = byte(v)
		d.img[off+1] = byte(v >> 8)
	}
	d.dirty = true
}

// clusters is how many clusters the image has room for.
func (d *Disk) clusters() int {
	total := len(d.img) / d.bps
	return (total-d.data)/d.spc + 2
}

// free walks a chain and marks every cluster in it unused.
func (d *Disk) free(clus int) {
	for c := clus; c >= 2 && c < 0xFF0; {
		n := d.next(c)
		d.setNext(c, 0)
		c = n
	}
}

// alloc takes n free clusters and chains them, returning the first.
func (d *Disk) alloc(n int) (int, bool) {
	if n == 0 {
		return 0, true
	}
	var got []int
	for c := 2; c < d.clusters() && len(got) < n; c++ {
		if d.next(c) == 0 {
			got = append(got, c)
		}
	}
	if len(got) < n {
		return 0, false
	}
	for i, c := range got {
		if i+1 < len(got) {
			d.setNext(c, got[i+1])
		} else {
			d.setNext(c, 0xFFF)
		}
	}
	return got[0], true
}

// Save replaces a file's contents, creating it if the directory has room.
func (d *Disk) Save(name string, data []byte) error {
	want := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(name), "."))
	slot, found := -1, false
	firstFree := -1
	for i := 0; i < d.ndir; i++ {
		e := d.dirent(i)
		if e == nil {
			break
		}
		if e[0] == 0x00 || e[0] == 0xE5 {
			if firstFree < 0 {
				firstFree = i
			}
			if e[0] == 0x00 {
				break
			}
			continue
		}
		if e[11]&0x18 != 0 {
			continue
		}
		nm := strings.TrimRight(string(e[0:8]), " ")
		if ext := strings.TrimRight(string(e[8:11]), " "); ext != "" {
			nm += "." + ext
		}
		if strings.ToUpper(nm) == want {
			slot, found = i, true
			break
		}
	}
	if !found {
		if firstFree < 0 {
			return fmt.Errorf("z80: the disk's directory is full")
		}
		slot = firstFree
	}
	e := d.dirent(slot)
	if e == nil {
		return fmt.Errorf("z80: no directory entry %d on this disk", slot)
	}
	if found {
		d.free(int(e[26]) | int(e[27])<<8)
	}
	per := d.spc * d.bps
	need := (len(data) + per - 1) / per
	first, ok := d.alloc(need)
	if !ok {
		return fmt.Errorf("z80: the disk is full; %s needs %d bytes",
			name, len(data))
	}
	// The contents, cluster by cluster along the chain we just made.
	pos := 0
	for c := first; c >= 2 && c < 0xFF0 && pos < len(data); c = d.next(c) {
		for s := 0; s < d.spc && pos < len(data); s++ {
			sec := d.sector(d.data + (c-2)*d.spc + s)
			if sec == nil {
				return fmt.Errorf("z80: %s runs past the end of the image", name)
			}
			n := copy(sec, data[pos:])
			for i := n; i < len(sec); i++ {
				sec[i] = 0
			}
			pos += n
		}
	}
	// The directory entry. A name shorter than its field is space padded,
	// which is what every reader expects to find there.
	base, ext := want, ""
	if i := strings.IndexByte(want, '.'); i >= 0 {
		base, ext = want[:i], want[i+1:]
	}
	for i := 0; i < 8; i++ {
		e[i] = ' '
		if i < len(base) {
			e[i] = base[i]
		}
	}
	for i := 0; i < 3; i++ {
		e[8+i] = ' '
		if i < len(ext) {
			e[8+i] = ext[i]
		}
	}
	if !found {
		e[11] = 0x20 // an ordinary file, newly made
		for i := 12; i < 26; i++ {
			e[i] = 0
		}
	}
	e[26], e[27] = byte(first), byte(first>>8)
	e[28] = byte(len(data))
	e[29] = byte(len(data) >> 8)
	e[30] = byte(len(data) >> 16)
	e[31] = byte(len(data) >> 24)
	d.dirty = true
	return nil
}

// WriteSector replaces one sector, for the absolute-sector function calls.
func (d *Disk) WriteSector(n int, data []byte) bool {
	s := d.sector(n)
	if s == nil {
		return false
	}
	copy(s, data)
	d.dirty = true
	return true
}

// ReadSector reads one sector for the same reason.
func (d *Disk) ReadSector(n int) []byte { return d.sector(n) }

// Delete removes a file, freeing its clusters.
func (d *Disk) Delete(name string) error {
	want := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(name), "."))
	for i := 0; i < d.ndir; i++ {
		e := d.dirent(i)
		if e == nil || e[0] == 0x00 {
			break
		}
		if e[0] == 0xE5 || e[11]&0x18 != 0 {
			continue
		}
		nm := strings.TrimRight(string(e[0:8]), " ")
		if ext := strings.TrimRight(string(e[8:11]), " "); ext != "" {
			nm += "." + ext
		}
		if strings.ToUpper(nm) != want {
			continue
		}
		d.free(int(e[26]) | int(e[27])<<8)
		e[0] = 0xE5
		d.dirty = true
		return nil
	}
	return fmt.Errorf("z80: %s is not on this disk", name)
}

// bootProgram is the BASIC program a disk starts with.
//
// Disk BASIC runs AUTOEXEC.BAS if there is one, and drops to its prompt if
// there is not -- at which point a person types the name. There is nobody to
// type it here, so a disk without an AUTOEXEC is searched for the one program
// it can only have meant: if it holds exactly one BASIC program, that is it.
//
// Anything else is reported with the candidates named, because guessing
// between two of them is how a converted disk runs the wrong half of itself.
// Breaker's two dumps differ in exactly this way: one carries an AUTOEXEC and
// the other, which is the one real hardware will read, does not.
func (d *Disk) bootProgram() (string, error) {
	var bas []string
	for _, f := range d.Files() {
		if strings.HasSuffix(strings.ToUpper(f.Name), ".BAS") {
			if strings.EqualFold(f.Name, "AUTOEXEC.BAS") {
				return f.Name, nil
			}
			bas = append(bas, f.Name)
		}
	}
	switch len(bas) {
	case 0:
		return "", fmt.Errorf("z80: this disk has no AUTOEXEC.BAS and no " +
			"BASIC program to start instead")
	case 1:
		return bas[0], nil
	}
	return "", fmt.Errorf("z80: this disk has no AUTOEXEC.BAS and more than "+
		"one BASIC program on it (%s); name the one to run with -run",
		strings.Join(bas, ", "))
}
