// Package emit turns traced instructions into Go.
//
// A mechanical, semantics-preserving translation of every instruction the
// tracer proved reachable. It is not pretty Go and it is not meant to be -- it
// is meant to be *verifiable*. Once it reproduces the cartridge's behaviour
// byte for byte it becomes a safety net for rewriting the logic into an
// idiomatic engine one routine at a time, which is the whole road this is the
// first step of.
//
// Everything lands in one function with a label per instruction:
//
//	func (m *M) Run(entry uint16) {
//	    m.push(sentinel)
//	    m.PC = entry
//	dispatch:
//	    switch m.PC {
//	    case 0x4010: goto L4010
//	    ...
//	    }
//	L4010:
//	    ...
//	}
//
// A `call` pushes a return address and goes straight to the target; a `ret`
// pops and re-enters the dispatch. Go's own call stack is deliberately not
// used, because cartridge code manipulates the return address: King's Valley's
// dispatcher at 404Bh does `pop hl` to find the table that follows the call,
// and the handler's `ret` then unwinds two levels. Modelling the stack
// explicitly is the only thing that works.
//
// An address the tracer never reached has no label, so reaching one at runtime
// panics with the address -- which is exactly the report we want.
package emit

import (
	"fmt"

	"github.com/brunoga/msx2go/internal/dis"
)

// r8 are the machine's byte registers by the Z80's own encoding, with (hl) at
// six standing for "not a register at all".
var r8 = [8]string{"m.B", "m.C", "m.D", "m.E", "m.H", "m.L", "", "m.A"}

// rp are the 16-bit pairs, and rp2 the set push and pop use.
var rp = [4]string{"BC", "DE", "HL", "SP"}
var rp2 = [4]string{"BC", "DE", "HL", "AF"}

// cc are the eight condition codes as Go expressions on the flag fields.
var cc = [8]string{"!m.Fz", "m.Fz", "!m.Fc", "m.Fc",
	"!m.Fp", "m.Fp", "!m.Fs", "m.Fs"}

// alu and rot are the method names for the arithmetic and shift groups.
var alu = [8]string{"Add", "Adc", "Sub", "Sbc", "And", "Xor", "Or", "Cp"}
var rot = [8]string{"rlc", "rrc", "rl", "rr", "sla", "sra", "sll", "srl"}

// Unsupported is an instruction the emitter has no translation for. It is
// reported rather than guessed at, and the generated code panics if it is ever
// reached, because inventing semantics here would be a bug that only shows up
// as the game behaving differently.
type Unsupported struct {
	Addr    uint16
	Op, Sub byte
}

func (u Unsupported) Error() string {
	return fmt.Sprintf("unsupported opcode %02x %02x at %04x",
		u.Op, u.Sub, u.Addr)
}

// Ctx is what one instruction's translation needs to know beyond itself.
type Ctx struct {
	// View reads the image, for the operands.
	View dis.Reader
	// Labels is every address that has one, so the emitter can tell a
	// jump into translated code from a jump into the unknown.
	Labels map[uint16]bool
	// Idle compiles a self-jump as a return. See Run.IdleOnSelfJump.
	Idle bool
	// Recover returns after reporting an address with no label. See
	// Run.RecoverOnNoLabel.
	Recover bool
	// Chunked says the translation is split across several functions, so a
	// transfer that leaves this one returns to the trampoline instead of
	// jumping. InChunk reports whether a target has a label in this chunk
	// and what it is called. See chunked.go.
	Chunked bool
	InChunk func(target uint16) (string, bool)
	// Banked says an address is not enough to name an instruction, so a
	// label is an offset and a target outside this page has to be looked
	// up at run time. Offsets turns a target into its offset and whether
	// that offset has a label, and is only consulted when Banked.
	Banked  bool
	Offsets func(target uint16) (int, bool)
}

