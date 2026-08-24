package z80

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// A fetch/decode/execute loop, which is the one thing the rest of this
// package deliberately does not have.
//
// It is here for two reasons, and neither is emulation for its own sake.
//
// The first is discovery. A static trace can only follow a jump whose target
// it can work out; at `jp (hl)` it stops, and everything reachable only
// through that jump is code msx2go never translated. Running the cartridge
// answers those instructions by observation instead of inference -- whatever
// address the jump actually went to is, by construction, code. One run over
// the real game finds more than any number of rounds of translating, crashing
// and translating again, and it has no coverage ceiling, because the
// interpreter can execute code the translator has never seen.
//
// The second is that it makes the translated build safe. An address with no
// label used to be a panic; now it is a hand-off. See nolabel.go.
//
// Every instruction here dispatches onto the same helpers the translated code
// calls -- m.aluAdd, m.inc8, m.ldir and the rest -- so the two cannot compute
// a different answer. What is not shared is only the decoding.

// maxInterpSteps stops a wrecked trajectory from spinning forever. A frame of
// real work is a few thousand instructions; this is four orders of magnitude
// of headroom.
const maxInterpSteps = 50_000_000

// Observe, when set, is called before each interpreted instruction with the
// address about to execute and the bank in each page register. This is the
// discovery hook; see cmd/msx2go/observe.go.
type Observer func(pc uint16, banks []int)

// Interpret runs from m.PC until the stack unwinds past the mark, the machine
// goes idle, or steps instructions have run. It reports the number executed.
//
// The mark is a stack pointer: when SP rises above it the routine that was
// entered has returned, which is how a hand-off from translated code knows it
// is finished. Pass 0 to run until idle.
func (m *M) Interpret(mark uint16, steps int) int {
	m.interpDepth++
	defer func() { m.interpDepth-- }()
	n := 0
	for ; n < steps; n++ {
		if m.idle {
			break
		}
		if m.halted {
			// A halt is not an idle loop: the processor waits for
			// the next interrupt and then continues. Control goes
			// back to whoever is running frames, which is where
			// interrupts come from.
			break
		}
		if mark != 0 && m.SP > mark {
			break
		}
		if m.PC == sentinel {
			break
		}
		if m.cycLimit != 0 && m.Cyc >= m.cycLimit {
			break
		}
		if m.bootStop {
			// The boot runaway asked for a clean hand-back. Here, at
			// the top of the loop, PC is on an instruction boundary.
			m.bootStop = false
			break
		}
		if m.Observe != nil {
			m.Observe(m.PC, m.mem.bank)
		}
		if m.LearnSites {
			m.learnPC()
		}
		op, spBefore := m.Mem[m.PC], m.SP
		m.Executed++
		if m.stepCatching() {
			// A handler hijacked the machine mid-instruction; the
			// half-done instruction belonged to a thread that no
			// longer exists. Continue, so the loop re-checks its
			// own marks against the new stack before running the
			// new thread's next instruction.
			continue
		}
		// The bridge: a call whose target is a translated label runs
		// translated, and comes back here when the routine returns --
		// the stack rising above the call's own level, which is this
		// loop's stopping rule too. Call boundaries are the only safe
		// crossing: the routine finds its real return address on the
		// stack, so code that reads it -- a threaded interpreter, a
		// dispatcher after the call site -- reads the truth.
		// Never start a crossing whose budget is already gone: a
		// bridged routine can only hand back at a ret, so entering
		// one past the limit spends a whole routine of somebody
		// else's frame. Let the interpreter's own check end it.
		if bridgeCall[op] && m.SP == spBefore-2 && m.PC >= 0x4000 &&
			(m.cycLimit == 0 || m.Cyc < m.cycLimit) &&
			m.canBridge() && labelAt(m.PC) {
			m.bridgeInto(m.PC, m.SP)
		}
	}
	return n
}

// bridgeCall is the call-shaped opcodes: `call nn` and its conditional
// forms. `rst` always lands in page zero, which is the BIOS, and never
// bridges.
var bridgeCall = [256]bool{
	0xCD: true, 0xC4: true, 0xCC: true, 0xD4: true, 0xDC: true,
	0xE4: true, 0xEC: true, 0xF4: true, 0xFC: true,
}

