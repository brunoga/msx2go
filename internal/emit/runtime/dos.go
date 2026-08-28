package z80

// The disk operating system a disk program calls.
//
// A program loaded from a disk does its own file work, and it does it the way
// CP/M taught everyone to: put a function number in C, point DE at a file
// control block, and call one entry point. MSX-DOS puts that entry at 0005h.
// Disk BASIC puts it at F37Dh, in the work area, which is what King's Valley
// Plus calls -- `ld c,1Ah / call F37Dh` is "set the transfer address" in the
// third instruction it runs.
//
// Both are shimmed here against the mounted image, so a disk program gets its
// files without a disk ROM, a controller or a real DOS underneath it.

import "fmt"

// dosBDOS is Disk BASIC's entry point for the function calls, in the RAM work
// area. MSX-DOS's own entry at 0005h is in page zero and reaches the same
// place through the BIOS shim.
const dosBDOS = 0xF37D

// The disk ROM's own jump table, in page one. A disk that carries a
// filesystem is read through the function calls at F37Dh above; a game
// that formatted its floppies its own way reads raw sectors through
// these, because there is no filesystem for the function calls to work
// on. Snatcher calls exactly one of them.
const (
	dskIO   = 0x4010 // read or write sectors
	dskChg  = 0x4013 // has the floppy been swapped
	getDPB  = 0x4016 // describe the disk's geometry
	choice  = 0x4019 // the format menu, which there is none of here
	dskFmt  = 0x401C // format a disk
	mtOff   = 0x401F // stop the motor
	dskBoot = 0x4022 // start the disk system, and BASIC after it
	dskLast = 0x4025 // one past the table
)

// FCB layout, as MSX-DOS defines it. Only the fields a program is entitled to
// see are touched; the rest is ours to use for the open file's identity.
const (
	fcbDrive  = 0  // 0 for the default drive, 1 for A
	fcbName   = 1  // eight characters, space padded
	fcbExt    = 9  // three more
	fcbExtent = 12 // which 16K extent, for the sequential calls
	fcbRecLen = 14 // bytes per record, 128 unless the program says otherwise
	fcbSize   = 16 // the file's length, four bytes
	fcbRecord = 32 // the record within the extent
	fcbRandom = 33 // the record number the random calls use, four bytes
)

