package trace

// Banked images, and the one thing that makes them hard.
//
// A logical address means nothing on its own in a megaROM: 8123h is a
// different byte depending on which bank is in the 8000h page. So the tracer
// carries the paging state with every trace state, and a write to a bank
// register changes it for the rest of that walk. That much is mechanical.
//
// What is not mechanical is a write whose *value* the tracer cannot evaluate.
// Konami megaROMs keep a copy in RAM of which bank is in each page -- a bank
// shadow -- so a routine can page something in, do its work, and put back
// whatever was there:
//
//	ld a,(0F0F2h) : ld (9000h),a      ; page 2 goes back to what it was
//
// Reading that as "the bank is now unknown" stops the trace at the most
// important routines in the program. Reading it as "the bank is unchanged" is
// a guess, and a wrong one means decoding one bank's bytes as another's. But
// if 0F0F2h is *known* to be page 2's shadow, then by the program's own
// invariant the write restores the bank this path began with, and that is not
// a guess at all.
//
// So the shadows have to be found, and they are found from provenance: the
// stepper records which address each value was loaded from, and a bank
// register written with a value that came from RAM makes that address a
// candidate. A candidate is not a shadow -- a level's "which bank am I in"
// variable reads exactly the same -- so it is only believed once it has been
// seen from both sides: read *into* a bank register for a page, and written
// *alongside* a switch of that same page.

// shadowNote is what the tracer learns about a candidate shadow byte, from
// each of the two sides it has to be seen from.
type shadowNote struct {
	readAs    int // the page it was read into, or -1
	writtenAs int // the page it was stored beside, or -1
}

// recentStore is a store the tracer has just seen, kept so that a RAM write
// and a bank-register write carrying the same value can be paired. The two sit
// within a couple of instructions of each other, in either order.
type recentStore struct {
	addr, val int
	isReg     bool
	page      int
}

// shadowValue implements the stepper's view of memory: a bank shadow whose
// value this path knows, and nothing else. Predicting any other RAM byte would
// be inventing the program's state.
func (v view) shadowValue(addr int) int {
	i, ok := v.t.shadowIdx[addr]
	if !ok || v.shadows == nil || i >= len(v.shadows) {
		return unknown
	}
	return v.shadows[i]
}

// shadowPage is the page a RAM byte shadows, or -1 if it is not one.
//
// A plain map lookup will not do: a missing key reads as zero, and zero is a
// page. That mistake made every address look like page 0's shadow, so nothing
// was ever a candidate and the trace stopped at the first restore it met.
func (t *Tracer) shadowPage(addr int) int {
	if p, ok := t.shadowMap[addr]; ok {
		return p
	}
	return -1
}

// note is what is known about a candidate so far, with -1 for "not seen".
func (t *Tracer) note(addr int) shadowNote {
	if n, ok := t.shadowNotes[addr]; ok {
		return n
	}
	return shadowNote{readAs: -1, writtenAs: -1}
}

// noteSwitchSource records that a bank register was written with a value that
// came from RAM, which makes that address a candidate shadow for the page.
func (t *Tracer) noteSwitchSource(src, page int) {
	if src == nosrc || t.shadowPage(src) >= 0 {
		return
	}
	// A shadow lives in RAM. The cartridge occupies the mapper's pages, so
	// anything inside them -- or down in the BIOS -- cannot be one.
	if t.mapper.PageOf(src) >= 0 || src < 0x8000 {
		return
	}
	n := t.note(src)
	if n.readAs < 0 {
		n.readAs = page
	}
	t.shadowNotes[src] = n
	t.confirmShadow(src, page)
}

