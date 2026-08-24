package z80

// Snapshots: the whole machine, written down and put back.
//
// What makes this possible at all is that the translated code is not a
// coroutine. A frame is one call to Run, and between frames there is no Go
// control flow to preserve -- no program counter halfway down thirty thousand
// labels, no Go stack to unwind. Everything that survives a frame boundary is
// a field of M, so writing those fields down is writing down the machine.
//
// The price is the rule that follows from it: a snapshot may only be taken
// between frames. Save is not safe to call from inside an interrupt handler,
// and there is no way to make it so short of translating to a coroutine.
//
// The address space is written whole rather than as "RAM plus which bank is
// paged where". Two thirds of it is the cartridge and re-derivable, so this is
// wasteful, and it is deliberately wasteful: paging is per-cartridge and a
// snapshot that reconstructed it would be a snapshot that could be wrong.
// Eighty kilobytes is not worth being clever about.

import (
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
)

// snapMagic identifies the format, and snapVersion guards against reading a
// snapshot written by a different one. There is no migration: a snapshot is a
// convenience, not an archive.
const (
	snapMagic   = "MSX2GO-SNAPSHOT"
	snapVersion = 1
)

// SaveState writes the machine to w. id should be the cartridge's SHA-1, and
// is checked on the way back in -- a snapshot of one game restored into
// another would be a machine in a state its code cannot account for.
//
// It must be called between frames. See the note at the top of this file.
func (m *M) SaveState(w io.Writer, id string) error {
	if m.nest > 0 {
		return fmt.Errorf("z80: SaveState called inside an interrupt " +
			"handler; a snapshot may only be taken between frames")
	}
	zw := gzip.NewWriter(w)
	s := &snapWriter{w: zw}
	s.str(snapMagic)
	s.u32(snapVersion)
	s.str(id)
	m.walk(s)
	if s.err != nil {
		return s.err
	}
	return zw.Close()
}

// LoadState puts a machine back as SaveState left it.
func (m *M) LoadState(r io.Reader, id string) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	s := &snapReader{r: zr}
	if got := s.str(); got != snapMagic {
		return fmt.Errorf("z80: not a snapshot")
	}
	if v := s.u32(); v != snapVersion {
		return fmt.Errorf("z80: snapshot is version %d, this build "+
			"reads version %d", v, snapVersion)
	}
	if got := s.str(); got != id {
		return fmt.Errorf("z80: snapshot is of a different cartridge "+
			"(%s, wanted %s)", got, id)
	}
	m.walk(s)
	return s.err
}

// walk visits every field that survives a frame boundary, in one place, so
// that saving and loading cannot drift apart: the same function does both.
func (m *M) walk(s snapper) {
	s.bytes(m.Mem[:])
	// Video memory is 16K on a TMS9918 and 128K on a V9938, and which one
	// this is was decided by the cartridge while it ran. So its size goes
	// in the snapshot and a restored machine grows to match.
	n := len(m.VDP.VRAM)
	s.int(&n)
	if s.reading() && n != len(m.VDP.VRAM) {
		m.VDP.VRAM = make([]byte, n)
		m.VDP.V9938 = n > 0x4000
	}
	s.bytes(m.VDP.VRAM)
	s.bytes(m.VDP.Reg[:])
	s.bool(&m.VDP.V9938)
	s.bytes(m.Keys[:])

	for _, p := range []*byte{
		&m.A, &m.B, &m.C, &m.D, &m.E, &m.H, &m.L,
		&m.A2, &m.B2, &m.C2, &m.D2, &m.E2, &m.H2, &m.L2,
		&m.rReg, &m.PrimarySlot, &m.ppiC, &m.keySeen,
		&m.PSG.Latch, &m.PSG.PortA, &m.SCC.Enable,
	} {
		s.byte(p)
	}
	for _, p := range []*bool{
		&m.Fs, &m.Fz, &m.Fh, &m.Fp, &m.Fn, &m.Fc,
		&m.Fs2, &m.Fz2, &m.Fh2, &m.Fp2, &m.Fn2, &m.Fc2,
		&m.IFF, &m.halted, &m.idle, &m.booting, &m.inISR,
		&m.VDP.BootVblank, &m.SCC.Active,
		// Which shape of cartridge this is, decided during boot: a
		// restored machine has not booted and would not work it out
		// again. See Boot.
		&m.MainThread,
		&m.transStale,
		// A swap the disk ROM has not been told about yet is state:
		// restoring must still let the game notice the floppy moved.
		&m.diskSwapped,
		// Whether a program loaded by the disk operating system is
		// running, which decides whether page zero answers as the
		// BIOS or as the program's own memory.
		&m.dosProgram,
	} {
		s.bool(p)
	}
	for _, p := range []*uint16{&m.IX, &m.IY, &m.SP, &m.PC, &m.VDP.addr,
		&m.loadLo, &m.loadHi} {
		s.u16(p)
	}

	s.byte(&m.VDP.first)
	s.byte(&m.VDP.status)
	s.byte(&m.VDP.readAhead)
	s.bool(&m.VDP.latched)
	s.u64(&m.VDP.Frame)

	// Which floppy is in which drive, and which drive the disk calls
	// act on. The images themselves belong to whoever built the
	// machine, the way Disk does -- but where a player got to in a
	// three-disk game is state, and restoring with the wrong disk in
	// the drive would be a snapshot that lies. See disks.go.
	nd := len(m.inDrive)
	s.int(&nd)
	if s.reading() && nd != len(m.inDrive) {
		m.inDrive = make([]int, nd)
	}
	for i := range m.inDrive {
		s.int(&m.inDrive[i])
	}
	s.int(&m.curDrive)
	s.int(&m.cwd)
	if s.reading() {
		m.syncDisk()
	}

	// The memory mapper's segments: which one is in each page, and the
	// bytes of the ones that are not. A game that uses mapper RAM keeps
	// most of itself there, so a snapshot without them comes back with
	// sixteen K of the game and a hundred and twelve of nothing.
	ns := len(m.ramStore)
	s.int(&ns)
	if s.reading() && ns != len(m.ramStore) {
		m.ramStore = make([][]byte, ns)
		for i := range m.ramStore {
			m.ramStore[i] = make([]byte, ramSegSize)
		}
	}
	for i := range m.ramStore {
		s.bytes(m.ramStore[i])
	}
	for i := range m.ramSeg {
		s.int(&m.ramSeg[i])
	}

	s.bytes(m.PSG.Reg[:])

	// The sound chip in the mapper, where there is one.
	for i := range m.SCC.Wave {
		for j := range m.SCC.Wave[i] {
			s.i8(&m.SCC.Wave[i][j])
		}
	}
	for i := range m.SCC.Freq {
		s.int(&m.SCC.Freq[i])
		s.byte(&m.SCC.Vol[i])
		s.int(&m.SCC.pos[i])
	}

	// Timing. irqTaken carries the debt of a handler that overran its
	// frame, so a snapshot that dropped it would resume a machine that had
	// forgotten it was behind. See cycles.go.
	s.u64(&m.Cyc)
	s.u64(&m.lastIRQ)
	s.i64(&m.credit)
	s.int(&m.irqTaken)
	s.int(&m.IM)
	s.int(&m.bootIRQs)
	s.int(&m.frames)

	// The paging: which bank each register has selected.
	nb := len(m.mem.bank)
	s.int(&nb)
	if s.reading() && nb != len(m.mem.bank) {
		s.fail(fmt.Errorf("z80: snapshot has %d bank registers, this "+
			"mapper has %d", nb, len(m.mem.bank)))
		return
	}
	for i := range m.mem.bank {
		s.int(&m.mem.bank[i])
	}
}

