// Package z80 is the machine that statically translated cartridge code runs
// against.
//
// It is deliberately not an emulator: there is no fetch/decode loop and no
// cycle counting. The cartridge's instructions were translated to Go ahead of
// time by msx2go; what lives here is the state those instructions read and
// write -- registers, flags, an address space with the cartridge's own mapper
// paging it, the video and sound chips, and shims for the BIOS entry points
// the cartridge calls.
//
// The Z80 stack is modelled explicitly rather than mapped onto Go call
// frames, because cartridge code treats return addresses as data: a jump-table
// dispatcher pops its own return address to find the table that follows the
// call, and the handler's ret then unwinds two levels. Go's own stack cannot
// express that.
package z80

import "fmt"

// sentinel is pushed by Run and marks the point at which it should return.
const sentinel = 0xFFFF

// M is the machine state.
type M struct {
	A, B, C, D, E, H, L    byte
	Fs, Fz, Fh, Fp, Fn, Fc bool

	// Shadow set, swapped by ex af,af' and exx.
	A2, B2, C2, D2, E2, H2, L2   byte
	Fs2, Fz2, Fh2, Fp2, Fn2, Fc2 bool

	IX, IY, SP, PC uint16
	IFF            bool
	IM             int
	halted         bool
	idle           bool
	booting        bool
	inISR          bool
	bootIRQs       int
	rReg           byte
	frames         int

	// Mem is the address space as the Z80 sees it right now, cartridge
	// pages included -- see memory.go for why the mapper copies rather
	// than indirects.
	Mem [0x10000]byte

	// mem carries the cartridge image and the paging over it.
	mem memory

	dispatchSeen    map[string]bool
	dispatchStopped bool

	// discovered is filled by the discovery build: every address reached
	// that had no label. See nolabel_discover.go.
	discovered   []string
	fallbackSeen map[string]bool

	// holes are the runs msx2go pruned away, consulted only by a build
	// with the msxcheck tag. See read_check.go.
	holes   []hole
	holesOn bool

	VDP VDP
	PSG PSG
	// SCC is the sound chip Konami put in the mapper, present only on a
	// cartridge whose mapper says so. See scc.go.
	SCC SCC

	// Keys is the keyboard matrix as SNSMAT returns it: one byte per row,
	// a 0 bit meaning pressed.
	Keys [12]byte

	// PrimarySlot is the slot register a cartridge reads to find out where
	// it is. See bios.go: there is one flat address space here, so the
	// slots are a fiction, but a consistent one.
	PrimarySlot byte

	// Trace, when set, is called before each BIOS shim. Useful when a port
	// diverges and you want to see what the cartridge asked the hardware
	// to do.
	Trace func(what string, a, b uint16)

	// WatchWrites, when set, is called for every RAM write. Point it at a
	// range and the cartridge tells you what it keeps there, in the order
	// it fills it, which is far quicker than reading a disassembly and
	// guessing.
	WatchWrites func(addr uint16, v byte)

	// OnBank, when set, is called for every bank-register write, before it
	// takes effect. A mapper is the one part of this machine a cartridge
	// can get wrong invisibly -- the bytes are simply someone else's -- so
	// being able to watch it is worth the field.
	OnBank func(page, bank int)

	// BiosTrace, when set, is called with the entry point of every BIOS
	// call, before the shim runs. The registers are still whatever the
	// cartridge set, which is how a call site says which routine it thinks
	// it is calling.
	BiosTrace func(addr uint16)

	// Cyc counts T-states. It is what tells this machine that a handler
	// overran its frame, which is the only way a game tuned around that
	// overrun runs at the speed it was tuned for. See cycles.go.
	Cyc uint64

	// credit is the frame budget not yet spent, in T-states. It goes
	// negative when a handler overruns and the interrupts that arrive
	// while it is still running are the ones the game's own re-entry
	// guard throws away.
	credit int64

	// lastIRQ is the cycle count at which the last interrupt was taken,
	// irqTaken how many have been taken mid-flight, and nest how deep they
	// are stacked right now. See cycles.go.
	lastIRQ  uint64
	irqTaken int
	nest     int

	// ppiC is the 8255's port C: its low nibble selects which keyboard row
	// port B reads. See in and out.
	ppiC byte

	// LearnSites makes the interpreter write down every address it
	// executes, for feeding back into msx2go: what ran interpreted this
	// generation is translated code in the next. See learnPC.
	LearnSites bool

	learned map[string]bool

	// interpDepth is how many Interpret loops are on the Go stack: whether
	// a clean instruction-boundary stop is available. See dueIRQ.
	interpDepth int

	// bootStop asks the interpreter to stop at the next instruction
	// boundary: INIT has been recognised as the game's main loop and Boot
	// should hand it back whole. See dueIRQ and Interpret.
	bootStop bool

	// frameOrigin is the cycle the current frame began at, for deriving
	// where in the frame the raster would be. See status2.
	frameOrigin uint64

	// fDue is when the next vertical blank comes due on the clock, for the
	// main-thread shape of cartridge. See dueVblank.
	fDue uint64

	// fhDue is when this frame's line interrupt comes due, in cycles. See
	// armLine.
	fhDue uint64

	// Disk is the floppy this machine booted from, where it booted from
	// one. The cartridge shape of machine leaves it nil.
	Disk *Disk
	// images are every floppy the machine was given and inDrive says
	// which is in each drive; curDrive is the one the disk calls act
	// on. A three-disk game is three images and one drive. See disks.go.
	images   []*Disk
	inDrive  []int
	curDrive int
	// diskSwapped is set when a floppy is changed, for the disk ROM's
	// "has the disk been swapped" call to report once. See diskROM.
	diskSwapped bool
	// dosProgram says a program loaded by the disk operating system is
	// running, which changes what page zero holds. See hdboot.go.
	dosProgram bool
	// cwd is the directory the disk calls look in, which a hard disk's
	// batch file changes and a floppy never has. See hdboot.go.
	cwd int
	// The memory mapper: which segment is in each page, and the bytes
	// of the segments that are not. See rammapper.go.
	ramSeg   [4]int
	ramStore [][]byte

	// dma is where the disk function calls read to and write from, which
	// a program sets with function 1Ah. MSX-DOS starts it at 0080h.
	dma uint16

	// warnedBIOS remembers which unimplemented entry points have already
	// been complained about, so a game that polls one does not fill the
	// terminal with the same line.
	warnedBIOS map[uint16]bool

	// DiskRun names the BASIC program a floppy should start with, for a
	// disk that has no AUTOEXEC.BAS and more than one to choose from.
	// Empty means work it out. See bootProgram.
	DiskRun string

	// MemTrace, when set, is told every write to memory. It is a
	// diagnostic and costs a branch on the hottest path there is, so it
	// stays nil unless something is being chased.
	MemTrace func(a uint16, v byte)

	// IRQTrace, when set, is told what the line interrupt is doing: armed
	// for a raster line, raised, held because interrupts were off, or
	// dropped because the raster had already gone by.
	IRQTrace func(what string, line int)

	// DOSTrace, when set, is told every disk function call.
	DOSTrace func(fn byte, de uint16)

	// files are the disk files a program has open, by the address of the
	// control block it opened each with.
	files map[uint16]*dosFile

	// searchFor and searchAt carry a directory search between the call
	// that starts one and the calls that continue it.
	searchFor string
	searchAt  int

	// runMark is the stack level the machine entered translated code at.
	// The interpreter needs it when it takes over partway through: see
	// noLabel.
	runMark uint16

	// fHeld is a vertical blank that came due while the cartridge was
	// inside `di` and has not been taken yet. See mainThreadFrame.
	fHeld bool

	// fhHeld is a line interrupt that has come due and not yet been
	// taken, because the cartridge was inside `di` when the raster
	// reached the line. It stands until the handler reads S#1. See
	// dueLine.
	fhHeld bool

	// cycLimit, when non-zero, stops Interpret at a cycle count: it is how
	// a main-thread game is held to one frame of work per frame. See
	// InterpretRun.
	cycLimit uint64

	// lastDeliver is m.Cyc when an interrupt was last delivered by any
	// route. See dueIRQ.
	lastDeliver uint64

	// MainThread says the cartridge's game loop is INIT itself, still
	// running, rather than an interrupt handler called once per frame. See
	// Boot.
	MainThread bool
	// keySeen is the character CHGET already delivered for the press
	// still held, so a key reads once per press. See keyEvent.
	keySeen byte
	// loadLo and loadHi bound every BLOAD into RAM, which is where a
	// disk's code lives and what its translation covers. transStale
	// says the bytes there no longer match what was translated -- an
	// edited floppy -- and the machine must interpret instead.
	loadLo, loadHi uint16
	transStale     bool
	// bridgeDepth counts nested interpreter-to-translation crossings,
	// so a bridged run knows to respect the frame's cycle budget at
	// its returns. See bridgeInto and retBail.
	bridgeDepth int

	// frameStart is m.Cyc when the current frame began, so a frame that
	// never ends can be noticed. See FrameRunaway.
	frameStart uint64

	// OnRunaway, when set, is called instead of reporting an abandoned
	// frame to stderr.
	OnRunaway func(pc uint16, banks []int)

	// Executed counts interpreted instructions. It is the only measure of
	// how much work a frame cost that this machine has -- see interp.go --
	// and a cartridge whose handler overruns a frame on real hardware needs
	// it to know that it did.
	Executed uint64

	// CPUScale multiplies the cycles a frame is allowed. 1 is a stock MSX;
	// higher is a faster processor, which stops a heavy handler overrunning
	// its frame; negative turns the accounting off altogether, which is a
	// machine outside of time and runs games tuned around the overrun too
	// fast. See cycles.go.
	CPUScale float64

	// Hz is the machine's vertical frequency: 60 for an NTSC machine, 50
	// for a PAL one. It is the rate the frame loop should tick at, and it
	// is what a cartridge is told when it reads the locale byte. Setting
	// one without the other gives a game whose idea of a second disagrees
	// with the machine's.
	Hz int

	// Fussy makes an address with no label a panic rather than a hand-off
	// to the interpreter. See nolabel.go: it is the right setting while a
	// cartridge is being taught and the wrong one for playing it.
	Fussy bool

	// Observe, when set, is called before each *interpreted* instruction.
	// Translated code does not go through it; see interp.go.
	Observe Observer
}