// dos runs one function call and leaves the result in A, the way the real one
// does: zero for success, and FFh for "no".
func (m *M) dos() {
	fn := m.C
	de := m.DE()
	if m.Disk == nil {
		m.A = 0xFF
		return
	}
	if m.DOSTrace != nil {
		m.DOSTrace(fn, de)
	}
	// The kernel this stands for is thousands of instructions of FAT and
	// directory work, and a game's loading is paced by it. See dosCost.
	m.tickShim(dosCost(fn))
	switch fn {
	case 0x02: // write the character in E to the console
		m.chPut(m.E)
		m.A = 0
	case 0x09: // write the string at DE, which ends at a dollar sign
		for a, n := de, 0; n < 0x4000; a, n = a+1, n+1 {
			c := m.rd(a)
			if c == '$' {
				break
			}
			m.chPut(c)
		}
		m.A = 0
	case 0x6F: // which MSX-DOS this is: B is the kernel's major version
		// Two, because that is what this machine's disk calls
		// behave like: directories on a path, a memory mapper, and
		// the segment count the kernel keeps. Answering one sends a
		// program down the path for a machine without them, and
		// Snatcher's loader says so and stops.
		// Measured on the reference machine, which answers A=0, B=2,
		// C=31h, D=0, E=0: kernel version 2.31, and no version for
		// the system file. The whole reply matters, not just B. A
		// program deciding whether the extended calls are there reads
		// further than the major version, and a machine that set B
		// and left the rest holding whatever the caller had put there
		// answered "two point whatever happened to be in C".
		m.A, m.B, m.C = 0, 2, 0x31
		m.setDE(0)
	case 0x0C: // return the version
		m.setHL(0x0022)
		m.A = 0
	case 0x0D: // reset the disk system
		m.A = 0
	case 0x0E: // select a drive: E names it, counted from zero
		m.SelectDrive(int(m.E))
		m.A = byte(m.CurrentDrive())
	case 0x19: // which drive is current
		m.A = byte(m.CurrentDrive())
	case 0x0F: // open a file
		m.A = 0xFF
		if f := m.dosOpen(de); f != nil {
			m.dosFillFCB(de, f)
			m.A = 0
		}
	case 0x10: // close it, writing back anything that changed
		m.A = 0xFF
		if f := m.files[de]; f != nil {
			if err := m.dosFlush(f); err == nil {
				m.A = 0
			}
			delete(m.files, de)
		}
	case 0x11: // find the first file matching the name
		m.searchAt = 0
		m.searchFor = m.fcbNameAt(de)
		m.searchIn, m.searchOn = m.cwd, m.fcbDisk(de)
		m.A = m.dosSearch()
	case 0x12: // and the next
		m.A = m.dosSearch()
	case 0x13: // delete
		m.A = 0xFF
		if name := m.fcbNameAt(de); name != "" {
			if err := m.Disk.Delete(name); err == nil {
				m.A = 0
			}
		}
	case 0x14: // read the record the file control block is sitting on
		m.tickShim(uint32(m.dosRecLen(de)) * cycDiskByte)
		m.A = m.dosRead(de, m.dosSeqPos(de))
		if m.A == 0 {
			m.dosBump(de)
		}
	case 0x15: // and write it
		m.tickShim(uint32(m.dosRecLen(de)) * cycDiskByte)
		m.A = m.dosWrite(de, m.dosSeqPos(de))
		if m.A == 0 {
			m.dosBump(de)
		}
	case 0x16: // create a file, replacing whatever was there
		m.dosCreate(de)
		m.A = 0
	case 0x1A: // set the transfer address
		m.dma = de
		m.A = 0
	case 0x21: // read the record the random field names
		m.tickShim(uint32(m.dosRecLen(de)) * cycDiskByte)
		m.A = m.dosRead(de, m.dosRandPos(de))
	case 0x22: // and write it
		m.tickShim(uint32(m.dosRecLen(de)) * cycDiskByte)
		m.A = m.dosWrite(de, m.dosRandPos(de))
	case 0x23: // how long is it, in records
		m.A = 0xFF
		if f := m.dosOpen(de); f != nil {
			n := (len(f.data) + m.dosRecLen(de) - 1) / m.dosRecLen(de)
			m.dosSetRandom(de, n)
			m.A = 0
		}
	case 0x26, 0x27: // block write and block read: HL records at once
		n := int(m.HL())
		rec := m.dosRecLen(de)
		pos := m.dosRandPos(de)
		done := 0
		for ; done < n; done++ {
			var st byte
			if fn == 0x27 {
				st = m.dosRead(de, pos+done*rec)
			} else {
				st = m.dosWrite(de, pos+done*rec)
			}
			if st != 0 {
				break
			}
			m.dma += uint16(rec)
		}
		m.tickShim(uint32(done*rec) * cycDiskByte)
		m.dma -= uint16(done * rec)
		m.setHL(uint16(done))
		m.dosSetRandom(de, (pos+done*rec)/rec)
		m.A = 0
		if done < n {
			m.A = 1 // ran out of file, which is not an error
		}
	case 0x2F, 0x30: // absolute sector read and write: DE = first
		// sector, H = how many, L = which drive (measured on the
		// real machine: B holds garbage there)
		sec := int(m.DE())
		cnt := int(m.H)
		disk := m.Disk
		if d := m.DriveDisk(int(m.L)); d != nil {
			disk = d
		}
		m.A = 0
		m.tickShim(uint32(cnt*disk.bps) * cycDiskByte)
		for i := 0; i < cnt; i++ {
			if fn == 0x2F {
				b := disk.ReadSector(sec + i)
				if b == nil {
					m.A = 0xFF
					break
				}
				for j, v := range b {
					m.wr(m.dma+uint16(i*len(b)+j), v)
				}
			} else {
				b := make([]byte, disk.bps)
				for j := range b {
					b[j] = m.Mem[m.dma+uint16(i*len(b)+j)]
				}
				if !disk.WriteSector(sec+i, b) {
					m.A = 0xFF
					break
				}
			}
		}
	default:
		panic(fmt.Sprintf("z80: this disk program calls DOS function "+
			"%02Xh, which is not implemented yet", fn))
	}
}

