package trace

import "github.com/brunoga/msx2go/internal/dis"

// Store is a byte an instruction writes to an address the tracer knows.
//
// A bank switch and a shadow update both look exactly like this, which is the
// point of returning them rather than acting on them here: the stepper stays
// a stepper and the tracer decides what a write to 5000h means.
type Store struct {
	// Addr is where it goes, Val the byte or unknown, Src the address the
	// byte was loaded from or nosrc.
	Addr, Val, Src int
}

// shadowReader is what the stepper is allowed to know about memory: nothing,
// except the value of a bank shadow byte. Reading anything else would be
// reading RAM whose contents the tracer has no business predicting.
type shadowReader interface {
	shadowValue(addr int) int
}

// edLdFrom and edLdTo are the ED-prefixed `ld rr,(nn)` and `ld (nn),rr`.
var edLdFrom = map[byte]int{0x4B: 0, 0x5B: 1, 0x6B: 2}
var edLdTo = map[byte]int{0x43: 0, 0x53: 1, 0x63: 2}

// Step applies one instruction to the abstract state and reports what it
// wrote to addresses the tracer can name.
func Step(r *Regs, view dis.Reader, ins dis.Insn, ctx shadowReader) []Store {
	op, addr := ins.Op, ins.Addr

	// load reads a byte the tracer might know: only bank shadows qualify.
	load := func(a int) (int, int) {
		if ctx == nil {
			return unknown, a
		}
		return ctx.shadowValue(a), a
	}

	switch op {
	case 0xDD, 0xFD:
		// Only `ld ix,nn` and `ld iy,nn` are modelled; the rest of the
		// index-register set is rare enough that forgetting is cheaper
		// than being careful.
		set := func(v int) {
			if op == 0xDD {
				r.IX = v
			} else {
				r.IY = v
			}
		}
		switch {
		case ins.Sub == 0x21 && ins.Len == 4:
			set(int(view.Word(addr + 2)))
		case ins.Sub >= 0x40 && ins.Sub <= 0xBF:
			r.Set(rA, unknown, nosrc)
		default:
			set(unknown)
		}
		return nil

	case 0xCB:
		// Everything but `bit`, which is flags only.
		if ins.Sub < 0x40 || ins.Sub >= 0x80 {
			r.Set(int(ins.Sub&7), unknown, nosrc)
		}
		return nil

	case 0xED:
		sub := ins.Sub
		if rp, ok := edLdFrom[sub]; ok { // ld rr,(nn)
			src := int(view.Word(addr + 2))
			v, s := load(src)
			r.Set(pairLo[rp], v, s)
			v, s = load(src + 1)
			r.Set(pairHi[rp], v, s)
			return nil
		}
		if rp, ok := edLdTo[sub]; ok { // ld (nn),rr
			dst := int(view.Word(addr + 2))
			hi, lo := pairHi[rp], pairLo[rp]
			return []Store{
				{dst, r.v[lo], r.src[lo]},
				{dst + 1, r.v[hi], r.src[hi]},
			}
		}
		switch sub { // the block moves and searches
		case 0xA0, 0xA8, 0xB0, 0xB8, 0xA1, 0xA9, 0xB1, 0xB9:
			for rp := 0; rp < 3; rp++ {
				r.SetPair(rp, unknown, nosrc)
			}
			r.Set(rA, unknown, nosrc)
			return nil
		}
		r.Clear()
		return nil
	}

	switch {
	// -- 8-bit immediate loads ------------------------------------------
	case op == 0x06 || op == 0x0E || op == 0x16 || op == 0x1E ||
		op == 0x26 || op == 0x2E || op == 0x3E:
		r.Set(int(op>>3&7), int(view.Byte(addr+1)), nosrc)
		return nil
	case op == 0x36: // ld (hl),n
		if hl := r.Pair(2); hl != unknown {
			return []Store{{hl, int(view.Byte(addr + 1)), nosrc}}
		}
		return nil

	// -- 16-bit immediate loads -----------------------------------------
	case op == 0x01 || op == 0x11 || op == 0x21:
		r.SetPair(int(op>>4&3), int(view.Word(addr+1)), nosrc)
		return nil
	case op == 0x31: // ld sp,nn
		r.ClearStack()
		return nil

	// -- loads from memory ----------------------------------------------
	case op == 0x3A: // ld a,(nn)
		v, s := load(int(view.Word(addr + 1)))
		r.Set(rA, v, s)
		return nil
	case op == 0x2A: // ld hl,(nn)
		src := int(view.Word(addr + 1))
		v, s := load(src)
		r.Set(rL, v, s)
		v, s = load(src + 1)
		r.Set(rH, v, s)
		return nil
	case op == 0x0A || op == 0x1A: // ld a,(bc) / ld a,(de)
		r.Set(rA, unknown, nosrc)
		return nil

	// -- stores ----------------------------------------------------------
	case op == 0x32: // ld (nn),a
		return []Store{{int(view.Word(addr + 1)), r.v[rA], r.src[rA]}}
	case op == 0x22: // ld (nn),hl
		dst := int(view.Word(addr + 1))
		return []Store{
			{dst, r.v[rL], r.src[rL]},
			{dst + 1, r.v[rH], r.src[rH]},
		}
	case op == 0x02: // ld (bc),a
		if bc := r.Pair(0); bc != unknown {
			return []Store{{bc, r.v[rA], r.src[rA]}}
		}
		return nil
	case op == 0x12: // ld (de),a
		if de := r.Pair(1); de != unknown {
			return []Store{{de, r.v[rA], r.src[rA]}}
		}
		return nil
	case op >= 0x70 && op <= 0x77 && op != 0x76: // ld (hl),r
		if hl := r.Pair(2); hl != unknown {
			k := int(op & 7)
			return []Store{{hl, r.v[k], r.src[k]}}
		}
		return nil

	// -- register to register --------------------------------------------
	case op >= 0x40 && op <= 0x7F:
		v, s := r.Get(int(op & 7))
		r.Set(int(op>>3&7), v, s)
		return nil

	// -- arithmetic worth keeping precise --------------------------------
	case op == 0xAF: // xor a
		r.Set(rA, 0, nosrc)
		return nil
	case op&0xC7 == 0x04: // inc r
		k := int(op >> 3 & 7)
		if v, _ := r.Get(k); v != unknown {
			r.Set(k, (v+1)&0xFF, nosrc)
		} else {
			r.Set(k, unknown, nosrc)
		}
		return nil
	case op&0xC7 == 0x05: // dec r
		k := int(op >> 3 & 7)
		if v, _ := r.Get(k); v != unknown {
			r.Set(k, (v-1)&0xFF, nosrc)
		} else {
			r.Set(k, unknown, nosrc)
		}
		return nil
	case op == 0x03 || op == 0x13 || op == 0x23 || op == 0x33: // inc rr
		rp := int(op >> 4 & 3)
		if v := r.Pair(rp); v != unknown {
			r.SetPair(rp, (v+1)&0xFFFF, nosrc)
		} else {
			r.SetPair(rp, unknown, nosrc)
		}
		return nil
	case op == 0x0B || op == 0x1B || op == 0x2B || op == 0x3B: // dec rr
		rp := int(op >> 4 & 3)
		if v := r.Pair(rp); v != unknown {
			r.SetPair(rp, (v-1)&0xFFFF, nosrc)
		} else {
			r.SetPair(rp, unknown, nosrc)
		}
		return nil
	case op == 0xEB: // ex de,hl
		for _, p := range [2][2]int{{rD, rH}, {rE, rL}} {
			va, sa := r.Get(p[0])
			vb, sb := r.Get(p[1])
			r.Set(p[0], vb, sb)
			r.Set(p[1], va, sa)
		}
		return nil
	case op == 0xC6 || op == 0xD6 || op == 0xE6 || op == 0xF6 || op == 0xEE:
		a := r.v[rA]
		n := int(view.Byte(addr + 1))
		if a == unknown {
			r.Set(rA, unknown, nosrc)
			return nil
		}
		switch op {
		case 0xC6:
			a = (a + n) & 0xFF
		case 0xD6:
			a = (a - n) & 0xFF
		case 0xE6:
			a &= n
		case 0xF6:
			a |= n
		case 0xEE:
			a ^= n
		}
		r.Set(rA, a, nosrc)
		return nil
	case op == 0xFE || (op >= 0xB8 && op <= 0xBF): // cp: flags only
		return nil
	case (op >= 0x80 && op <= 0xB7) || op == 0xCE || op == 0xDE:
		r.Set(rA, unknown, nosrc)
		return nil
	case op == 0x07 || op == 0x0F || op == 0x17 || op == 0x1F ||
		op == 0x27 || op == 0x2F:
		r.Set(rA, unknown, nosrc)
		return nil
	case op == 0x09 || op == 0x19 || op == 0x29 || op == 0x39: // add hl,rr
		r.SetPair(2, unknown, nosrc)
		return nil
	case op == 0x34 || op == 0x35: // inc/dec (hl)
		return nil

	// -- the stack --------------------------------------------------------
	case op == 0xC1 || op == 0xD1 || op == 0xE1: // pop rr
		rp := int(op >> 4 & 3)
		r.Pop(pairHi[rp], pairLo[rp])
		return nil
	case op == 0xF1: // pop af
		if len(r.stk) > 0 {
			e := r.stk[len(r.stk)-1]
			r.stk = r.stk[:len(r.stk)-1]
			r.Set(rA, e.hv, e.hs)
		} else {
			r.Set(rA, unknown, nosrc)
		}
		return nil
	case op == 0xC5 || op == 0xD5 || op == 0xE5: // push rr
		rp := int(op >> 4 & 3)
		r.Push(pairHi[rp], pairLo[rp])
		return nil
	case op == 0xF5: // push af
		av, as := r.Get(rA)
		r.stk = append(r.stk, stkEntry{av, as, unknown, nosrc})
		if len(r.stk) > maxStack {
			r.stk = r.stk[len(r.stk)-maxStack:]
		}
		return nil
	case op == 0xE3: // ex (sp),hl
		r.SetPair(2, unknown, nosrc)
		r.ClearStack()
		return nil
	case op == 0xF9: // ld sp,hl
		r.ClearStack()
		return nil

	// -- the shadow set ---------------------------------------------------
	case op == 0x08: // ex af,af'
		r.Set(rA, unknown, nosrc)
		return nil
	case op == 0xD9: // exx
		for rp := 0; rp < 3; rp++ {
			r.SetPair(rp, unknown, nosrc)
		}
		return nil

	case op == 0xDB: // in a,(n)
		r.Set(rA, unknown, nosrc)
		return nil

	// nop, di, ei, out (n),a, scf, ccf, halt: nothing to model.
	case op == 0x00 || op == 0xF3 || op == 0xFB || op == 0xD3 ||
		op == 0x37 || op == 0x3F || op == 0x76:
		return nil
	}

	switch ins.Kind {
	case dis.Jp, dis.Jr, dis.Djnz, dis.Ret, dis.Reti, dis.Ijp:
		if op == 0x10 { // djnz decrements b
			if b, _ := r.Get(rB); b != unknown {
				r.Set(rB, (b-1)&0xFF, nosrc)
			} else {
				r.Set(rB, unknown, nosrc)
			}
		}
		return nil
	}

	// Calls and anything unmodelled: assume the worst.
	r.Clear()
	return nil
}