// jumpTo is how control reaches an address: a direct goto where the label can
// be named now, and the dispatch where it cannot.
func (c Ctx) jumpTo(t uint16) string {
	if c.Chunked {
		if t < 0x4000 {
			return fmt.Sprintf("{ m.bios(0x%04x); goto ret_ }", t)
		}
		if l, ok := c.InChunk(t); ok {
			return fmt.Sprintf("goto L%s", l)
		}
		// Another chunk, or nothing translated there: the trampoline
		// decides, exactly as it does for an indirect jump.
		return fmt.Sprintf("{ m.PC = 0x%04x; return false }", t)
	}
	if !c.Banked {
		if t < 0x4000 {
			return fmt.Sprintf("{ m.bios(0x%04x); goto ret_ }", t)
		}
		if c.Labels[t] {
			return fmt.Sprintf("goto L%04x", t)
		}
		return fmt.Sprintf("m.noLabel(0x%04x)", t)
	}
	if t < 0x4000 {
		return fmt.Sprintf("{ m.bios(0x%04x); goto ret_ }", t)
	}
	if off, ok := c.Offsets(t); ok {
		return fmt.Sprintf("goto L%05x", off)
	}
	// Another page, or nothing translated there. Either way the answer
	// depends on the paging, so ask the dispatch.
	return fmt.Sprintf("{ m.PC = 0x%04x; goto dispatch }", t)
}

// returnStmt leaves Run. A chunk function reports back to the trampoline
// whether the machine is finished, so its return carries a value.
func (c Ctx) returnStmt() string {
	if c.Chunked {
		return "return true"
	}
	return "return"
}

// dispatchStmt hands control back to whatever decides where to go next: the
// in-function dispatch when the translation is one function, the trampoline
// when it is several.
func (c Ctx) dispatchStmt() string {
	if c.Chunked {
		return "return false"
	}
	return "goto dispatch"
}

// noLabel is what to emit for an address the tracer never proved reachable.
//
// A panic naming it is the ordinary answer: it is a bug report rather than a
// silent wrong turn. The discovery build wants something else -- to write the
// address down and give up on the frame -- which needs a return, because
// falling through would land in whatever label the file happens to have next.
func (c Ctx) noLabel(t uint16) string {
	if c.Recover {
		return fmt.Sprintf("{ m.noLabel(0x%04x); return }", t)
	}
	return fmt.Sprintf("m.noLabel(0x%04x)", t)
}

// callTo is a Z80 CALL: push the return address, then go to the target.
func (c Ctx) callTo(target, ret uint16) string {
	if target < 0x4000 {
		return fmt.Sprintf("m.bios(0x%04x)", target)
	}
	if c.Chunked {
		if l, ok := c.InChunk(target); ok {
			return fmt.Sprintf("{ m.push(0x%04x); goto L%s }", ret, l)
		}
		return fmt.Sprintf(
			"{ m.push(0x%04x); m.PC = 0x%04x; return false }",
			ret, target)
	}
	if !c.Banked {
		if c.Labels[target] {
			return fmt.Sprintf("{ m.push(0x%04x); goto L%04x }",
				ret, target)
		}
		// A call whose target has no label still *is* a call: the
		// return address goes on the stack before the interpreter
		// takes over, or the callee's ret pops whatever lies beneath
		// -- the sentinel -- and the caller is abandoned mid-routine
		// with no sign that anything went wrong. King's Valley Plus
		// calls H.TIMI two instructions into its interrupt handler,
		// so its whole frame vanished and the screen never changed.
		// Every other branch here pushes; this one forgot.
		return fmt.Sprintf("{ m.push(0x%04x); %s }", ret,
			c.noLabel(target))
	}
	if off, ok := c.Offsets(target); ok {
		return fmt.Sprintf("{ m.push(0x%04x); goto L%05x }", ret, off)
	}
	return fmt.Sprintf("{ m.push(0x%04x); m.PC = 0x%04x; goto dispatch }",
		ret, target)
}

// selfJump reports whether an instruction is the idle loop: an unconditional
// jump to itself.
//
// The conditional forms are left alone. `jr z,$` is a spin on a flag some
// other piece of the machine sets, and turning that into a return would be
// making a decision about what the program meant rather than translating it.
func selfJump(ins dis.Insn) bool {
	if ins.Cond != dis.None {
		return false
	}
	return (ins.Kind == dis.Jp || ins.Kind == dis.Jr) &&
		ins.Target == ins.Addr
}