// dosFile is a file a program has open. The contents are held whole and
// written back when it is closed, which is what makes a partly written
// directory impossible.
type dosFile struct {
	name string
	data []byte
	// disk is the floppy it was opened on, so that a file written back
	// after the drive was reselected -- or after the player swapped a
	// disk -- lands where it came from rather than on whatever is in
	// the drive now.
	disk  *Disk
	dirty bool
}

// FCBBlock is what a block call will move: the byte offset in the file that
// the random-record field names, and the record size it is counted in.
//
// A disk game's block reads are landmarks nothing else can fake -- the same
// bytes in the same order however fast either machine runs -- so lining them
// up against a reference machine's is how a loading sequence's timing is
// checked at all. See games/snatcher/README.md.
func (m *M) FCBBlock(fcb uint16) (pos, recLen int) {
	return m.dosRandPos(fcb), m.dosRecLen(fcb)
}

// DMA is the address a disk read will land at, which the program sets with
// function 1Ah.
func (m *M) DMA() uint16 { return m.dma }

// dosRecLen is the record size the file control block asks for, which is 128
// unless the program set it.
func (m *M) dosRecLen(fcb uint16) int {
	n := int(m.Mem[fcb+fcbRecLen]) | int(m.Mem[fcb+fcbRecLen+1])<<8
	if n == 0 {
		return 128
	}
	return n
}

// dosSeqPos is where the sequential calls are: the extent and the record
// within it, in bytes.
func (m *M) dosSeqPos(fcb uint16) int {
	ex := int(m.Mem[fcb+fcbExtent]) | int(m.Mem[fcb+fcbExtent+1])<<8
	cr := int(m.Mem[fcb+fcbRecord])
	return (ex*128 + cr) * m.dosRecLen(fcb)
}

// dosBump moves a file control block on by one record, carrying into the
// extent as CP/M's does.
func (m *M) dosBump(fcb uint16) {
	cr := int(m.Mem[fcb+fcbRecord]) + 1
	if cr > 127 {
		cr = 0
		ex := int(m.Mem[fcb+fcbExtent]) | int(m.Mem[fcb+fcbExtent+1])<<8 + 1
		m.Mem[fcb+fcbExtent] = byte(ex)
		m.Mem[fcb+fcbExtent+1] = byte(ex >> 8)
	}
	m.Mem[fcb+fcbRecord] = byte(cr)
}

// dosRandPos is where the random calls are.
func (m *M) dosRandPos(fcb uint16) int {
	n := 0
	for i := 3; i >= 0; i-- {
		n = n<<8 | int(m.Mem[fcb+fcbRandom+uint16(i)])
	}
	return n * m.dosRecLen(fcb)
}

func (m *M) dosSetRandom(fcb uint16, rec int) {
	for i := 0; i < 4; i++ {
		m.Mem[fcb+fcbRandom+uint16(i)] = byte(rec >> (8 * i))
	}
}