// noteStore pairs a RAM store with a bank-register write carrying the same
// value, looking both ways because the two can come in either order.
func (t *Tracer) noteStore(addr, value int, isReg bool, page int) {
	if value == unknown {
		return
	}
	for _, prev := range t.recent {
		if prev.val != value {
			continue
		}
		switch {
		case isReg && !prev.isReg:
			n := t.note(prev.addr)
			if n.writtenAs < 0 {
				n.writtenAs = page
			}
			t.shadowNotes[prev.addr] = n
			t.confirmShadow(prev.addr, page)
		case prev.isReg && !isReg:
			n := t.note(addr)
			if n.writtenAs < 0 {
				n.writtenAs = prev.page
			}
			t.shadowNotes[addr] = n
			t.confirmShadow(addr, prev.page)
		}
	}
	t.recent = append(t.recent, recentStore{addr, value, isReg, page})
	if len(t.recent) > 6 {
		t.recent = t.recent[len(t.recent)-6:]
	}
}

// confirmShadow believes a candidate once it has been seen from both sides.
func (t *Tracer) confirmShadow(addr, page int) {
	n := t.note(addr)
	if n.readAs == page && n.writtenAs == page && t.shadowPage(addr) < 0 {
		if _, seen := t.res.ShadowCandidates[addr]; !seen {
			t.res.ShadowCandidates[addr] = page
		}
	}
}

// applyStores acts on what an instruction wrote: a bank switch, a shadow
// update, or neither. It reports the paging that follows and whether the walk
// has to stop because the mapping is no longer known.
func (t *Tracer) applyStores(stores []Store, addr uint16, banks, entry,
	shadows []int) (nb, nsh []int, stop bool) {
	nb, nsh = banks, shadows
	for _, st := range stores {
		page := t.mapper.SwitchPage(st.Addr)
		t.noteStore(st.Addr, st.Val, page >= 0, page)

		if page >= 0 {
			t.noteSwitchSource(st.Src, page)
			if st.Val == unknown {
				if t.shadowPage(st.Src) == page {
					// Page `page` goes back to its shadow, whose
					// value this path happens not to know. The
					// shadow holds "the bank in this page", so
					// by the program's own invariant this puts
					// back whatever was there when the path
					// began -- which is exactly what a routine
					// that pages something in temporarily is
					// doing.
					t.res.ShadowRestores = append(
						t.res.ShadowRestores, Restore{addr, page})
					nb = withBank(nb, page, entry[page])
					continue
				}
				// A genuinely unknown bank. Carrying on with the
				// old one would be a guess, and a wrong guess
				// makes the tracer decode another bank's bytes as
				// this one's code. Stop and say so.
				t.res.UnresolvedSwitches = append(
					t.res.UnresolvedSwitches, Restore{addr, page})
				stop = true
				continue
			}
			bank := t.mapper.Mask(st.Val, t.nbanks)
			t.res.BankSwitches = append(t.res.BankSwitches,
				BankSwitch{addr, page, bank})
			t.observe(page, bank)
			nb = withBank(nb, page, bank)
			continue
		}
		if i, ok := t.shadowIdx[st.Addr]; ok {
			v := unknown
			if st.Val != unknown {
				v = t.mapper.Mask(st.Val, t.nbanks)
				t.observe(t.shadowPage(st.Addr), v)
			}
			if nsh[i] != v {
				nsh = withBank(nsh, i, v)
			}
		}
	}
	return nb, nsh, stop
}

// observe records that a bank was seen in a page, which is what a later round
// speculates from when a switch cannot be resolved.
func (t *Tracer) observe(page, bank int) {
	if page < 0 {
		return
	}
	for _, b := range t.res.ObservedPages[page] {
		if b == bank {
			return
		}
	}
	t.res.ObservedPages[page] = append(t.res.ObservedPages[page], bank)
}

// withBank is a copy of a paging state with one entry changed. Copies, because
// the state travels with every queued walk and sharing it would let one path
// rewrite another's history.
func withBank(v []int, i, b int) []int {
	if i < 0 || i >= len(v) || v[i] == b {
		return v
	}
	out := append([]int(nil), v...)
	out[i] = b
	return out
}