// snapper is the one interface save and load share.
type snapper interface {
	bytes(b []byte)
	byte(p *byte)
	i8(p *int8)
	bool(p *bool)
	u16(p *uint16)
	u64(p *uint64)
	i64(p *int64)
	int(p *int)
	reading() bool
	fail(err error)
}

type snapWriter struct {
	w   io.Writer
	err error
}

func (s *snapWriter) reading() bool { return false }
func (s *snapWriter) fail(e error)  { s.setErr(e) }
func (s *snapWriter) setErr(e error) {
	if s.err == nil {
		s.err = e
	}
}
func (s *snapWriter) bytes(b []byte) {
	if s.err == nil {
		_, err := s.w.Write(b)
		s.setErr(err)
	}
}
func (s *snapWriter) byte(p *byte) { s.bytes([]byte{*p}) }
func (s *snapWriter) i8(p *int8)   { s.bytes([]byte{byte(*p)}) }
func (s *snapWriter) bool(p *bool) {
	var b byte
	if *p {
		b = 1
	}
	s.bytes([]byte{b})
}
func (s *snapWriter) u16(p *uint16) {
	var b [2]byte
	binary.LittleEndian.PutUint16(b[:], *p)
	s.bytes(b[:])
}
func (s *snapWriter) u64(p *uint64) {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], *p)
	s.bytes(b[:])
}
func (s *snapWriter) i64(p *int64) { v := uint64(*p); s.u64(&v) }
func (s *snapWriter) int(p *int)   { v := int64(*p); s.i64(&v) }
func (s *snapWriter) u32(v uint32) {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	s.bytes(b[:])
}
func (s *snapWriter) str(v string) {
	s.u32(uint32(len(v)))
	s.bytes([]byte(v))
}

type snapReader struct {
	r   io.Reader
	err error
}

func (s *snapReader) reading() bool { return true }
func (s *snapReader) fail(e error)  { s.setErr(e) }
func (s *snapReader) setErr(e error) {
	if s.err == nil {
		s.err = e
	}
}
func (s *snapReader) bytes(b []byte) {
	if s.err == nil {
		_, err := io.ReadFull(s.r, b)
		s.setErr(err)
	}
}
func (s *snapReader) byte(p *byte) {
	var b [1]byte
	s.bytes(b[:])
	*p = b[0]
}
func (s *snapReader) i8(p *int8) {
	var b [1]byte
	s.bytes(b[:])
	*p = int8(b[0])
}
func (s *snapReader) bool(p *bool) {
	var b [1]byte
	s.bytes(b[:])
	*p = b[0] != 0
}
func (s *snapReader) u16(p *uint16) {
	var b [2]byte
	s.bytes(b[:])
	*p = binary.LittleEndian.Uint16(b[:])
}
func (s *snapReader) u64(p *uint64) {
	var b [8]byte
	s.bytes(b[:])
	*p = binary.LittleEndian.Uint64(b[:])
}
func (s *snapReader) i64(p *int64) {
	var v uint64
	s.u64(&v)
	*p = int64(v)
}
func (s *snapReader) int(p *int) {
	var v int64
	s.i64(&v)
	*p = int(v)
}
func (s *snapReader) u32() uint32 {
	var b [4]byte
	s.bytes(b[:])
	return binary.LittleEndian.Uint32(b[:])
}
func (s *snapReader) str() string {
	n := s.u32()
	if s.err != nil || n > 1<<16 {
		return ""
	}
	b := make([]byte, n)
	s.bytes(b)
	return string(b)
}