// dosOpen finds the file a control block names, reading it in the first time.
//
// The block is remembered by its address, and a program reuses one address for
// one file after another -- King's Valley Plus loads both of its overlays,
// GAME.USR and EDIT.USR, through the control block at DCB9h. So what is
// remembered has to be checked against the name that is in the block *now*.
// Handing back the last file opened there meant that choosing the level editor
// after playing a game loaded the game's overlay again and jumped into it,
// which is a black screen.
func (m *M) dosOpen(fcb uint16) *dosFile {
	name := m.fcbNameAt(fcb)
	want := m.fcbDisk(fcb)
	if f, ok := m.files[fcb]; ok {
		// The same block naming the same file on the same floppy is
		// the file that is already open. On a *different* floppy it
		// is a different file with the same name, which is what an
		// "insert disk 2" prompt produces -- and answering it from
		// the cache hands back the disk that was just taken out.
		if f.name == name && f.disk == want {
			return f
		}
		// Reused for something else: whatever was open in it is
		// finished with, so write back anything it changed.
		m.dosFlush(f)
		delete(m.files, fcb)
	}
	data, ok := want.ReadAt(m.cwd, name)
	if !ok {
		return nil
	}
	if m.files == nil {
		m.files = map[uint16]*dosFile{}
	}
	f := &dosFile{name: name, data: data, disk: want}
	m.files[fcb] = f
	return f
}

// dosCreate opens an empty file, which is what a program about to save does.
func (m *M) dosCreate(fcb uint16) *dosFile {
	if m.files == nil {
		m.files = map[uint16]*dosFile{}
	}
	if f, ok := m.files[fcb]; ok {
		m.dosFlush(f)
	}
	f := &dosFile{name: m.fcbNameAt(fcb), dirty: true, disk: m.fcbDisk(fcb)}
	m.files[fcb] = f
	m.dosFillFCB(fcb, f)
	return f
}

// dosFillFCB tells the program how big the file is and where it is sitting,
// which is what a real open leaves behind.
func (m *M) dosFillFCB(fcb uint16, f *dosFile) {
	for i := 0; i < 4; i++ {
		m.Mem[fcb+fcbSize+uint16(i)] = byte(len(f.data) >> (8 * i))
	}
	if m.dosRecLen(fcb) == 128 && m.Mem[fcb+fcbRecLen] == 0 &&
		m.Mem[fcb+fcbRecLen+1] == 0 {
		m.Mem[fcb+fcbRecLen] = 128
	}
	m.Mem[fcb+fcbExtent] = 0
	m.Mem[fcb+fcbExtent+1] = 0
	m.Mem[fcb+fcbRecord] = 0
}

// dosRead copies one record out of an open file into the transfer address.
func (m *M) dosRead(fcb uint16, pos int) byte {
	f := m.dosOpen(fcb)
	if f == nil {
		return 0xFF
	}
	n := m.dosRecLen(fcb)
	if pos >= len(f.data) {
		return 1 // past the end, which is how a reader knows to stop
	}
	for i := 0; i < n; i++ {
		var v byte
		if pos+i < len(f.data) {
			v = f.data[pos+i]
		}
		m.wr(m.dma+uint16(i), v)
	}
	return 0
}

// dosWrite copies one record the other way, growing the file to fit.
func (m *M) dosWrite(fcb uint16, pos int) byte {
	f := m.files[fcb]
	if f == nil {
		if f = m.dosOpen(fcb); f == nil {
			f = m.dosCreate(fcb)
		}
	}
	n := m.dosRecLen(fcb)
	for len(f.data) < pos+n {
		f.data = append(f.data, 0)
	}
	for i := 0; i < n; i++ {
		f.data[pos+i] = m.Mem[m.dma+uint16(i)]
	}
	f.dirty = true
	return 0
}

// dosFlush writes a changed file back to the image.
func (m *M) dosFlush(f *dosFile) error {
	if !f.dirty {
		return nil
	}
	disk := f.disk
	if disk == nil {
		disk = m.Disk
	}
	if err := disk.Save(f.name, f.data); err != nil {
		return err
	}
	f.dirty = false
	return nil
}

// DOSFlushAll writes back every file a program left open, which is what a
// harness does when it is closing down and the program never did.
func (m *M) DOSFlushAll() error {
	for _, f := range m.files {
		if err := m.dosFlush(f); err != nil {
			return err
		}
	}
	return nil
}