// canBridge says the translation may be entered from here: it is not
// stale, and the image is flat -- on a banked cartridge an address does
// not name an instruction without the paging, and the banked dispatch
// speaks offsets, not addresses.
func (m *M) canBridge() bool {
	if m.booting {
		// Boot is the interpreter's own dance: the runaway detector
		// that decides a cartridge's shape, the halt promotion, the
		// hand-back at an instruction boundary. None of it can reach
		// inside translated code, and INIT is the game loop itself
		// for a main-thread cartridge -- so a bridge here enters the
		// whole game and never comes back. Castle Excellent ran
		// twenty-nine seconds of machine time inside frame one.
		return false
	}
	return !m.transStale && m.mem.nbanks <= 1 && len(TranslatedAddrs) > 0
}

// labelAt reports whether the translation has a label at pc, from the
// published address list, spread into a table on first use.
var (
	labelSetOnce sync.Once
	labelSet     []bool
)

func labelAt(pc uint16) bool {
	labelSetOnce.Do(func() {
		if len(TranslatedAddrs) == 0 {
			return
		}
		labelSet = make([]bool, 0x10000)
		for _, a := range TranslatedAddrs {
			labelSet[a] = true
		}
	})
	return labelSet != nil && labelSet[pc]
}

func (m *M) f8() byte {
	v := m.rd(m.PC)
	m.PC++
	return v
}

func (m *M) f16() uint16 {
	v := m.rd16(m.PC)
	m.PC += 2
	return v
}

// reg8 is the byte register at index i of the opcode's 3-bit field, with H and
// L standing for the index register's halves when a DD or FD prefix is in
// force. Index 6 is (HL) and belongs to the caller, which has to fetch the
// displacement at the right moment.
func (m *M) reg8(i, pfx int) byte {
	switch i {
	case 0:
		return m.B
	case 1:
		return m.C
	case 2:
		return m.D
	case 3:
		return m.E
	case 4:
		switch pfx {
		case 1:
			return m.ixh()
		case 2:
			return m.iyh()
		}
		return m.H
	case 5:
		switch pfx {
		case 1:
			return m.ixl()
		case 2:
			return m.iyl()
		}
		return m.L
	case 7:
		return m.A
	}
	return 0
}

func (m *M) setReg8(i, pfx int, v byte) {
	switch i {
	case 0:
		m.B = v
	case 1:
		m.C = v
	case 2:
		m.D = v
	case 3:
		m.E = v
	case 4:
		switch pfx {
		case 1:
			m.setIXh(v)
		case 2:
			m.setIYh(v)
		default:
			m.H = v
		}
	case 5:
		switch pfx {
		case 1:
			m.setIXl(v)
		case 2:
			m.setIYl(v)
		default:
			m.L = v
		}
	case 7:
		m.A = v
	}
}

// hlAddr is (HL), or (IX+d) under a prefix, and fetches the displacement.
func (m *M) hlAddr(pfx int) uint16 {
	if pfx != 0 {
		m.tick(cycIndex)
	}
	switch pfx {
	case 1:
		return uint16(int(m.IX) + int(int8(m.f8())))
	case 2:
		return uint16(int(m.IY) + int(int8(m.f8())))
	}
	return m.HL()
}

// rp is the 16-bit pair at index i, BC DE HL SP, HL again standing for the
// index register under a prefix.
func (m *M) rp(i, pfx int) uint16 {
	switch i {
	case 0:
		return m.BC()
	case 1:
		return m.DE()
	case 2:
		switch pfx {
		case 1:
			return m.IX
		case 2:
			return m.IY
		}
		return m.HL()
	}
	return m.SP
}

func (m *M) setRP(i, pfx int, v uint16) {
	switch i {
	case 0:
		m.setBC(v)
	case 1:
		m.setDE(v)
	case 2:
		switch pfx {
		case 1:
			m.IX = v
		case 2:
			m.IY = v
		default:
			m.setHL(v)
		}
	default:
		m.SP = v
	}
}