// --- register pairs -------------------------------------------------------

func (m *M) BC() uint16 { return uint16(m.B)<<8 | uint16(m.C) }
func (m *M) DE() uint16 { return uint16(m.D)<<8 | uint16(m.E) }
func (m *M) HL() uint16 { return uint16(m.H)<<8 | uint16(m.L) }

func (m *M) setBC(v uint16) { m.B, m.C = byte(v>>8), byte(v) }
func (m *M) setDE(v uint16) { m.D, m.E = byte(v>>8), byte(v) }

// SetDE seeds the DE register pair, for calling a ROM routine directly.
func (m *M) SetDE(v uint16) { m.setDE(v) }
func (m *M) setHL(v uint16) { m.H, m.L = byte(v>>8), byte(v) }

func (m *M) AF() uint16 { return uint16(m.A)<<8 | uint16(m.flags()) }

// cursorAsStick reads the cursor keys as though they were a joystick, which
// is what GTSTCK does when asked for stick zero. Row eight of the key matrix
// holds the four arrows; the sticks report on port A of the sound chip in a
// different order, and both are active low.
func (m *M) cursorAsStick() byte {
	row := m.Keys[8]
	out := byte(0xFF)
	for _, b := range [4]struct{ key, stick uint }{
		{5, 0}, // up
		{6, 1}, // down
		{4, 2}, // left
		{7, 3}, // right
	} {
		if row&(1<<b.key) == 0 {
			out &^= 1 << b.stick
		}
	}
	return out
}

