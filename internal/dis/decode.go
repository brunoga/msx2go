// Package dis decodes Z80 instructions.
//
// It is the shared front end: the tracer uses it to walk reachable code and
// the emitter uses the very same instruction boundaries to write Go. Sharing
// matters more than it sounds. Cartridge code overlaps -- `jr z,4F34h` lands
// one byte into the `cp 20h` at 4F33h, so those bytes are two different
// instructions depending on how they are entered -- and any second opinion
// about where an instruction starts is a bug that shows up as a game
// panicking the first time it takes the other path.
package dis

// Kind is what a decoded instruction does to the flow of control.
type Kind int

const (
	// Normal falls through to the next instruction.
	Normal Kind = iota
	// Jp, Jr, Call and Djnz name a target; Cond says whether it is taken
	// on a condition, in which case the instruction also falls through.
	Jp
	Jr
	Call
	Djnz
	// Ret ends a routine; conditional forms fall through.
	Ret
	// Reti is ED-prefixed RETN/RETI and their undocumented duplicates.
	Reti
	// Rst is a one-byte call to a page-zero vector.
	Rst
	// Ijp is `jp (hl)` and its IX/IY forms: the target is a register and
	// only the tracer's abstract interpretation can say what it holds.
	Ijp
	// Halt stops until an interrupt.
	Halt
)

// Cond names a condition code.
type Cond int

// The eight condition codes, plus None for the unconditional forms.
const (
	None Cond = iota - 1
	NZ
	Z
	NC
	C
	PO
	PE
	P
	M
)

// Name is the condition as it is written in assembly.
func (c Cond) Name() string {
	if c < NZ || c > M {
		return ""
	}
	return [...]string{"nz", "z", "nc", "c", "po", "pe", "p", "m"}[c]
}

// Insn is one decoded instruction.
type Insn struct {
	// Addr and Len are where it is and how long.
	Addr uint16
	Len  int
	// Kind and Cond are what it does to control flow.
	Kind Kind
	Cond Cond
	// Target is where it goes, for the kinds that name one.
	Target uint16
	// Refs are 16-bit immediates that might be addresses: the operand of
	// `ld hl,nnnn`, `ld (nnnn),a` and so on. The tracer decides which of
	// them are really references.
	Refs []uint16
	// Op is the first byte and Sub the second where there is a prefix, so
	// that a consumer can re-derive the operands without decoding again.
	Op, Sub byte
}

// End is the address after the instruction.
func (i Insn) End() uint16 { return i.Addr + uint16(i.Len) }

// FallsThrough reports whether execution continues at End.
//
// A halt does: an interrupt resumes at the instruction after it, and the
// tracer has to follow. Only an unconditional jump or return, and an indirect
// jump -- which never has a condition -- end the run of code outright.
func (i Insn) FallsThrough() bool {
	switch i.Kind {
	case Jp, Jr, Ret, Reti:
		return i.Cond != None
	case Ijp:
		return false
	}
	return true
}

// Reader is somewhere instructions can be decoded from: a bank of a
// cartridge, or the whole address space under one paging state.
type Reader interface {
	// Readable reports whether n bytes from addr are mapped.
	Readable(addr uint16, n int) bool
	// Byte reads one byte.
	Byte(addr uint16) byte
	// Word reads a little-endian sixteen-bit value.
	Word(addr uint16) uint16
}

// mainLen is the length of un-prefixed opcodes. Zero marks a prefix, which is
// handled on its own below.
var mainLen = [256]int{
	1, 3, 1, 1, 1, 1, 2, 1, 1, 1, 1, 1, 1, 1, 2, 1, // 0x
	2, 3, 1, 1, 1, 1, 2, 1, 2, 1, 1, 1, 1, 1, 2, 1, // 1x
	2, 3, 3, 1, 1, 1, 2, 1, 2, 1, 3, 1, 1, 1, 2, 1, // 2x
	2, 3, 3, 1, 1, 1, 2, 1, 2, 1, 3, 1, 1, 1, 2, 1, // 3x
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 4x
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 5x
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 6x
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 7x
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 8x
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // 9x
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // Ax
	1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, // Bx
	1, 1, 3, 3, 3, 1, 2, 1, 1, 1, 3, 0, 3, 3, 2, 1, // Cx  (CB)
	1, 1, 3, 2, 3, 1, 2, 1, 1, 1, 3, 2, 3, 0, 2, 1, // Dx  (DD)
	1, 1, 3, 1, 3, 1, 2, 1, 1, 1, 3, 1, 3, 0, 2, 1, // Ex  (ED)
	1, 1, 3, 1, 3, 1, 2, 1, 1, 1, 3, 1, 3, 0, 2, 1, // Fx  (FD)
}

// edLong are the four-byte ED instructions: `ld (nnnn),rr` and `ld rr,(nnnn)`.
func edLong(sub byte) bool {
	switch sub {
	case 0x43, 0x4B, 0x53, 0x5B, 0x63, 0x6B, 0x73, 0x7B:
		return true
	}
	return false
}

