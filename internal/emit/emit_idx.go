package emit

import (
	"fmt"

	"github.com/brunoga/msx2go/internal/dis"
)

// The DD and FD prefixes: the index registers, and the (IX+d) forms.
//
// Also the undocumented halves -- IXh, IXl and their IY twins -- which are
// exactly the sort of thing that has no business being in a commercial ROM and
// is in Konami's anyway.

// idxRegs is the naming for one prefix.
type idxRegs struct {
	reg          string
	hiGet, loGet string
	hiSet, loSet string
}

func idxFor(op byte) idxRegs {
	if op == 0xDD {
		return idxRegs{"m.IX", "m.ixh()", "m.ixl()", "m.setIXh", "m.setIXl"}
	}
	return idxRegs{"m.IY", "m.iyh()", "m.iyl()", "m.setIYh", "m.setIYl"}
}

// half is the getter for an 8-bit operand where four and five mean the index
// register's halves rather than H and L.
func (x idxRegs) half(i int) string {
	switch i {
	case 4:
		return x.hiGet
	case 5:
		return x.loGet
	}
	return r8[i]
}

// setHalf is the setter for the same, or empty where the operand is an
// ordinary register.
func (x idxRegs) setHalf(i int) string {
	switch i {
	case 4:
		return x.hiSet
	case 5:
		return x.loSet
	}
	return ""
}

// idx translates a DD- or FD-prefixed instruction.
func (c Ctx) idx(ins dis.Insn) ([]string, error) {
	op, sub, addr := ins.Op, ins.Sub, ins.Addr
	x := idxFor(op)
	one := func(s string) ([]string, error) { return []string{s}, nil }

	// ea is the effective address of an (IX+d) operand. The displacement
	// is signed and folded into sixteen bits, so that the Go reads as an
	// addition however negative it was.
	ea := func() string {
		d := int8(c.View.Byte(addr + 2))
		return fmt.Sprintf("%s+0x%04x", x.reg, uint16(int16(d)))
	}

	if sub == 0xCB {
		d := int8(c.View.Byte(addr + 2))
		sub2 := c.View.Byte(addr + 3)
		kind, bit := sub2>>6, int(sub2>>3&7)
		at := fmt.Sprintf("%s+0x%04x", x.reg, uint16(int16(d)))
		switch kind {
		case 0:
			return one(fmt.Sprintf("m.wr(%s, m.%s(m.rd(%s)))",
				at, rot[bit], at))
		case 1:
			return one(fmt.Sprintf("m.bit(%d, m.rd(%s))", bit, at))
		case 2:
			return one(fmt.Sprintf("m.wr(%s, m.rd(%s) &^ 0x%02x)",
				at, at, 1<<uint(bit)))
		}
		return one(fmt.Sprintf("m.wr(%s, m.rd(%s) | 0x%02x)",
			at, at, 1<<uint(bit)))
	}

	switch sub {
	case 0x21:
		return one(fmt.Sprintf("%s = 0x%04x", x.reg, c.View.Word(addr+2)))
	case 0x22:
		return one(fmt.Sprintf("m.wr16(0x%04x, %s)",
			c.View.Word(addr+2), x.reg))
	case 0x2A:
		return one(fmt.Sprintf("%s = m.rd16(0x%04x)",
			x.reg, c.View.Word(addr+2)))
	case 0xE5:
		return one(fmt.Sprintf("m.push(%s)", x.reg))
	case 0xE1:
		return one(fmt.Sprintf("%s = m.pop()", x.reg))
	case 0xE9:
		return []string{fmt.Sprintf("m.PC = %s", x.reg), c.dispatchStmt()}, nil
	case 0xF9:
		return one(fmt.Sprintf("m.SP = %s", x.reg))
	case 0xE3:
		return one(fmt.Sprintf("m.exSP(&%s)", x.reg))
	case 0x09, 0x19, 0x29, 0x39:
		src := map[byte]string{
			0x09: "m.BC()", 0x19: "m.DE()", 0x29: x.reg, 0x39: "m.SP",
		}[sub]
		return one(fmt.Sprintf("%s = m.add16(%s, %s)", x.reg, x.reg, src))
	case 0x23:
		return one(fmt.Sprintf("%s++", x.reg))
	case 0x2B:
		return one(fmt.Sprintf("%s--", x.reg))
	case 0x36:
		return one(fmt.Sprintf("m.wr(%s, 0x%02x)",
			ea(), c.View.Byte(addr+3)))
	case 0x34:
		return one(fmt.Sprintf("m.wr(%s, m.inc8(m.rd(%s)))", ea(), ea()))
	case 0x35:
		return one(fmt.Sprintf("m.wr(%s, m.dec8(m.rd(%s)))", ea(), ea()))
	case 0x26, 0x2E:
		set := x.hiSet
		if sub == 0x2E {
			set = x.loSet
		}
		return one(fmt.Sprintf("%s(0x%02x)", set, c.View.Byte(addr+2)))
	case 0x24, 0x2C, 0x25, 0x2D:
		get, set := x.hiGet, x.hiSet
		if sub == 0x2C || sub == 0x2D {
			get, set = x.loGet, x.loSet
		}
		fn := "m.inc8"
		if sub == 0x25 || sub == 0x2D {
			fn = "m.dec8"
		}
		return one(fmt.Sprintf("%s(%s(%s))", set, fn, get))
	}

	// The (IX+d) forms of the 8-bit loads and the ALU.
	switch {
	case sub >= 0x70 && sub <= 0x77 && sub != 0x76:
		return one(fmt.Sprintf("m.wr(%s, %s)", ea(), r8[sub&7]))
	case sub >= 0x40 && sub <= 0x7F && sub&7 == 6:
		return one(fmt.Sprintf("%s = m.rd(%s)", r8[sub>>3&7], ea()))
	case sub >= 0x80 && sub <= 0xBF && sub&7 == 6:
		return one(fmt.Sprintf("m.alu%s(m.rd(%s))", alu[sub>>3&7], ea()))

	// The undocumented halves.
	case sub >= 0x40 && sub <= 0x7F:
		d, s := int(sub>>3&7), int(sub&7)
		src := x.half(s)
		if set := x.setHalf(d); set != "" {
			return one(fmt.Sprintf("%s(%s)", set, src))
		}
		return one(fmt.Sprintf("%s = %s", r8[d], src))
	case sub >= 0x80 && sub <= 0xBF:
		return one(fmt.Sprintf("m.alu%s(%s)",
			alu[sub>>3&7], x.half(int(sub&7))))
	}

	// A DD or FD prefix on an instruction with no index operand does
	// nothing: the processor treats the prefix as a one-byte no-op and
	// decodes again at the byte after it. So translate what is there.
	//
	// The address handed on is addr+1, which is where that instruction
	// really begins -- so an immediate is read from the right place. A
	// *relative* jump would also be relative to that, and this is where
	// the decoder and the hardware part company: the decoder counts the
	// prefix as part of one instruction. Nothing has been seen doing that
	// yet, and if it is, it will be a wrong target rather than a silence.
	if sub != 0xCB && sub != 0xDD && sub != 0xFD && sub != 0xED {
		inner := dis.Insn{
			Addr: addr + 1, Len: ins.Len - 1, Kind: ins.Kind,
			Cond: ins.Cond, Target: ins.Target, Op: sub,
		}
		if out, err := c.Insn(inner); err == nil {
			return out, nil
		}
	}
	return nil, Unsupported{addr, op, sub}
}