func (m *M) setAF(v uint16) {
	m.A = byte(v >> 8)
	m.setFlags(byte(v))
}

func (m *M) flags() byte {
	var f byte
	if m.Fs {
		f |= 0x80
	}
	if m.Fz {
		f |= 0x40
	}
	if m.Fh {
		f |= 0x10
	}
	if m.Fp {
		f |= 0x04
	}
	if m.Fn {
		f |= 0x02
	}
	if m.Fc {
		f |= 0x01
	}
	return f
}

func (m *M) setFlags(f byte) {
	m.Fs = f&0x80 != 0
	m.Fz = f&0x40 != 0
	m.Fh = f&0x10 != 0
	m.Fp = f&0x04 != 0
	m.Fn = f&0x02 != 0
	m.Fc = f&0x01 != 0
}

func (m *M) exAF() {
	m.A, m.A2 = m.A2, m.A
	m.Fs, m.Fs2 = m.Fs2, m.Fs
	m.Fz, m.Fz2 = m.Fz2, m.Fz
	m.Fh, m.Fh2 = m.Fh2, m.Fh
	m.Fp, m.Fp2 = m.Fp2, m.Fp
	m.Fn, m.Fn2 = m.Fn2, m.Fn
	m.Fc, m.Fc2 = m.Fc2, m.Fc
}

