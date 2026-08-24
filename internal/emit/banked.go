package emit

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/brunoga/msx2go/internal/dis"
	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
)

// A banked cartridge, where an address is not enough to name an instruction.
//
// 8123h is a different byte depending on which bank is in the 8000h page, so a
// label cannot be the address: it is the offset in the image, which names the
// bank and the address together. The dispatch then has to work that offset out
// at run time, from whatever the mapper has mapped at the moment.
//
// Two things follow, and the second is the interesting one.
//
// The bank is **not** carried on the Z80 stack. A `ret` pops sixteen bits and
// lands wherever the mapper has that page pointed right then -- which is what
// the hardware does, and what makes an inter-bank trampoline work without the
// translation knowing anything about it. A routine that pages a bank in, calls
// into it, and pages the old one back on return is nothing special here: the
// dispatch simply looks again.
//
// And a jump *within the same page* still gets a direct `goto`. It has to be
// the same bank: the code doing the jumping is itself in that page, so nothing
// can have changed the mapping between the jump and its target. That covers
// almost every branch in a program, so the dispatch is only paid for the ones
// that genuinely cross a page -- which is exactly where the hardware pays too.

// pagedView reads the address space under one paging state, which is how an
// instruction's operands are found however close to the end of a bank it sits.
type pagedView struct {
	rom    []byte
	mapper z80.Mapper
	banks  []int
	nbanks int
}

func (v pagedView) off(a uint16) int {
	off := v.mapper.Phys(v.banks, int(a), v.nbanks)
	if off < 0 || off >= len(v.rom) {
		return -1
	}
	return off
}

func (v pagedView) Readable(a uint16, n int) bool {
	for i := 0; i < n; i++ {
		if v.off(a+uint16(i)) < 0 {
			return false
		}
	}
	return true
}

func (v pagedView) Byte(a uint16) byte {
	if o := v.off(a); o >= 0 {
		return v.rom[o]
	}
	return 0xFF
}

func (v pagedView) Word(a uint16) uint16 {
	return uint16(v.Byte(a)) | uint16(v.Byte(a+1))<<8
}

// nbanks is how many banks the image holds.
func (r Run) nbanks() int { return r.Mapper.BankCount(len(r.ROM)) }

// Site is one instruction of a banked cartridge: where it is in the image,
// what address it was decoded at, and the paging that was in force -- which is
// what its operands were read against, because an instruction at the end of a
// bank reads them out of the next page.
type Site struct {
	Off   int
	Addr  uint16
	Ins   dis.Insn
	Banks []int
}

