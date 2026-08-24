// Package trace finds the code in a cartridge.
//
// Recursive descent from the entry points, following calls and jumps, with
// just enough abstract interpretation to answer the questions plain descent
// cannot: what does `jp (hl)` jump to, which bank is mapped where, and is this
// call followed by a table of pointers rather than by code.
//
// The interpretation is deliberately weak. Every register is a constant or
// unknown, and anything the stepper cannot model sets the affected registers
// to unknown. It under-approximates and never invents a value, because a
// wrong answer here is code decoded out of data and data decoded as code, and
// both of those are silent.
package trace

// Registers, by the numbering the Z80's own 8-bit encodings use, so that
// `ld r,r'` can index straight into it.
const (
	rB = iota
	rC
	rD
	rE
	rH
	rL
	rHL // (hl): not a register. Reads unknown, writes go nowhere.
	rA
	nregs
)

// unknown is a register whose value the tracer does not know, and nosrc a
// value that did not come from a known address.
const (
	unknown = -1
	nosrc   = -1
)

// pairs are the 16-bit register pairs, by the encoding's numbering: bc, de,
// hl, sp. SP is not modelled, which is why it has no halves.
var pairHi = [4]int{rB, rD, rH, -1}
var pairLo = [4]int{rC, rE, rL, -1}

// Regs is the register file as constant-or-unknown, with provenance.
//
// Two facts are kept per register: the constant it holds, and the address that
// constant was loaded from. Provenance is what makes bank shadows findable --
// when a bank register is written with a value the tracer cannot evaluate, the
// address it came from is exactly the shadow byte for that page.
type Regs struct {
	v   [nregs]int
	src [nregs]int
	IX  int
	IY  int
	// stk is an abstract stack of pushed pairs, so a value survives
	// `push bc ... pop bc` around a call. Calls are assumed stack
	// balanced, which is the convention every one of these ROMs follows.
	stk []stkEntry
}

type stkEntry struct{ hv, hs, lv, ls int }

// NewRegs is a register file that knows nothing.
func NewRegs() *Regs {
	r := &Regs{IX: unknown, IY: unknown}
	for i := range r.v {
		r.v[i], r.src[i] = unknown, nosrc
	}
	return r
}

// Copy is a deep copy: trace states travel with their own register file.
func (r *Regs) Copy() *Regs {
	n := *r
	n.stk = append([]stkEntry(nil), r.stk...)
	return &n
}

// A is the accumulator, which is the one register the visited set keys on --
// dispatchers index their tables with it, so two arrivals at the same address
// with different A are genuinely different states.
func (r *Regs) A() int { return r.v[rA] }

// Set writes a register. Writing (hl) is ignored, which is what makes the
// register numbering usable directly from the instruction encoding.
func (r *Regs) Set(k, value, src int) {
	if k == rHL || k < 0 || k >= nregs {
		return
	}
	r.v[k], r.src[k] = value, src
}

// Get reads a register and where its value came from.
func (r *Regs) Get(k int) (int, int) {
	if k == rHL || k < 0 || k >= nregs {
		return unknown, nosrc
	}
	return r.v[k], r.src[k]
}

// Clear forgets the registers. The stack survives: a callee restores it.
func (r *Regs) Clear() {
	for i := range r.v {
		r.v[i], r.src[i] = unknown, nosrc
	}
	r.IX, r.IY = unknown, unknown
}

// Pair reads a 16-bit pair, or unknown if either half is.
func (r *Regs) Pair(i int) int {
	hi, lo := pairHi[i], pairLo[i]
	if hi < 0 || r.v[hi] == unknown || r.v[lo] == unknown {
		return unknown
	}
	return r.v[hi]<<8 | r.v[lo]
}

// SetPair writes a 16-bit pair. srcBase, where given, is the address the low
// half came from; the high half came from the byte after it.
func (r *Regs) SetPair(i, value, srcBase int) {
	hi, lo := pairHi[i], pairLo[i]
	if hi < 0 {
		return
	}
	hiSrc := nosrc
	if srcBase != nosrc {
		hiSrc = srcBase + 1
	}
	if value == unknown {
		r.Set(hi, unknown, hiSrc)
		r.Set(lo, unknown, srcBase)
		return
	}
	r.Set(hi, value>>8&0xFF, hiSrc)
	r.Set(lo, value&0xFF, srcBase)
}

// maxStack is how deep the abstract stack is kept. Anything below that is a
// value no `pop` in the same routine is going to want.
const maxStack = 16

// Push and Pop model the pair pushes and pops, and nothing else.
func (r *Regs) Push(hi, lo int) {
	hv, hs := r.Get(hi)
	lv, ls := r.Get(lo)
	r.stk = append(r.stk, stkEntry{hv, hs, lv, ls})
	if len(r.stk) > maxStack {
		r.stk = r.stk[len(r.stk)-maxStack:]
	}
}

func (r *Regs) Pop(hi, lo int) {
	if len(r.stk) == 0 {
		r.Set(hi, unknown, nosrc)
		r.Set(lo, unknown, nosrc)
		return
	}
	e := r.stk[len(r.stk)-1]
	r.stk = r.stk[:len(r.stk)-1]
	r.Set(hi, e.hv, e.hs)
	r.Set(lo, e.lv, e.ls)
}

// ClearStack is what `ld sp,nn` and its friends do: whatever was there is not
// coming back.
func (r *Regs) ClearStack() { r.stk = r.stk[:0] }