func (m *M) exx() {
	m.B, m.B2 = m.B2, m.B
	m.C, m.C2 = m.C2, m.C
	m.D, m.D2 = m.D2, m.D
	m.E, m.E2 = m.E2, m.E
	m.H, m.H2 = m.H2, m.H
	m.L, m.L2 = m.L2, m.L
}

func (m *M) exDEHL() {
	m.D, m.H = m.H, m.D
	m.E, m.L = m.L, m.E
}

func (m *M) exSPHL() {
	v := m.rd16(m.SP)
	m.wr16(m.SP, m.HL())
	m.setHL(v)
}

func (m *M) exSP(r *uint16) {
	v := m.rd16(m.SP)
	m.wr16(m.SP, *r)
	*r = v
}

// Undocumented index-register halves. Rare, but Konami code does use them.
func (m *M) ixh() byte     { return byte(m.IX >> 8) }
func (m *M) ixl() byte     { return byte(m.IX) }
func (m *M) iyh() byte     { return byte(m.IY >> 8) }
func (m *M) iyl() byte     { return byte(m.IY) }
func (m *M) setIXh(v byte) { m.IX = uint16(v)<<8 | m.IX&0xFF }
func (m *M) setIXl(v byte) { m.IX = m.IX&0xFF00 | uint16(v) }
func (m *M) setIYh(v byte) { m.IY = uint16(v)<<8 | m.IY&0xFF }
func (m *M) setIYl(v byte) { m.IY = m.IY&0xFF00 | uint16(v) }

// --- flag helpers ---------------------------------------------------------

func parity(v byte) bool {
	v ^= v >> 4
	v ^= v >> 2
	v ^= v >> 1
	return v&1 == 0
}

func (m *M) sz(v byte) {
	m.Fs = v&0x80 != 0
	m.Fz = v == 0
}

// --- 8-bit arithmetic -----------------------------------------------------

func (m *M) aluAdd(n byte) { m.addc(n, 0) }
func (m *M) aluAdc(n byte) { m.addc(n, boolByte(m.Fc)) }
func (m *M) aluSub(n byte) { m.subc(n, 0) }
func (m *M) aluSbc(n byte) { m.subc(n, boolByte(m.Fc)) }