func get8(i int) string {
	if i == 6 {
		return "m.rd(m.HL())"
	}
	return r8[i]
}

func set8(i int, expr string) string {
	if i == 6 {
		return fmt.Sprintf("m.wr(m.HL(), %s)", expr)
	}
	return fmt.Sprintf("%s = %s", r8[i], expr)
}

func rpget(i int) string {
	if i == 3 {
		return "m.SP"
	}
	return fmt.Sprintf("m.%s()", rp[i])
}

func rpset(i int, expr string) string {
	if i == 3 {
		return fmt.Sprintf("m.SP = %s", expr)
	}
	return fmt.Sprintf("m.set%s(%s)", rp[i], expr)
}

// label is where a jump goes.
//
// Below 4000h is the BIOS, which is not part of the cartridge and is shimmed
// rather than translated -- so a `jp` there is a tail call: run the shim, then
// return. Anything else with no label is an address the tracer never proved
// reachable, and reaching it is a bug report.
func (c Ctx) label(t uint16) string { return c.jumpTo(t) }

// callStmt is a Z80 CALL: push the return address, then jump to the target.
func (c Ctx) callStmt(target, ret uint16) string {
	return c.callTo(target, ret)
}

// Insn translates one instruction into Go statements.
func (c Ctx) Insn(ins dis.Insn) ([]string, error) {
	op, sub, addr := ins.Op, ins.Sub, ins.Addr
	imm8 := func() byte { return c.View.Byte(addr + 1) }
	imm16 := func() uint16 { return c.View.Word(addr + 1) }
	one := func(s string) ([]string, error) { return []string{s}, nil }

	if c.Idle && selfJump(ins) {
		return []string{"m.Idle()", c.returnStmt()}, nil
	}

	switch op {
	case 0xCB:
		return c.cb(sub)
	case 0xED:
		return c.ed(ins)
	case 0xDD, 0xFD:
		return c.idx(ins)
	}

	switch {
	// ---- 8-bit loads ------------------------------------------------
	case op >= 0x40 && op <= 0x7F && op != 0x76:
		return one(set8(int(op>>3&7), get8(int(op&7))))
	case op == 0x06 || op == 0x0E || op == 0x16 || op == 0x1E ||
		op == 0x26 || op == 0x2E || op == 0x3E:
		return one(set8(int(op>>3&7), fmt.Sprintf("0x%02x", imm8())))
	case op == 0x36:
		return one(fmt.Sprintf("m.wr(m.HL(), 0x%02x)", imm8()))
	case op == 0x0A:
		return one("m.A = m.rd(m.BC())")
	case op == 0x1A:
		return one("m.A = m.rd(m.DE())")
	case op == 0x02:
		return one("m.wr(m.BC(), m.A)")
	case op == 0x12:
		return one("m.wr(m.DE(), m.A)")
	case op == 0x3A:
		return one(fmt.Sprintf("m.A = m.rd(0x%04x)", imm16()))
	case op == 0x32:
		return one(fmt.Sprintf("m.wr(0x%04x, m.A)", imm16()))
	case op == 0x2A:
		return one(fmt.Sprintf("m.setHL(m.rd16(0x%04x))", imm16()))
	case op == 0x22:
		return one(fmt.Sprintf("m.wr16(0x%04x, m.HL())", imm16()))

	// ---- 16-bit loads -----------------------------------------------
	case op == 0x01 || op == 0x11 || op == 0x21 || op == 0x31:
		return one(rpset(int(op>>4&3), fmt.Sprintf("0x%04x", imm16())))
	case op == 0xF9:
		return one("m.SP = m.HL()")
	case op == 0xC5 || op == 0xD5 || op == 0xE5 || op == 0xF5:
		return one(fmt.Sprintf("m.push(m.%s())", rp2[op>>4&3]))
	case op == 0xC1 || op == 0xD1 || op == 0xE1 || op == 0xF1:
		return one(fmt.Sprintf("m.set%s(m.pop())", rp2[op>>4&3]))
	case op == 0xE3:
		return one("m.exSPHL()")
	case op == 0xEB:
		return one("m.exDEHL()")
	case op == 0x08:
		return one("m.exAF()")
	case op == 0xD9:
		return one("m.exx()")

	// ---- arithmetic --------------------------------------------------
	case op >= 0x80 && op <= 0xBF:
		return one(fmt.Sprintf("m.alu%s(%s)",
			alu[op>>3&7], get8(int(op&7))))
	case op == 0xC6 || op == 0xCE || op == 0xD6 || op == 0xDE ||
		op == 0xE6 || op == 0xEE || op == 0xF6 || op == 0xFE:
		return one(fmt.Sprintf("m.alu%s(0x%02x)", alu[op>>3&7], imm8()))
	case op&0xC7 == 0x04:
		if i := int(op >> 3 & 7); i == 6 {
			return one("m.wr(m.HL(), m.inc8(m.rd(m.HL())))")
		} else {
			return one(fmt.Sprintf("%s = m.inc8(%s)", r8[i], r8[i]))
		}
	case op&0xC7 == 0x05:
		if i := int(op >> 3 & 7); i == 6 {
			return one("m.wr(m.HL(), m.dec8(m.rd(m.HL())))")
		} else {
			return one(fmt.Sprintf("%s = m.dec8(%s)", r8[i], r8[i]))
		}
	case op == 0x03 || op == 0x13 || op == 0x23 || op == 0x33:
		i := int(op >> 4 & 3)
		return one(rpset(i, rpget(i)+" + 1"))
	case op == 0x0B || op == 0x1B || op == 0x2B || op == 0x3B:
		i := int(op >> 4 & 3)
		return one(rpset(i, rpget(i)+" - 1"))
	case op == 0x09 || op == 0x19 || op == 0x29 || op == 0x39:
		return one(fmt.Sprintf("m.addHL(%s)", rpget(int(op>>4&3))))

	// ---- rotates and the odds and ends -------------------------------
	case op == 0x00:
		return nil, nil // nop
	case op == 0x07:
		return one("m.rlca()")
	case op == 0x0F:
		return one("m.rrca()")
	case op == 0x17:
		return one("m.rla()")
	case op == 0x1F:
		return one("m.rra()")
	case op == 0x27:
		return one("m.daa()")
	case op == 0x2F:
		return one("m.cpl()")
	case op == 0x37:
		return one("m.scf()")
	case op == 0x3F:
		return one("m.ccf()")
	case op == 0xF3:
		return one("m.IFF = false")
	case op == 0xFB:
		return one("m.IFF = true")
	case op == 0x76:
		return one("m.halted = true")
	case op == 0xD3:
		return one(fmt.Sprintf("m.out(0x%02x, m.A)", imm8()))
	case op == 0xDB:
		return one(fmt.Sprintf("m.A = m.in(0x%02x)", imm8()))

	// ---- control flow -------------------------------------------------
	case op == 0xC3:
		return one(c.label(imm16()))
	case op&0xC7 == 0xC2:
		return one(fmt.Sprintf("if %s { %s }",
			cc[op>>3&7], c.label(imm16())))
	case op == 0x18:
		return one(c.label(ins.Target))
	case op == 0x20 || op == 0x28 || op == 0x30 || op == 0x38:
		return one(fmt.Sprintf("if %s { %s }",
			cc[op>>3&3], c.label(ins.Target)))
	case op == 0x10:
		return []string{"m.B--",
			fmt.Sprintf("if m.B != 0 { %s }", c.label(ins.Target)),
		}, nil
	case op == 0xCD:
		return one(c.callStmt(imm16(), ins.End()))
	case op&0xC7 == 0xC4:
		return one(fmt.Sprintf("if %s { %s }", cc[op>>3&7],
			c.callStmt(imm16(), ins.End())))
	case op == 0xC9:
		return one("goto ret_")
	case op&0xC7 == 0xC0:
		return one(fmt.Sprintf("if %s { goto ret_ }", cc[op>>3&7]))
	case op == 0xE9:
		return []string{"m.PC = m.HL()", c.dispatchStmt()}, nil
	case op&0xC7 == 0xC7:
		return one(fmt.Sprintf("m.rst(0x%02x)", op&0x38))
	}
	return nil, Unsupported{addr, op, sub}
}

