package emit

import (
	"bytes"
	"fmt"
	"go/format"
	"sort"

	"github.com/brunoga/msx2go/internal/dis"
	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
)

// Run assembles the whole of rom_gen.go.
//
// The instruction boundaries come from the trace, not from a linear re-walk of
// the code regions. Code overlaps -- `jr z,4F34h` lands one byte into the
// `cp 20h` at 4F33h, so those bytes are two different instructions depending
// on how they are entered -- and a linear walk sees only one of the two.
// Missing the other is what made King's Valley panic the first time a pickaxe
// was used.
type Run struct {
	// Package is the package clause, and Source what the header credits
	// the translation to.
	Package string
	Source  string
	// View reads the image for a flat cartridge, where an address is all
	// that is needed to find a byte.
	View dis.Reader
	// Starts is every address the tracer proved reachable, sorted. Flat
	// cartridges only.
	Starts []uint16
	// Sites are the instructions of a banked cartridge, which need more
	// than an address to name: 8123h is a different byte depending on
	// which bank is in the 8000h page, so the label is the offset in the
	// image and the dispatch has to work it out at run time.
	Sites []Site
	// Mapper and Base are how a logical address becomes an offset. Only
	// the banked path uses them.
	Mapper z80.Mapper

	// CountCycles makes each instruction charge what it costs, so a
	// handler that does more work than a frame has cycles for is seen to
	// overrun it. Without that a game tuned around the overrun -- most
	// action games are -- runs several times too fast. See
	// runtime/cycles.go.
	//
	// It is off by default because the test that this emitter is right is
	// that it reproduces what tools/z80togo.py wrote, and that had no
	// notion of time.
	CountCycles bool
	Base        uint16
	// ROM is the image, for reading a banked instruction's operands out of
	// the bank it was decoded in.
	ROM []byte
	// TraceTransfers emits the hook that lets a discovery build record
	// every dynamic transfer. It is a no-op call in an ordinary build and
	// is off by default, because the test that this emitter is right is
	// that it reproduces what tools/z80togo.py wrote.
	TraceTransfers bool
	// MaxChunk is how many instructions go in one Go function before the
	// translation is split across several. Zero takes the default; a
	// cartridge with fewer instructions than this is emitted whole. See
	// chunked.go for why a big one cannot be.
	MaxChunk int
	// BIOSTailCall sends a dynamic jump into page zero to the BIOS shim
	// rather than reporting it as unreachable. A `ret` or a `jp (hl)` into
	// the BIOS is an ordinary thing for a cartridge to do; the static
	// forms already go there, and this is the same rule for the forms
	// only the dispatch sees.
	//
	// Off by default, for the same reason IdleOnSelfJump is: the test that
	// this emitter is right is that it reproduces what tools/z80togo.py
	// wrote, and that does not have it.
	BIOSTailCall bool
	// RecoverOnNoLabel returns out of Run after reporting an address with
	// no label, instead of falling into whatever label happens to be next
	// in the file.
	//
	// It is dead code in an ordinary build, where noLabel panics. It is
	// what makes the discovery build possible: there, reaching an
	// untranslated address abandons the frame and records where it was,
	// so one run finds every place the cartridge goes that the trace
	// missed rather than only the first.
	RecoverOnNoLabel bool
	// IdleOnSelfJump compiles an unconditional jump to itself -- `jr $`,
	// `jp $` -- as a return out of Run rather than as a Go loop that
	// never ends.
	//
	// It is what a cartridge's INIT does when it has finished setting up:
	// spin until an interrupt takes the processor somewhere else. There
	// is nothing here to spin for, so the honest translation is to hand
	// control back and let the caller deliver the interrupt, which is
	// also what lets Boot stop exactly where the idle loop begins without
	// a step budget or a guess about how long INIT ought to take.
	//
	// Off by default, because the test that this emitter is right is that
	// it reproduces the file tools/z80togo.py wrote, and that file has a
	// game-specific Boot which inlines INIT instead.
	IdleOnSelfJump bool
}

// Generate returns the file's bytes, gofmt'd, and any instructions it had no
// translation for.
func (r Run) Generate() ([]byte, []error, error) {
	if len(r.Sites) > 0 {
		return r.generateBanked()
	}
	return r.generateFlat()
}