func (m *M) addc(n, carry byte) {
	r := uint16(m.A) + uint16(n) + uint16(carry)
	half := (m.A & 0x0F) + (n & 0x0F) + carry
	res := byte(r)
	m.Fh = half > 0x0F
	m.Fc = r > 0xFF
	m.Fp = (m.A^n)&0x80 == 0 && (m.A^res)&0x80 != 0
	m.Fn = false
	m.A = res
	m.sz(res)
}

func (m *M) subc(n, carry byte) {
	r := int16(m.A) - int16(n) - int16(carry)
	res := byte(r)
	m.Fh = int8(m.A&0x0F)-int8(n&0x0F)-int8(carry) < 0
	m.Fc = r < 0
	m.Fp = (m.A^n)&0x80 != 0 && (m.A^res)&0x80 != 0
	m.Fn = true
	m.A = res
	m.sz(res)
}

func (m *M) aluAnd(n byte) {
	m.A &= n
	m.Fh, m.Fn, m.Fc = true, false, false
	m.Fp = parity(m.A)
	m.sz(m.A)
}

func (m *M) aluOr(n byte) {
	m.A |= n
	m.Fh, m.Fn, m.Fc = false, false, false
	m.Fp = parity(m.A)
	m.sz(m.A)
}

func (m *M) aluXor(n byte) {
	m.A ^= n
	m.Fh, m.Fn, m.Fc = false, false, false
	m.Fp = parity(m.A)
	m.sz(m.A)
}

// aluCp is sub without keeping the result.
func (m *M) aluCp(n byte) {
	a := m.A
	m.subc(n, 0)
	m.A = a
}

func (m *M) inc8(v byte) byte {
	r := v + 1
	m.Fh = v&0x0F == 0x0F
	m.Fp = v == 0x7F
	m.Fn = false
	m.sz(r)
	return r
}

func (m *M) dec8(v byte) byte {
	r := v - 1
	m.Fh = v&0x0F == 0
	m.Fp = v == 0x80
	m.Fn = true
	m.sz(r)
	return r
}

func (m *M) neg() {
	a := m.A
	m.A = 0
	m.subc(a, 0)
}

// --- 16-bit arithmetic ----------------------------------------------------

func (m *M) addHL(n uint16) { m.setHL(m.add16(m.HL(), n)) }

func (m *M) add16(a, n uint16) uint16 {
	r := uint32(a) + uint32(n)
	m.Fh = (a&0x0FFF)+(n&0x0FFF) > 0x0FFF
	m.Fc = r > 0xFFFF
	m.Fn = false
	return uint16(r)
}

func (m *M) adcHL(n uint16) {
	hl := m.HL()
	c := uint32(boolByte(m.Fc))
	r := uint32(hl) + uint32(n) + c
	res := uint16(r)
	m.Fh = (hl&0x0FFF)+(n&0x0FFF)+uint16(c) > 0x0FFF
	m.Fc = r > 0xFFFF
	m.Fp = (hl^n)&0x8000 == 0 && (hl^res)&0x8000 != 0
	m.Fn = false
	m.Fs = res&0x8000 != 0
	m.Fz = res == 0
	m.setHL(res)
}

func (m *M) sbcHL(n uint16) {
	hl := m.HL()
	c := int32(boolByte(m.Fc))
	r := int32(hl) - int32(n) - c
	res := uint16(r)
	m.Fh = int32(hl&0x0FFF)-int32(n&0x0FFF)-c < 0
	m.Fc = r < 0
	m.Fp = (hl^n)&0x8000 != 0 && (hl^res)&0x8000 != 0
	m.Fn = true
	m.Fs = res&0x8000 != 0
	m.Fz = res == 0
	m.setHL(res)
}

// --- rotates and shifts ---------------------------------------------------

func (m *M) rlca() {
	m.A = m.A<<1 | m.A>>7
	m.Fc = m.A&1 != 0
	m.Fh, m.Fn = false, false
}

