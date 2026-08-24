package z80

// Directories, for a disk big enough to want them.
//
// A floppy keeps every file in one fixed root directory, which is all the
// games here have ever needed. A hard disk does not: its files live in
// directories, a directory is itself a file with a flag set, and a program
// asks for one by path. That is the whole of the difference, and it is
// small, because a directory's entries have exactly the shape the root's
// entries have -- there are just more places to look.

import "strings"

// rootDir is the cluster number that means the fixed root directory, which
// is not a cluster at all: it sits at a known sector, before the data.
const rootDir = 0

// dirBytes reads a directory whole: the fixed root, or the cluster chain of
// a subdirectory.
func (d *Disk) dirBytes(clus int) []byte {
	if clus == rootDir {
		out := make([]byte, 0, d.ndir*32)
		for i := 0; i < d.ndir; i++ {
			e := d.dirent(i)
			if e == nil {
				break
			}
			out = append(out, e...)
		}
		return out
	}
	var out []byte
	for c := clus; c >= 2 && c < 0xFF0; c = d.next(c) {
		for s := 0; s < d.spc; s++ {
			if b := d.sector(d.data + (c-2)*d.spc + s); b != nil {
				out = append(out, b...)
			}
		}
	}
	return out
}

// entryName is the name a directory entry carries, as a program writes it:
// upper case, no padding, a dot only where there is an extension.
func entryName(e []byte) string {
	name := strings.TrimRight(string(e[0:8]), " ")
	if ext := strings.TrimRight(string(e[8:11]), " "); ext != "" {
		name += "." + ext
	}
	return strings.ToUpper(name)
}

// dirEntry is one entry found by a lookup.
type dirEntry struct {
	name  string
	clus  int
	size  int
	isDir bool
}

// list reads a directory's entries, skipping the deleted, the volume label
// and the two that point at the directory itself and its parent.
func (d *Disk) list(dir int) []dirEntry {
	raw := d.dirBytes(dir)
	var out []dirEntry
	for i := 0; i+32 <= len(raw); i += 32 {
		e := raw[i : i+32]
		if e[0] == 0x00 {
			break
		}
		if e[0] == 0xE5 || e[11]&0x08 != 0 {
			continue // deleted, or the volume label
		}
		name := entryName(e)
		if name == "." || name == ".." {
			continue
		}
		out = append(out, dirEntry{
			name:  name,
			clus:  int(e[26]) | int(e[27])<<8,
			size:  int(e[28]) | int(e[29])<<8 | int(e[30])<<16 | int(e[31])<<24,
			isDir: e[11]&0x10 != 0,
		})
	}
	return out
}

// lookup finds one name in one directory.
func (d *Disk) lookup(dir int, name string) (dirEntry, bool) {
	want := strings.ToUpper(strings.TrimSuffix(strings.TrimSpace(name), "."))
	for _, e := range d.list(dir) {
		if e.name == want {
			return e, true
		}
	}
	return dirEntry{}, false
}

// Resolve walks a path from a starting directory: "SNATCHER\SNATCHER.COM",
// or "\SNATCHER" from the root, or a bare name in the directory given. Both
// separators are accepted, because MSX-DOS writes one and everything else
// writes the other.
func (d *Disk) Resolve(from int, path string) (dirEntry, bool) {
	path = strings.TrimSpace(path)
	path = strings.ReplaceAll(path, "/", "\\")
	if strings.HasPrefix(path, "\\") {
		from = rootDir
		path = strings.TrimPrefix(path, "\\")
	}
	cur := dirEntry{clus: from, isDir: true}
	for _, part := range strings.Split(path, "\\") {
		if part == "" || part == "." {
			continue
		}
		if !cur.isDir {
			return dirEntry{}, false
		}
		e, ok := d.lookup(cur.clus, part)
		if !ok {
			return dirEntry{}, false
		}
		cur = e
	}
	return cur, true
}

// ReadAt reads a file whole, from wherever it is.
func (d *Disk) ReadAt(dir int, path string) ([]byte, bool) {
	e, ok := d.Resolve(dir, path)
	if !ok || e.isDir {
		return nil, false
	}
	return d.read(e.clus, e.size), true
}

// read follows a cluster chain and stops at the file's length.
func (d *Disk) read(clus, size int) []byte {
	out := make([]byte, 0, size)
	for c := clus; c >= 2 && c < 0xFF0 && len(out) < size; c = d.next(c) {
		for s := 0; s < d.spc && len(out) < size; s++ {
			b := d.sector(d.data + (c-2)*d.spc + s)
			if b == nil {
				return out
			}
			out = append(out, b...)
		}
	}
	if len(out) > size {
		out = out[:size]
	}
	return out
}

// HardDisk reports whether this is a volume big enough to have been
// partitioned rather than a floppy somebody put in a drive. It is asked
// only to decide how to boot: a floppy boots through BASIC or its own boot
// sector, a hard disk boots through the disk operating system on it.
func (d *Disk) HardDisk() bool { return len(d.img) > 2<<20 }