func (m *M) cond(i int) bool {
	switch i {
	case 0:
		return !m.Fz
	case 1:
		return m.Fz
	case 2:
		return !m.Fc
	case 3:
		return m.Fc
	case 4:
		return !m.Fp
	case 5:
		return m.Fp
	case 6:
		return !m.Fs
	}
	return m.Fs
}

func (m *M) alu(op int, n byte) {
	switch op {
	case 0:
		m.aluAdd(n)
	case 1:
		m.aluAdc(n)
	case 2:
		m.aluSub(n)
	case 3:
		m.aluSbc(n)
	case 4:
		m.aluAnd(n)
	case 5:
		m.aluXor(n)
	case 6:
		m.aluOr(n)
	case 7:
		m.aluCp(n)
	}
}

// callTo is a call, taking the BIOS route when the target is not the
// cartridge's. Page 0 belongs to the BIOS on a real machine and there is no
// image of it here, only the shims in bios.go.
func (m *M) callTo(target uint16) {
	if m.isBIOS(target) {
		m.bios(target)
		return
	}
	m.push(m.PC)
	m.PC = target
}

// jumpTo is a jump, which a cartridge also uses to *call* the BIOS: `jp
// WRTPSG` runs the routine and lets its ret return to whoever called us. The
// emitter has the same rule under Run.BIOSTailCall.
func (m *M) jumpTo(target uint16) {
	if m.isBIOS(target) {
		m.bios(target)
		m.PC = m.pop()
		return
	}
	m.PC = target
}

func (m *M) isBIOS(a uint16) bool {
	if m.Disk != nil && len(m.mem.rom) == 0 &&
		a >= dskIO && a < dskLast && (a-dskIO)%3 == 0 {
		// The disk ROM's jump table, which lives in page one. A disk
		// machine has no cartridge there, so nothing else can own
		// these addresses; a cartridge machine never reaches here.
		return true
	}
	if m.dosProgram && interSlotEntry[a] {
		// A program running under the disk operating system has RAM
		// in page zero, not the BIOS -- but the inter-slot routines
		// still answer there, because the kernel keeps them working:
		// switching slots is how a program reaches anything outside
		// the sixty-four K it is running in, and it cannot do that
		// without them. Snatcher's loader calls ENASLT four
		// instructions into counting the machine's memory, and a
		// machine that let that run into empty RAM wandered back to
		// the program's start and began again, for ever.
		return true
	}
	if m.Disk != nil && a == dosBDOS {
		// Disk BASIC's function-call entry point lives in the work
		// area, above the ROM, so it needs naming rather than a range.
		return true
	}
	if a >= 0x4000 || m.mem.rom8k[a>>13] {
		return false
	}
	// Page zero is the BIOS only while the BIOS's slot is selected there.
	// A cartridge may switch it to RAM, copy routines in and call them --
	// Space Manbow does, and the reference machine reports page zero in
	// slot 3 while it runs them. Treating every low address as a BIOS
	// entry point runs a shim in place of the cartridge's own code, or
	// stops on "unimplemented BIOS call" at an address that was never a
	// BIOS call at all.
	//
	// Port A8h's lowest two bits are page zero's primary slot, and the
	// BIOS is slot zero. Nothing that leaves the register alone notices.
	return m.PrimarySlot&3 == 0
}

// stepCatching is step with the hijack unwind caught at the instruction
// boundary. It reports whether a hijack happened.
func (m *M) stepCatching() (hijack bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(hijacked); ok {
				hijack = true
				return
			}
			panic(r)
		}
	}()
	m.step()
	return false
}