func (m *M) rrca() {
	m.Fc = m.A&1 != 0
	m.A = m.A>>1 | m.A<<7
	m.Fh, m.Fn = false, false
}

func (m *M) rla() {
	c := boolByte(m.Fc)
	m.Fc = m.A&0x80 != 0
	m.A = m.A<<1 | c
	m.Fh, m.Fn = false, false
}

func (m *M) rra() {
	c := boolByte(m.Fc)
	m.Fc = m.A&1 != 0
	m.A = m.A>>1 | c<<7
	m.Fh, m.Fn = false, false
}

func (m *M) rotFlags(r byte) byte {
	m.sz(r)
	m.Fp = parity(r)
	m.Fh, m.Fn = false, false
	return r
}

func (m *M) rlc(v byte) byte {
	m.Fc = v&0x80 != 0
	return m.rotFlags(v<<1 | v>>7)
}

func (m *M) rrc(v byte) byte {
	m.Fc = v&1 != 0
	return m.rotFlags(v>>1 | v<<7)
}

func (m *M) rl(v byte) byte {
	c := boolByte(m.Fc)
	m.Fc = v&0x80 != 0
	return m.rotFlags(v<<1 | c)
}

func (m *M) rr(v byte) byte {
	c := boolByte(m.Fc)
	m.Fc = v&1 != 0
	return m.rotFlags(v>>1 | c<<7)
}

func (m *M) sla(v byte) byte {
	m.Fc = v&0x80 != 0
	return m.rotFlags(v << 1)
}

func (m *M) sra(v byte) byte {
	m.Fc = v&1 != 0
	return m.rotFlags(v&0x80 | v>>1)
}

func (m *M) sll(v byte) byte { // undocumented
	m.Fc = v&0x80 != 0
	return m.rotFlags(v<<1 | 1)
}

func (m *M) srl(v byte) byte {
	m.Fc = v&1 != 0
	return m.rotFlags(v >> 1)
}

func (m *M) bit(n int, v byte) {
	set := v&(1<<uint(n)) != 0
	m.Fz = !set
	m.Fs = n == 7 && set
	m.Fp = !set
	m.Fh, m.Fn = true, false
}

func (m *M) cpl() {
	m.A = ^m.A
	m.Fh, m.Fn = true, true
}

func (m *M) scf() {
	m.Fc, m.Fh, m.Fn = true, false, false
}

func (m *M) ccf() {
	m.Fh = m.Fc
	m.Fc = !m.Fc
	m.Fn = false
}

func (m *M) daa() {
	a := m.A
	var corr byte
	carry := m.Fc
	if m.Fh || (!m.Fn && a&0x0F > 9) {
		corr |= 0x06
	}
	if m.Fc || (!m.Fn && a > 0x99) {
		corr |= 0x60
		carry = true
	}
	if m.Fn {
		m.Fh = m.Fh && a&0x0F < 6
		m.A = a - corr
	} else {
		m.Fh = a&0x0F > 9
		m.A = a + corr
	}
	m.Fc = carry
	m.Fp = parity(m.A)
	m.sz(m.A)
}

func (m *M) rrd() {
	v := m.rd(m.HL())
	m.wr(m.HL(), v>>4|m.A<<4)
	m.A = m.A&0xF0 | v&0x0F
	m.Fp = parity(m.A)
	m.Fh, m.Fn = false, false
	m.sz(m.A)
}

func (m *M) rld() {
	v := m.rd(m.HL())
	m.wr(m.HL(), v<<4|m.A&0x0F)
	m.A = m.A&0xF0 | v>>4
	m.Fp = parity(m.A)
	m.Fh, m.Fn = false, false
	m.sz(m.A)
}

// --- block moves ----------------------------------------------------------

func (m *M) ldStep(delta int) {
	m.wr(m.DE(), m.rd(m.HL()))
	m.setHL(uint16(int(m.HL()) + delta))
	m.setDE(uint16(int(m.DE()) + delta))
	m.setBC(m.BC() - 1)
	m.Fp = m.BC() != 0
	m.Fh, m.Fn = false, false
}