// dosSearch fills the transfer address with the next matching directory
// entry, in the thirty-two byte form a program expects to read there.
func (m *M) dosSearch() byte {
	on := m.searchOn
	if on == nil {
		on = m.Disk
	}
	files := on.FilesIn(m.searchIn)
	for ; m.searchAt < len(files); m.searchAt++ {
		f := files[m.searchAt]
		if !dosMatch(m.searchFor, f.Name) {
			continue
		}
		m.searchAt++
		base, ext := f.Name, ""
		if i := indexByte(base, '.'); i >= 0 {
			base, ext = base[:i], base[i+1:]
		}
		for i := 0; i < 8; i++ {
			c := byte(' ')
			if i < len(base) {
				c = base[i]
			}
			m.Mem[m.dma+1+uint16(i)] = c
		}
		for i := 0; i < 3; i++ {
			c := byte(' ')
			if i < len(ext) {
				c = ext[i]
			}
			m.Mem[m.dma+9+uint16(i)] = c
		}
		m.Mem[m.dma] = 0
		for i := 0; i < 4; i++ {
			m.Mem[m.dma+28+uint16(i)] = byte(f.Size >> (8 * i))
		}
		return 0
	}
	return 0xFF
}

// dosMatch is the directory match, with ? standing for any character and a
// name of all question marks -- which is what a listing asks for -- matching
// everything.
func dosMatch(pattern, name string) bool {
	if pattern == "" {
		return true
	}
	pb, pe := splitName(pattern)
	nb, ne := splitName(name)
	return globPart(pb, nb, 8) && globPart(pe, ne, 3)
}