// Decode reads one instruction at addr, or reports false if it runs off the
// end of mapped memory.
func Decode(r Reader, addr uint16) (Insn, bool) {
	if !r.Readable(addr, 1) {
		return Insn{}, false
	}
	op := r.Byte(addr)

	switch op {
	case 0xCB:
		if !r.Readable(addr, 2) {
			return Insn{}, false
		}
		return Insn{Addr: addr, Len: 2, Kind: Normal, Cond: None,
			Op: op, Sub: r.Byte(addr + 1)}, true

	case 0xED:
		if !r.Readable(addr, 2) {
			return Insn{}, false
		}
		sub := r.Byte(addr + 1)
		n := 2
		if edLong(sub) {
			n = 4
		}
		if !r.Readable(addr, n) {
			return Insn{}, false
		}
		if sub&0xC7 == 0x45 { // RETN / RETI and their duplicates
			return Insn{Addr: addr, Len: n, Kind: Reti, Cond: None,
				Op: op, Sub: sub}, true
		}
		var refs []uint16
		if edLong(sub) {
			refs = []uint16{r.Word(addr + 2)}
		}
		return Insn{Addr: addr, Len: n, Kind: Normal, Cond: None,
			Refs: refs, Op: op, Sub: sub}, true

	case 0xDD, 0xFD:
		if !r.Readable(addr, 2) {
			return Insn{}, false
		}
		sub := r.Byte(addr + 1)
		if sub == 0xCB {
			if !r.Readable(addr, 4) {
				return Insn{}, false
			}
			return Insn{Addr: addr, Len: 4, Kind: Normal, Cond: None,
				Op: op, Sub: sub}, true
		}
		// A prefix chain: this one degenerates to a nop and the next
		// byte starts again.
		if sub == 0xDD || sub == 0xFD || sub == 0xED {
			return Insn{Addr: addr, Len: 1, Kind: Normal, Cond: None,
				Op: op, Sub: sub}, true
		}
		base := mainLen[sub]
		if base == 0 {
			return Insn{Addr: addr, Len: 1, Kind: Normal, Cond: None,
				Op: op, Sub: sub}, true
		}
		// The displacement byte is present only for the (IX+d) forms.
		needsD := (sub >= 0x34 && sub <= 0x36) ||
			(sub >= 0x40 && sub <= 0x7F && sub != 0x76 &&
				(sub&0x07 == 0x06 || sub&0xF8 == 0x70)) ||
			(sub >= 0x80 && sub <= 0xBF && sub&0x07 == 0x06)
		n := 1 + base
		if needsD {
			n++
		}
		if !r.Readable(addr, n) {
			return Insn{}, false
		}
		if sub == 0xE9 {
			return Insn{Addr: addr, Len: n, Kind: Ijp, Cond: None,
				Op: op, Sub: sub}, true
		}
		var refs []uint16
		if base == 3 {
			refs = []uint16{r.Word(addr + 2)}
		}
		return Insn{Addr: addr, Len: n, Kind: Normal, Cond: None,
			Refs: refs, Op: op, Sub: sub}, true
	}

	n := mainLen[op]
	if !r.Readable(addr, n) {
		return Insn{}, false
	}
	ins := Insn{Addr: addr, Len: n, Kind: Normal, Cond: None, Op: op}

	switch {
	case op == 0x76:
		ins.Len, ins.Kind = 1, Halt
	case op == 0xC9:
		ins.Len, ins.Kind = 1, Ret
	case op&0xC7 == 0xC0:
		ins.Len, ins.Kind, ins.Cond = 1, Ret, Cond(op>>3&7)
	case op == 0xC3:
		ins.Len, ins.Kind, ins.Target = 3, Jp, r.Word(addr+1)
	case op&0xC7 == 0xC2:
		ins.Len, ins.Kind, ins.Target = 3, Jp, r.Word(addr+1)
		ins.Cond = Cond(op >> 3 & 7)
	case op == 0xCD:
		ins.Len, ins.Kind, ins.Target = 3, Call, r.Word(addr+1)
	case op&0xC7 == 0xC4:
		ins.Len, ins.Kind, ins.Target = 3, Call, r.Word(addr+1)
		ins.Cond = Cond(op >> 3 & 7)
	case op == 0x18:
		ins.Len, ins.Kind = 2, Jr
		ins.Target = rel(addr, r.Byte(addr+1))
	case op == 0x20 || op == 0x28 || op == 0x30 || op == 0x38:
		ins.Len, ins.Kind = 2, Jr
		ins.Target = rel(addr, r.Byte(addr+1))
		ins.Cond = Cond(op >> 3 & 3)
	case op == 0x10:
		ins.Len, ins.Kind = 2, Djnz
		ins.Target = rel(addr, r.Byte(addr+1))
	case op == 0xE9:
		ins.Len, ins.Kind = 1, Ijp
	case op&0xC7 == 0xC7:
		ins.Len, ins.Kind, ins.Target = 1, Rst, uint16(op&0x38)
	default:
		if n == 3 {
			ins.Refs = []uint16{r.Word(addr + 1)}
		}
	}
	return ins, true
}

// rel resolves a two-byte relative jump's displacement.
func rel(addr uint16, d byte) uint16 {
	return addr + 2 + uint16(int8(d))
}