func (m *M) ldi() { m.ldStep(1) }
func (m *M) ldd() { m.ldStep(-1) }

func (m *M) ldir() {
	for {
		m.ldStep(1)
		if m.BC() == 0 {
			return
		}
		m.tick(cycBlockRepeat)
	}
}

func (m *M) lddr() {
	for {
		m.ldStep(-1)
		if m.BC() == 0 {
			return
		}
		m.tick(cycBlockRepeat)
	}
}

func (m *M) cpStep(delta int) {
	v := m.rd(m.HL())
	a := m.A
	m.subc(v, 0)
	m.A = a
	m.setHL(uint16(int(m.HL()) + delta))
	m.setBC(m.BC() - 1)
	m.Fp = m.BC() != 0
}

// The block I/O group: a byte between memory and a port, with B as the count
// and HL walking. Konami's VRAM writers use OTIR, which is the fastest way to
// get bytes into the VDP a Z80 has.
func (m *M) ioStep(delta int, out bool) {
	if out {
		m.out(m.C, m.rd(m.HL()))
	} else {
		m.wr(m.HL(), m.in(m.C))
	}
	m.setHL(uint16(int(m.HL()) + delta))
	m.B = m.dec8(m.B)
	m.Fn = true
	m.Fz = m.B == 0
}

func (m *M) outi() { m.ioStep(1, true) }
func (m *M) outd() { m.ioStep(-1, true) }
func (m *M) ini()  { m.ioStep(1, false) }
func (m *M) ind()  { m.ioStep(-1, false) }

func (m *M) otir() {
	for {
		m.outi()
		if m.B == 0 {
			return
		}
		m.tick(cycBlockRepeat)
	}
}

func (m *M) otdr() {
	for {
		m.outd()
		if m.B == 0 {
			return
		}
		m.tick(cycBlockRepeat)
	}
}

func (m *M) inir() {
	for {
		m.ini()
		if m.B == 0 {
			return
		}
		m.tick(cycBlockRepeat)
	}
}

func (m *M) indr() {
	for {
		m.ind()
		if m.B == 0 {
			return
		}
		m.tick(cycBlockRepeat)
	}
}

func (m *M) cpi() { m.cpStep(1) }
func (m *M) cpd() { m.cpStep(-1) }

func (m *M) cpir() {
	for {
		m.cpStep(1)
		if m.Fz || m.BC() == 0 {
			return
		}
		m.tick(cycBlockRepeat)
	}
}

func (m *M) cpdr() {
	for {
		m.cpStep(-1)
		if m.Fz || m.BC() == 0 {
			return
		}
		m.tick(cycBlockRepeat)
	}
}

// --- I/O ------------------------------------------------------------------

func (m *M) out(port, v byte) {
	if page, ok := ramMapperPort(port); ok && m.hasRAMMapper() {
		m.setRAMSegment(page, int(v))
		return
	}
	switch port {
	case 0x98:
		m.VDP.WriteData(v)
	case 0x99:
		m.VDP.WriteCtrl(v)
	// The two the V9938 adds: the palette, and register writes aimed by
	// register 17. An MSX2 cartridge sets the chip up almost entirely
	// through 9Bh -- Space Manbow writes registers 0 to 7 directly not
	// once -- so a machine that drops these has a video chip nobody has
	// configured.
	case 0x9A:
		m.VDP.WritePalette(v)
	case 0x9B:
		m.VDP.WriteIndirect(v)
	case 0xA0:
		m.PSG.Latch = v
	case 0xA1:
		m.PSG.Write(m.PSG.Latch, v)

	case 0xA8:
		// The slots are a fiction here -- everything is already paged in
		// -- but a consistent one, so the value reads back.
		m.PrimarySlot = v
	case 0xAA:
		m.ppiC = v
	case 0xAB:
		// The 8255's bit set/reset: bit 0 is the value, bits 1 to 3 the
		// bit of port C to apply it to. This is how the BIOS selects a
		// keyboard row one bit at a time.
		bit := uint(v>>1) & 7
		if v&1 != 0 {
			m.ppiC |= 1 << bit
		} else {
			m.ppiC &^= 1 << bit
		}
	}
}