// cb translates the CB-prefixed shifts and bit operations.
func (c Ctx) cb(sub byte) ([]string, error) {
	kind, bit, r := sub>>6, int(sub>>3&7), int(sub&7)
	switch kind {
	case 0:
		if r == 6 {
			return []string{fmt.Sprintf(
				"m.wr(m.HL(), m.%s(m.rd(m.HL())))", rot[bit])}, nil
		}
		return []string{fmt.Sprintf("%s = m.%s(%s)",
			r8[r], rot[bit], r8[r])}, nil
	case 1:
		return []string{fmt.Sprintf("m.bit(%d, %s)", bit, get8(r))}, nil
	case 2:
		return []string{set8(r, fmt.Sprintf("%s &^ 0x%02x",
			get8(r), 1<<uint(bit)))}, nil
	}
	return []string{set8(r, fmt.Sprintf("%s | 0x%02x",
		get8(r), 1<<uint(bit)))}, nil
}

// ed translates the ED-prefixed set.
func (c Ctx) ed(ins dis.Insn) ([]string, error) {
	sub, addr := ins.Sub, ins.Addr
	one := func(s string) ([]string, error) { return []string{s}, nil }
	block := map[byte]string{
		0xA0: "ldi", 0xA8: "ldd", 0xB0: "ldir", 0xB8: "lddr",
		0xA1: "cpi", 0xA9: "cpd", 0xB1: "cpir", 0xB9: "cpdr",
		0xA2: "ini", 0xAA: "ind", 0xB2: "inir", 0xBA: "indr",
		0xA3: "outi", 0xAB: "outd", 0xB3: "otir", 0xBB: "otdr",
	}
	switch {
	case sub&0xC7 == 0x41: // out (c),r
		if r := int(sub >> 3 & 7); r == 6 {
			return one("m.outC(0)")
		} else {
			return one(fmt.Sprintf("m.outC(%s)", r8[r]))
		}
	case sub&0xC7 == 0x40: // in r,(c)
		if r := int(sub >> 3 & 7); r == 6 {
			return one("m.inC()")
		} else {
			return one(fmt.Sprintf("%s = m.inC()", r8[r]))
		}
	}
	if name, ok := block[sub]; ok {
		return one(fmt.Sprintf("m.%s()", name))
	}
	switch {
	case sub == 0x43 || sub == 0x53 || sub == 0x63 || sub == 0x73:
		return one(fmt.Sprintf("m.wr16(0x%04x, %s)",
			c.View.Word(addr+2), rpget(int(sub>>4&3))))
	case sub == 0x4B || sub == 0x5B || sub == 0x6B || sub == 0x7B:
		return one(rpset(int(sub>>4&3),
			fmt.Sprintf("m.rd16(0x%04x)", c.View.Word(addr+2))))
	case sub == 0x42 || sub == 0x52 || sub == 0x62 || sub == 0x72:
		return one(fmt.Sprintf("m.sbcHL(%s)", rpget(int(sub>>4&3))))
	case sub == 0x4A || sub == 0x5A || sub == 0x6A || sub == 0x7A:
		return one(fmt.Sprintf("m.adcHL(%s)", rpget(int(sub>>4&3))))
	case sub == 0x44:
		return one("m.neg()")
	case sub == 0x46 || sub == 0x56 || sub == 0x5E || sub == 0x4E ||
		sub == 0x66 || sub == 0x6E || sub == 0x76 || sub == 0x7E:
		mode := 1
		switch sub {
		case 0x46:
			mode = 0
		case 0x5E:
			mode = 2
		}
		return one(fmt.Sprintf("m.IM = %d", mode))
	case sub == 0x47 || sub == 0x4F:
		return one("// ld i,a / ld r,a - not modelled")
	case sub == 0x57: // ld a,i -- IM 1 never uses I, so zero is the truth
		return one("m.A = 0 // ld a,i / ld a,r - no meaningful value")
	case sub == 0x5F: // ld a,r
		return one("m.A = m.refreshR()")
	case sub == 0x67:
		return one("m.rrd()")
	case sub == 0x6F:
		return one("m.rld()")
	case sub&0xC7 == 0x45:
		return one("goto ret_")
	}
	return nil, Unsupported{addr, 0xED, sub}
}