// generateBanked writes the translation of a megaROM.
func (r Run) generateBanked() ([]byte, []error, error) {
	sites := append([]Site(nil), r.Sites...)
	sort.Slice(sites, func(i, j int) bool { return sites[i].Off < sites[j].Off })

	labels := map[int]bool{}
	for _, s := range sites {
		labels[s.Off] = true
	}

	// Too many for one function: see chunked.go.
	max := r.MaxChunk
	if max <= 0 {
		max = DefaultMaxChunk
	}
	if len(sites) > max {
		items := make([]chunkItem, 0, len(sites))
		for _, s := range sites {
			items = append(items, chunkItem{
				key:   s.Off,
				label: fmt.Sprintf("%05x", s.Off),
				ins:   s.Ins,
				ctx:   r.ctxFor(s, labels),
			})
		}
		// A target's key: the same page means the same bank, so the
		// offset is this instruction's bank plus the target's distance
		// into the page. Anything else is not nameable here, and the
		// trampoline handles it.
		byOff := make(map[int]Site, len(sites))
		for _, s := range sites {
			byOff[s.Off] = s
		}
		endKey := func(it chunkItem, target uint16) int {
			site, ok := byOff[it.key]
			if !ok {
				return -1
			}
			page := r.Mapper.PageOf(int(site.Addr))
			if page < 0 || r.Mapper.PageOf(int(target)) != page {
				return -1
			}
			bank := site.Off / r.Mapper.BankSize
			return bank*r.Mapper.BankSize +
				int(target) - r.Mapper.Pages[page][0]
		}
		return r.generateChunks(items, "m.Offset(m.PC)", endKey)
	}

	var b bytes.Buffer
	w := func(f string, args ...any) { fmt.Fprintf(&b, f+"\n", args...) }

	r.header(w)
	w("func (m *M) Run(entry uint16) {")
	w("\tm.push(sentinel)")
	w("\tm.PC = entry")
	w("\tgoto dispatch")
	w("")
	w("ret_:")
	w("\tm.PC = m.pop()")
	w("\tif m.PC == sentinel {")
	w("\t\treturn")
	w("\t}")
	w("")
	w("dispatch:")
	if r.TraceTransfers {
		w("\tm.DispatchNote()")
	}
	w("\t// Which bytes m.PC names depends on the paging right now, so the")
	w("\t// switch is on the offset in the image rather than the address.")
	w("\tswitch m.Offset(m.PC) {")
	for _, s := range sites {
		w("\tcase 0x%05x:", s.Off)
		w("\t\tgoto L%05x", s.Off)
	}
	w("\tdefault:")
	if r.BIOSTailCall {
		w("\t\t// A dynamic jump into page zero is a tail call into")
		w("\t\t// the BIOS, which is not part of the image and is")
		w("\t\t// shimmed rather than translated.")
		w("\t\tif m.PC < 0x4000 {")
		w("\t\t\tm.bios(m.PC)")
		w("\t\t\tgoto ret_")
		w("\t\t}")
	}
	w("\t\tm.noLabel(m.PC)")
	if r.RecoverOnNoLabel {
		w("\t\tgoto ret_")
	} else {
		w("\t\treturn")
	}
	w("\t}")
	w("")

	var bad []error
	for _, s := range sites {
		ctx := r.ctxFor(s, labels)
		body, err := ctx.Insn(s.Ins)
		if r.CountCycles {
			body = append([]string{fmt.Sprintf("m.Tick(%d)",
				z80.CycleCost(s.Ins.Op, s.Ins.Sub))}, body...)
		}
		if err != nil {
			bad = append(bad, err)
			body = []string{fmt.Sprintf("m.unsupported(0x%04x)", s.Addr)}
		}
		w("L%05x:", s.Off)
		for _, line := range body {
			w("\t%s", line)
		}
		if s.Ins.FallsThrough() {
			w("\t%s", ctx.jumpTo(s.Ins.End()))
		}
	}
	w("\tgoto ret_")
	w("}")
	w("")
	w("// RunAt and TranslatedAddrs satisfy the interpreter bridge, which")
	w("// a banked cartridge never crosses: an address does not name an")
	w("// instruction without the paging, so canBridge is always false")
	w("// here and RunAt only ever interprets.")
	w("func (m *M) RunAt(entry uint16) {")
	w("\tm.PC = entry")
	w("\tm.idle, m.halted = false, false")
	w("\tm.Interpret(m.runMark, maxInterpSteps)")
	w("}")
	w("")
	w("var TranslatedAddrs = []uint16{}")
	return finish(b, bad)
}

// ctxFor builds the translation context for one instruction: a reader over the
// bank it lives in, and the rule for turning a target address into a label.
func (r Run) ctxFor(s Site, labels map[int]bool) Ctx {
	page := r.Mapper.PageOf(int(s.Addr))
	bankSize := r.Mapper.BankSize
	bank := s.Off / bankSize
	start := bank * bankSize
	pageBase := 0
	if page >= 0 {
		pageBase = r.Mapper.Pages[page][0]
	}
	return Ctx{
		View:    pagedView{r.ROM, r.Mapper, s.Banks, r.nbanks()},
		Idle:    r.IdleOnSelfJump,
		Recover: r.RecoverOnNoLabel,
		Banked:  true,
		Offsets: func(target uint16) (int, bool) {
			// Same page, so the same bank: nothing can have changed
			// the mapping between here and there.
			if r.Mapper.PageOf(int(target)) != page {
				return 0, false
			}
			off := start + int(target) - pageBase
			return off, labels[off]
		},
	}
}