func (m *M) step() {
	op := m.f8()
	pfx := 0
	for op == 0xDD || op == 0xFD {
		if op == 0xDD {
			pfx = 1
		} else {
			pfx = 2
		}
		m.tick(cycPrefix)
		op = m.f8()
	}
	m.tick(uint32(cycBase[op]))
	switch op {
	case 0xCB:
		m.stepCB(pfx)
		return
	case 0xED:
		m.stepED()
		return
	}
	switch {
	case op == 0x76: // halt, before ld r,r' claims it
		// The translated code sets the same flag and falls through, this
		// being a machine with no interrupts of its own. Here there is a
		// frame loop that can deliver one, so a halt also ends the run --
		// and PC is left after the halt, which is where the hardware
		// resumes.
		m.halted = true
		return
	case op >= 0x40 && op < 0x80: // ld r,r'
		dst, src := int(op>>3)&7, int(op)&7
		if dst == 6 {
			a := m.hlAddr(pfx)
			m.wr(a, m.reg8(src, 0))
		} else if src == 6 {
			a := m.hlAddr(pfx)
			m.setReg8(dst, 0, m.rd(a))
		} else {
			m.setReg8(dst, pfx, m.reg8(src, pfx))
		}
		return
	case op >= 0x80 && op < 0xC0: // alu a,r
		src := int(op) & 7
		var n byte
		if src == 6 {
			n = m.rd(m.hlAddr(pfx))
		} else {
			n = m.reg8(src, pfx)
		}
		m.alu(int(op>>3)&7, n)
		return
	}
	switch op {
	case 0x00: // nop
	case 0x08:
		m.exAF()
	case 0x10: // djnz
		d := int8(m.f8())
		m.B--
		if m.B != 0 {
			m.PC = uint16(int(m.PC) + int(d))
		}
	case 0x18:
		at := m.PC - 1
		d := int8(m.f8())
		m.jumpTo(uint16(int(m.PC) + int(d)))
		if m.PC == at {
			m.Idle()
		}
	case 0x20, 0x28, 0x30, 0x38: // jr cc,d
		d := int8(m.f8())
		if m.cond(int(op>>3) & 3) {
			m.PC = uint16(int(m.PC) + int(d))
		}
	case 0x01, 0x11, 0x21, 0x31:
		m.setRP(int(op>>4)&3, pfx, m.f16())
	case 0x09, 0x19, 0x29, 0x39: // add hl,rr
		i := int(op>>4) & 3
		m.setRP(2, pfx, m.add16(m.rp(2, pfx), m.rp(i, pfx)))
	case 0x02:
		m.wr(m.BC(), m.A)
	case 0x12:
		m.wr(m.DE(), m.A)
	case 0x0A:
		m.A = m.rd(m.BC())
	case 0x1A:
		m.A = m.rd(m.DE())
	case 0x22: // ld (nn),hl
		m.wr16(m.f16(), m.rp(2, pfx))
	case 0x2A:
		m.setRP(2, pfx, m.rd16(m.f16()))
	case 0x32:
		m.wr(m.f16(), m.A)
	case 0x3A:
		m.A = m.rd(m.f16())
	case 0x03, 0x13, 0x23, 0x33:
		i := int(op>>4) & 3
		m.setRP(i, pfx, m.rp(i, pfx)+1)
	case 0x0B, 0x1B, 0x2B, 0x3B:
		i := int(op>>4) & 3
		m.setRP(i, pfx, m.rp(i, pfx)-1)
	case 0x04, 0x0C, 0x14, 0x1C, 0x24, 0x2C, 0x34, 0x3C: // inc r
		i := int(op>>3) & 7
		if i == 6 {
			a := m.hlAddr(pfx)
			m.wr(a, m.inc8(m.rd(a)))
		} else {
			m.setReg8(i, pfx, m.inc8(m.reg8(i, pfx)))
		}
	case 0x05, 0x0D, 0x15, 0x1D, 0x25, 0x2D, 0x35, 0x3D: // dec r
		i := int(op>>3) & 7
		if i == 6 {
			a := m.hlAddr(pfx)
			m.wr(a, m.dec8(m.rd(a)))
		} else {
			m.setReg8(i, pfx, m.dec8(m.reg8(i, pfx)))
		}
	case 0x06, 0x0E, 0x16, 0x1E, 0x26, 0x2E, 0x36, 0x3E: // ld r,n
		i := int(op>>3) & 7
		if i == 6 {
			a := m.hlAddr(pfx) // displacement first, then the byte
			m.wr(a, m.f8())
		} else {
			m.setReg8(i, pfx, m.f8())
		}
	case 0x07:
		m.rlca()
	case 0x0F:
		m.rrca()
	case 0x17:
		m.rla()
	case 0x1F:
		m.rra()
	case 0x27:
		m.daa()
	case 0x2F:
		m.cpl()
	case 0x37:
		m.scf()
	case 0x3F:
		m.ccf()
	case 0xC0, 0xC8, 0xD0, 0xD8, 0xE0, 0xE8, 0xF0, 0xF8: // ret cc
		if m.cond(int(op>>3) & 7) {
			m.PC = m.pop()
		}
	case 0xC9:
		m.PC = m.pop()
	case 0xC2, 0xCA, 0xD2, 0xDA, 0xE2, 0xEA, 0xF2, 0xFA: // jp cc,nn
		t := m.f16()
		if m.cond(int(op>>3) & 7) {
			m.jumpTo(t)
		}
	case 0xC3:
		at := m.PC - 1
		t := m.f16()
		m.jumpTo(t)
		if m.PC == at {
			m.Idle()
		}
	case 0xC4, 0xCC, 0xD4, 0xDC, 0xE4, 0xEC, 0xF4, 0xFC: // call cc,nn
		t := m.f16()
		if m.cond(int(op>>3) & 7) {
			m.callTo(t)
		}
	case 0xCD:
		m.callTo(m.f16())
	case 0xC1, 0xD1, 0xE1, 0xF1: // pop
		v := m.pop()
		switch (op >> 4) & 3 {
		case 0:
			m.setBC(v)
		case 1:
			m.setDE(v)
		case 2:
			m.setRP(2, pfx, v)
		default:
			m.setAF(v)
		}
	case 0xC5, 0xD5, 0xE5, 0xF5: // push
		switch (op >> 4) & 3 {
		case 0:
			m.push(m.BC())
		case 1:
			m.push(m.DE())
		case 2:
			m.push(m.rp(2, pfx))
		default:
			m.push(m.AF())
		}
	case 0xC6, 0xCE, 0xD6, 0xDE, 0xE6, 0xEE, 0xF6, 0xFE: // alu a,n
		m.alu(int(op>>3)&7, m.f8())
	case 0xC7, 0xCF, 0xD7, 0xDF, 0xE7, 0xEF, 0xF7, 0xFF: // rst
		m.rst(op & 0x38)
	case 0xD3: // out (n),a
		m.out(m.f8(), m.A)
	case 0xDB:
		m.A = m.in(m.f8())
	case 0xD9:
		m.exx()
	case 0xE3: // ex (sp),hl
		switch pfx {
		case 1:
			m.exSP(&m.IX)
		case 2:
			m.exSP(&m.IY)
		default:
			m.exSPHL()
		}
	case 0xE9: // jp (hl)
		m.jumpTo(m.rp(2, pfx))
	case 0xEB:
		m.exDEHL()
	case 0xF3:
		m.IFF = false
	case 0xFB:
		m.IFF = true
	case 0xF9:
		m.SP = m.rp(2, pfx)
	default:
		m.unsupported(m.PC - 1)
	}
}