func (m *M) in(port byte) byte {
	if page, ok := ramMapperPort(port); ok && m.hasRAMMapper() {
		return m.ramSegmentOf(page)
	}
	switch port {
	case 0x98:
		return m.VDP.ReadData()
	case 0x99:
		return m.VDP.ReadStatus()
	case 0xA2:
		return m.PSG.Read(m.PSG.Latch)

	// The PPI. Port A is the primary slot register, port B reads the
	// keyboard row port C selected, and port C also carries the caps lamp,
	// the key click and the cassette motor -- none of which anything here
	// can do anything about, but which are written and read back.
	//
	// A cartridge is not obliged to go through SNSMAT for its keyboard and
	// plenty do not: Space Manbow reads port A9h directly. Returning FFh
	// from an unimplemented port is "no key pressed, forever", which is a
	// silent way to make a game unplayable.
	case 0xA8:
		return m.PrimarySlot
	case 0xA9:
		return m.Keys[m.ppiC&0x0F]
	case 0xAA:
		return m.ppiC
	}
	return 0xFF
}

// outC and inC back `out (c),a` / `in a,(c)`, which the ROM's VRAM writers
// use after pointing C at the VDP data port.
func (m *M) outC(v byte) { m.out(m.C, v) }
func (m *M) inC() byte   { return m.in(m.C) }

// refreshR is `ld a,r`: the DRAM refresh register, whose low seven bits
// count instruction fetches and which games read as a free random number --
// Salamander aims its title-screen debris with it.
//
// This machine executes no fetches, so the true value is unreachable; what a
// game needs from R is that consecutive reads differ unpredictably-enough,
// and a full-period odd stride over the seven bits gives it that. It will
// not match a cycle-accurate machine read for read -- R is a cycle counter,
// and that is the known ceiling -- but a game seeded from it stays inside
// the behaviour its designers shipped, instead of the one degenerate path a
// constant picks.
func (m *M) refreshR() byte {
	m.rReg = (m.rReg + 41) & 0x7F
	return m.rReg
}

// --- error paths ----------------------------------------------------------

func (m *M) unsupported(a uint16) {
	panic(fmt.Sprintf("z80: unsupported instruction at %04Xh", a))
}

// rst is a one-byte call to a page-zero vector, which on the MSX is the BIOS.
//
// A game rarely uses one -- CALLF at 30h for an inter-slot call is the
// exception -- so reaching one is as often padding decoded as code as it is
// real. Either way the shim says which it was, by name.
// rst is a restart, which on this machine is always a BIOS entry: page zero
// holds shims rather than an image of the ROM.
//
// The return address goes on the stack first and comes back off after, the
// way the hardware does it. That is not bookkeeping: CALLF at 0030h reads
// its arguments *from* the return address -- the slot and the address to
// call follow the restart inline -- and steps the address past them before
// returning. Dispatching without pushing left it reading whatever the stack
// happened to hold, and King's Valley II, whose whole start goes through
// that restart, walked off into page zero and NOPped its way up into the
// cartridge.
func (m *M) rst(n byte) {
	m.push(m.PC)
	mark := m.SP
	m.bios(uint16(n))
	if m.SP > mark {
		// What the restart reached abandoned the stack: it reset the
		// stack pointer and went its own way, which is exactly how a
		// cartridge started from the BIOS' take-over hook begins --
		// King's Valley II sets its own stack in its second
		// instruction. There is no return address to pop any more,
		// and popping the stack it has just built puts the machine
		// wherever that data happens to point.
		return
	}
	m.PC = m.pop()
}

func boolByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}