// generateFlat is the single-bank case, where an address names a byte and a
// label can be the address.
func (r Run) generateFlat() ([]byte, []error, error) {
	starts := append([]uint16(nil), r.Starts...)
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })

	type entry struct {
		addr uint16
		ins  dis.Insn
	}
	var addrs []entry
	labels := map[uint16]bool{}
	for _, a := range starts {
		ins, ok := dis.Decode(r.View, a)
		if !ok {
			continue
		}
		addrs = append(addrs, entry{a, ins})
		labels[a] = true
	}

	ctx := Ctx{View: r.View, Labels: labels, Idle: r.IdleOnSelfJump,
		Recover: r.RecoverOnNoLabel}
	var b bytes.Buffer
	w := func(f string, args ...any) {
		fmt.Fprintf(&b, f+"\n", args...)
	}

	r.header(w)
	w("func (m *M) Run(entry uint16) {")
	w("\tm.push(sentinel)")
	w("\tm.RunAt(entry)")
	w("}")
	w("")
	w("// RunAt enters the translation at entry with whatever the stack")
	w("// holds: no sentinel. It returns when a ret pops the sentinel a")
	w("// caller pushed, or when the stack rises above m.runMark -- the")
	w("// same rule the interpreter stops by, which is what lets the two")
	w("// hand a routine back and forth. See the bridge in interp.go.")
	w("func (m *M) RunAt(entry uint16) {")
	w("\tm.PC = entry")
	w("\tgoto dispatch")
	w("")
	w("ret_:")
	w("\tm.PC = m.pop()")
	w("\tif m.PC == sentinel {")
	w("\t\treturn")
	w("\t}")
	w("\tif m.retBail() {")
	w("\t\treturn")
	w("\t}")
	w("")
	w("dispatch:")
	if r.TraceTransfers {
		w("\tm.DispatchNote()")
	}
	w("\tswitch m.PC {")
	for _, e := range addrs {
		w("\tcase 0x%04x:", e.addr)
		w("\t\tgoto L%04x", e.addr)
	}
	w("\tdefault:")
	if r.BIOSTailCall {
		w("\t\t// A dynamic jump into the BIOS is a tail call into")
		w("\t\t// it: not part of the image, and shimmed rather")
		w("\t\t// than translated. Whether page zero *is* the BIOS")
		w("\t\t// is the machine's to answer, not an address range")
		w("\t\t// -- under the disk operating system page zero is")
		w("\t\t// RAM and the program itself is loaded at 0100h.")
		w("\t\tif m.isBIOS(m.PC) {")
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
	for _, e := range addrs {
		body, err := ctx.Insn(e.ins)
		if r.CountCycles {
			body = append([]string{fmt.Sprintf("m.Tick(%d)",
				z80.CycleCost(e.ins.Op, e.ins.Sub))}, body...)
		}
		if err != nil {
			bad = append(bad, err)
			body = []string{fmt.Sprintf("m.unsupported(0x%04x)", e.addr)}
		}
		w("L%04x:", e.addr)
		for _, s := range body {
			w("\t%s", s)
		}
		// Never rely on the next label in the file being the next
		// instruction: overlapping code means the instruction after
		// L4f33 in address order is L4f34, but execution goes to L4f35.
		if e.ins.FallsThrough() {
			if labels[e.ins.End()] {
				w("\tgoto L%04x", e.ins.End())
			} else {
				w("\t%s", ctx.noLabel(e.ins.End()))
			}
		}
	}
	w("\tgoto ret_")
	w("}")

	// Publish the address set so a test can check it against the tracer's
	// own instruction boundaries. The two are generated from the same
	// source here, but that is exactly the invariant worth pinning: an
	// earlier generator used a linear walk instead, and silently missed
	// every piece of overlapping code.
	w("")
	w("// TranslatedAddrs lists every address this file can be")
	w("// entered at, sorted. It must match the instruction starts")
	w("// in the trace report; internal/z80 has a test for that.")
	w("var TranslatedAddrs = []uint16{")
	var row []string
	for _, e := range addrs {
		row = append(row, fmt.Sprintf("0x%04x,", e.addr))
		if len(row) == 12 {
			w("\t%s", join(row))
			row = row[:0]
		}
	}
	if len(row) > 0 {
		w("\t%s", join(row))
	}
	w("}")
	return finish(b, bad)
}

func (r Run) header(w func(string, ...any)) {
	w("// Code generated by msx2go from %s. DO NOT EDIT.", r.Source)
	w("//")
	w("// A static translation of every instruction the tracer proved")
	w("// reachable. See msx2go/internal/emit for the shape and why the")
	w("// Z80 stack is modelled explicitly instead of using Go call frames.")
	w("")
	w("package %s", r.Package)
	w("")
	w("// Run executes translated ROM code starting at entry, returning when")
	w("// the matching ret pops the sentinel pushed here.")
}

func finish(b bytes.Buffer, bad []error) ([]byte, []error, error) {
	src, err := format.Source(b.Bytes())
	if err != nil {
		// Hand back the unformatted source anyway: a syntax error in
		// generated code is read by looking at the generated code.
		return b.Bytes(), bad, fmt.Errorf("emit: %w", err)
	}
	return src, bad, nil
}

func join(row []string) string {
	out := ""
	for i, s := range row {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