// R is not stepped per instruction here, though that is what the hardware
// does, because the translated build cannot count instructions and steps it
// only when `ld a,r` reads it (see refreshR). King's Valley reads R for
// randomness, so a machine that counted honestly would deal a different game
// from the translation's -- and agreeing with the translation is the whole
// job. The fiction is shared deliberately.

func (m *M) stepCB(pfx int) {
	var a uint16
	if pfx != 0 {
		a = m.hlAddr(pfx) // the displacement comes before the opcode here
	}
	op := m.f8()
	i := int(op) & 7
	n := int(op>>3) & 7
	switch {
	case pfx != 0:
		// A prefixed CB is 23 T-states, except that `bit n,(ix+d)` is
		// 20. The emitter cannot tell those apart -- the decoder carries
		// the prefix as the instruction and not the operation byte after
		// the displacement -- so neither does this, and both charge 23.
		// Twelve are already on the meter from the prefix and the
		// displacement.
		m.tick(11)
	case i == 6 && op >= 0x40 && op < 0x80: // bit n,(hl)
		m.tick(12)
	case i == 6:
		m.tick(15)
	default:
		m.tick(8)
	}

	get := func() byte {
		if pfx != 0 {
			return m.rd(a)
		}
		if i == 6 {
			return m.rd(m.HL())
		}
		return m.reg8(i, 0)
	}
	// A prefixed CB writes the result to the register named by the low
	// three bits as well as to memory, which is the whole undocumented
	// family; index 6 means memory only.
	put := func(v byte) {
		if pfx != 0 {
			m.wr(a, v)
			if i != 6 {
				m.setReg8(i, 0, v)
			}
			return
		}
		if i == 6 {
			m.wr(m.HL(), v)
			return
		}
		m.setReg8(i, 0, v)
	}

	switch {
	case op < 0x40: // rotates and shifts
		v := get()
		switch n {
		case 0:
			v = m.rlc(v)
		case 1:
			v = m.rrc(v)
		case 2:
			v = m.rl(v)
		case 3:
			v = m.rr(v)
		case 4:
			v = m.sla(v)
		case 5:
			v = m.sra(v)
		case 6:
			v = m.sll(v)
		case 7:
			v = m.srl(v)
		}
		put(v)
	case op < 0x80:
		m.bit(n, get())
	case op < 0xC0:
		put(get() &^ (1 << uint(n)))
	default:
		put(get() | 1<<uint(n))
	}
}