func splitName(s string) (string, string) {
	if i := indexByte(s, '.'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func globPart(pat, name string, n int) bool {
	for i := 0; i < n; i++ {
		p, c := byte(' '), byte(' ')
		if i < len(pat) {
			p = pat[i]
		}
		if i < len(name) {
			c = name[i]
		}
		if p == '?' || p == '*' {
			continue
		}
		if p != c {
			return false
		}
	}
	return true
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// FCBName is the file a control block names, for diagnostics.
func (m *M) FCBName(fcb uint16) string { return m.fcbNameAt(fcb) }

// fcbName reads the eleven characters of a file control block's name as
// "NAME.EXT", which is what the directory holds.
func (m *M) fcbNameAt(fcb uint16) string {
	name := ""
	for i := 0; i < 8; i++ {
		name += string(rune(m.Mem[fcb+fcbName+uint16(i)]))
	}
	ext := ""
	for i := 0; i < 3; i++ {
		ext += string(rune(m.Mem[fcb+fcbExt+uint16(i)]))
	}
	name = trimSpaces(name)
	if ext = trimSpaces(ext); ext != "" {
		return name + "." + ext
	}
	return name
}

func trimSpaces(s string) string {
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == 0) {
		s = s[:len(s)-1]
	}
	return s
}

// diskROM is the disk ROM's jump table: raw sector access, for a floppy
// with no filesystem on it.
//
// The register conventions are the published ones, and were measured on
// the reference machine before being written down: A is the drive, B how
// many sectors, C the media descriptor, DE the first sector and HL where
// the bytes go, with carry set for a write. Coming back, carry clear is
// success and B says how many sectors moved; carry set puts an error code
// in A.
// hStkE is the hook BASIC calls while it lays out its stack. Measured in the
// reference machine's BIOS at 62F0h, where the call sits one instruction
// before `ld sp,hl`.
const hStkE = 0xFEDA

// basicTop is where BASIC keeps the top of the memory it will use, which is
// what it hands the stack-end hook in HL.
const basicTop = 0xF674

// diskBoot is the disk ROM's last jump-table entry: start the disk system.
//
// On the hardware it lays out the disk work area and goes on into BASIC's
// initialisation, and partway through that BASIC calls the stack-end hook.
// That is not a detail -- it is how a program hands the machine over without
// being a cartridge. Snatcher's loader points the hook at its own code, calls
// this entry through the disk ROM's slot, and follows the call with a jump to
// the reset vector, which says plainly that it does not expect to come back.
//
// So what this does is the part the caller depends on: run the hook, with HL
// holding what BASIC would have put there. There is no BASIC underneath to
// return into, and a program that hooks the stack end is not planning to let
// one run.
func (m *M) diskBoot() {
	if m.Mem[hStkE] != 0xC3 {
		return
	}
	m.biosPageZero()
	m.setHL(m.rd16(basicTop))
	m.run(m.rd16(hStkE + 1))
}

// biosPageZero gives back the two bytes in page zero that name the VDP's
// ports.
//
// A program that has stopped being a disk program reads them from 0006h and
// 0007h directly. Under the operating system those two addresses are the
// operand of the kernel's own entry at 0005h, which this machine wrote there
// itself when it loaded the program, so what it hands back is its own
// clobber rather than anything the program did.
//
// Only those two. Page zero is under the memory mapper as well as under the
// slots, and this machine keeps one flat memory for both: Snatcher switches
// page zero's segment between two of its own while it runs, and a wholesale
// swap of the BIOS's page zero into that memory is written straight back out
// into whichever segment the game switches to next, which corrupts a segment
// it is about to execute. Modelling page zero's slot properly means keeping
// the RAM underneath it whole, which is a larger change than this.
func (m *M) biosPageZero() {
	if m.bios0 == nil {
		return
	}
	m.Mem[VDPDataRead] = m.bios0[VDPDataRead]
	m.Mem[VDPDataWrite] = m.bios0[VDPDataWrite]
}

func (m *M) diskROM(a uint16) {
	switch a {
	case dskBoot:
		m.diskBoot()
	case dskIO:
		write := m.Fc
		drive, want := int(m.A), int(m.B)
		sec := int(m.DE())
		at := m.HL()
		disk := m.Disk
		if d := m.DriveDisk(drive); d != nil {
			disk = d
		}
		done := 0
		for ; done < want; done++ {
			if write {
				b := make([]byte, disk.bps)
				for i := range b {
					b[i] = m.rd(at + uint16(done*disk.bps+i))
				}
				if !disk.WriteSector(sec+done, b) {
					break
				}
				continue
			}
			b := disk.ReadSector(sec + done)
			if b == nil {
				break
			}
			for i, v := range b {
				m.wr(at+uint16(done*disk.bps+i), v)
			}
		}
		// The disk ROM sits below the kernel, so none of dosCost's FAT
		// and directory work applies -- but a byte off a disk costs
		// what a byte off a disk costs, not what a byte into video
		// memory costs, which is what this charged before.
		m.tickShim(uint32(done*disk.bps) * cycDiskByte)
		m.B = byte(done)
		if done < want {
			// "Not ready", which is what a drive says about a
			// sector that is not there.
			m.A, m.Fc = 2, true
			return
		}
		m.A, m.Fc = 0, false
	case dskChg:
		// The floppy has not been swapped since the last call unless
		// somebody swapped it, which Insert records. One says
		// unchanged, minus one changed.
		m.B = 1
		if m.diskSwapped {
			m.B, m.diskSwapped = 0xFF, false
		}
		m.A, m.Fc = 0, false
	case getDPB:
		// The drive parameter block, built from the floppy's own boot
		// sector. A disk with no filesystem has nothing to describe,
		// so this reports failure rather than inventing a geometry.
		m.A, m.Fc = 12, true
	case choice:
		// No format menu: a machine with one floppy shape has no
		// choice to offer.
		m.setHL(0)
		m.A, m.Fc = 0, false
	case dskFmt:
		m.A, m.Fc = 16, true // write protected: nothing formats here
	case mtOff:
		m.A, m.Fc = 0, false
	}
}