func (m *M) stepED() {
	op := m.f8()
	m.tick(uint32(cycED(op)))
	switch op {
	case 0x40, 0x48, 0x50, 0x58, 0x60, 0x68, 0x70, 0x78: // in r,(c)
		v := m.inC()
		m.sz(v)
		m.Fh, m.Fn = false, false
		m.Fp = parity(v)
		if i := int(op>>3) & 7; i != 6 {
			m.setReg8(i, 0, v)
		}
	case 0x41, 0x49, 0x51, 0x59, 0x61, 0x69, 0x71, 0x79: // out (c),r
		i := int(op>>3) & 7
		if i == 6 {
			m.outC(0)
		} else {
			m.outC(m.reg8(i, 0))
		}
	case 0x42, 0x52, 0x62, 0x72:
		m.sbcHL(m.rp(int(op>>4)&3, 0))
	case 0x4A, 0x5A, 0x6A, 0x7A:
		m.adcHL(m.rp(int(op>>4)&3, 0))
	case 0x43, 0x53, 0x63, 0x73:
		m.wr16(m.f16(), m.rp(int(op>>4)&3, 0))
	case 0x4B, 0x5B, 0x6B, 0x7B:
		m.setRP(int(op>>4)&3, 0, m.rd16(m.f16()))
	case 0x44, 0x4C, 0x54, 0x5C, 0x64, 0x6C, 0x74, 0x7C:
		m.neg()
	case 0x45, 0x4D, 0x55, 0x5D, 0x65, 0x6D, 0x75, 0x7D: // retn / reti
		m.PC = m.pop()
	case 0x46, 0x4E, 0x66, 0x6E:
		m.IM = 0
	case 0x56, 0x76:
		m.IM = 1
	case 0x5E, 0x7E:
		m.IM = 2
	case 0x47, 0x4F: // ld i,a / ld r,a -- no I here, and R is free-running
	case 0x57, 0x5F:
		m.A = m.refreshR()
		m.sz(m.A)
		m.Fh, m.Fn, m.Fp = false, false, m.IFF
	case 0x67:
		m.rrd()
	case 0x6F:
		m.rld()
	case 0xA0:
		m.ldi()
	case 0xA8:
		m.ldd()
	case 0xB0:
		m.ldir()
	case 0xB8:
		m.lddr()
	case 0xA1:
		m.cpi()
	case 0xA9:
		m.cpd()
	case 0xB1:
		m.cpir()
	case 0xB9:
		m.cpdr()
	case 0xA2:
		m.ini()
	case 0xAA:
		m.ind()
	case 0xB2:
		m.inir()
	case 0xBA:
		m.indr()
	case 0xA3:
		m.outi()
	case 0xAB:
		m.outd()
	case 0xB3:
		m.otir()
	case 0xBB:
		m.otdr()
	default:
		// The undefined ED opcodes are two-byte nops on real silicon, and
		// a cartridge that runs into one is usually running through data.
		// Being quiet here is what lets discovery survive such a stretch
		// and pick the trail up again.
	}
}

// InterpretRun runs the cartridge the way the hardware does: INIT once, then
// a vblank interrupt per frame with the main thread carrying on from wherever
// the interrupt found it.
//
// That last part is what the translated build cannot do -- there, every frame
// is a fresh call to the interrupt handler and a game's main loop is assumed
// to be a self-jump. Plenty of cartridges instead do their work in the main
// thread and use the handler only to tick a counter, and this is the loop that
// runs those. It is also the loop discovery wants, because the code in a main
// thread is code, and a model that never resumes one never sees it.
//
// There is no cycle counting, so a frame is a quota of instructions or a halt,
// whichever comes first. A halt is the honest boundary: a game that waits for
// vblank says so with one.
func (m *M) InterpretRun(base uint16, frames, quota int, perFrame func(f int)) error {
	// A machine restored from a snapshot has already booted; booting it
	// again would run INIT over a running game.
	if m.Frames() == 0 {
		boot := func() error { return m.Boot(base) }
		if m.Disk != nil {
			// A disk has no cartridge header: what it has is a
			// BASIC loader that ends by jumping into the code it
			// loaded. See BootDisk.
			boot = func() error { return m.BootDisk(m.Disk, "") }
		}
		if err := boot(); err != nil {
			return err
		}
	}
	// A cartridge whose loop is INIT itself need not have installed a hook
	// at all yet; Frame checks for one when it is the one delivering.
	if _, ok := m.InterruptEntry(); !ok && !m.MainThread {
		return errNoHook
	}
	for f := 0; f < frames; f++ {
		if err := m.Frame(); err != nil {
			// The same path the translated build takes, so that
			// interpreting a cartridge and running its translation
			// stay the same machine. That invariant is what the
			// sweep and every comparison here rest on, and it does
			// not survive two frame loops that merely resemble each
			// other.
			return err
		}
		if perFrame != nil {
			perFrame(f)
		}
	}
	return nil
}

var errNoHook = errInterp("z80: no interrupt hook installed at H.KEYI or H.TIMI")

type errInterp string

func (e errInterp) Error() string { return string(e) }

// bootRun runs INIT interpreted, whatever kind of build this is.
//
// A translated build could run its INIT translated -- and did -- but a
// cartridge whose game loop *is* INIT has to be stopped at an instruction
// boundary and resumed from a PC, and translated code can do neither. The
// interpreter can, and the two produce identical machines instruction for
// instruction -- that equivalence is checked, not assumed -- so INIT runs
// interpreted everywhere and the per-frame work stays translated.
func (m *M) bootRun(entry uint16) {
	mark := m.SP
	m.push(sentinel)
	m.PC = entry
	m.idle, m.halted = false, false
	m.Interpret(mark, maxInterpSteps)
}

// learnPC writes down that this address ran interpreted, with the banks in
// force, in the form the tracer reads back.
//
// This is the loop that closes over time: a generated game records everything
// it had to interpret -- the fallbacks past the translation's edge, and a
// main-thread game's whole main thread -- and feeding the file back into
// msx2go turns those addresses into translated code in the next build. Each
// generation interprets less than the last.
func (m *M) learnPC() {
	if m.PC < 0x4000 {
		return // the BIOS, which is shims, not translatable image
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%04X ", m.PC)
	for i, n := range m.mem.bank {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d", n)
	}
	if m.learned == nil {
		m.learned = map[string]bool{}
	}
	m.learned[b.String()] = true
}

// Learned is every interpreted site recorded so far, sorted, ready to be
// appended to the module's sites.txt and regenerated from.
func (m *M) Learned() []string {
	out := make([]string, 0, len(m.learned))
	for s := range m.learned {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// interSlotEntry is the group of page-zero routines that keep answering
// while a disk program runs: read and write a byte in another slot, call a
// routine in one, and page one in. See isBIOS.
var interSlotEntry = map[uint16]bool{
	0x000C: true, 0x0014: true, 0x001C: true, 0x0024: true, 0x0030: true,
}
