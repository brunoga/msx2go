package emit

import (
	"bytes"
	"fmt"

	"github.com/brunoga/msx2go/internal/dis"
	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
)

// Splitting the translation across several functions.
//
// Everything lands in one function with a label per instruction, and for a
// 16K cartridge that is exactly right. For a 128K megaROM it is fatal: Go's
// compiler is superlinear in the number of basic blocks in a single function,
// and one Run holding twenty-four thousand of them made the compiler ask the
// kernel for fifty-six gigabytes and get killed for it. King's Valley's five
// and a half thousand compile in seconds; there is a cliff between.
//
// So a large cartridge is emitted as a set of chunk functions, each holding a
// slice of the instructions in address order. Within a chunk nothing changes:
// a jump is a `goto`, which is the whole point of the exercise. A transfer
// that leaves the chunk sets m.PC and returns to a trampoline, which is the
// same mechanism `jp (hl)` already used -- so this is not a new concept in
// the machine, only a wider application of one that was always there.
//
// Address order is what makes it cheap. Code is local: almost every jump goes
// somewhere near, so almost every jump stays inside its chunk and stays a
// `goto`. The trampoline is paid at the boundaries, which are few.

// chunkItem is one translated instruction, in the form both the flat and the
// banked path can hand to the chunked emitter.
type chunkItem struct {
	// key is what the dispatch switches on: an address for a flat
	// cartridge, an offset in the image for a banked one.
	key int
	// label is what this instruction is called, without the leading L.
	label string
	ins   dis.Insn
	ctx   Ctx
}

// generateChunks writes a translation split across several functions.
//
// keyExpr is the Go expression that turns m.PC into a dispatch key, and
// endKey turns an instruction's end address into one, which is how a
// fall-through finds its successor.
func (r Run) generateChunks(items []chunkItem, keyExpr string,
	endKey func(it chunkItem, end uint16) int) ([]byte, []error, error) {

	size := r.MaxChunk
	if size <= 0 {
		size = DefaultMaxChunk
	}
	var chunks [][]chunkItem
	for i := 0; i < len(items); i += size {
		j := i + size
		if j > len(items) {
			j = len(items)
		}
		chunks = append(chunks, items[i:j])
	}

	// Which chunk owns each key, and what each key is called.
	owner := make(map[int]int, len(items))
	label := make(map[int]string, len(items))
	for c, chunk := range chunks {
		for _, it := range chunk {
			owner[it.key] = c
			label[it.key] = it.label
		}
	}

	var b bytes.Buffer
	w := func(f string, args ...any) { fmt.Fprintf(&b, f+"\n", args...) }

	r.header(w)
	w("//")
	w("// Split across %d functions: see msx2go/internal/emit/chunked.go", len(chunks))
	w("// for why one function will not do at this size.")
	w("func (m *M) Run(entry uint16) {")
	w("\tm.push(sentinel)")
	w("\tm.RunAt(entry)")
	w("}")
	w("")
	w("// RunAt enters the translation with whatever the stack holds: no")
	w("// sentinel. It returns on the sentinel a caller pushed, or when the")
	w("// stack rises above m.runMark -- the interpreter's own stopping")
	w("// rule, which is what lets the two hand a routine back and forth.")
	w("func (m *M) RunAt(entry uint16) {")
	w("\tm.PC = entry")
	w("\tfor {")
	if r.TraceTransfers {
		w("\t\tm.DispatchNote()")
	}
	w("\t\tvar done bool")
	w("\t\tswitch chunkOf(%s) {", keyExpr)
	for c := range chunks {
		w("\t\tcase %d:", c)
		w("\t\t\tdone = m.runC%d()", c)
	}
	w("\t\tdefault:")
	w("\t\t\tm.noLabel(m.PC)")
	if r.RecoverOnNoLabel {
		w("\t\t\treturn")
	} else {
		w("\t\t\treturn")
	}
	w("\t\t}")
	w("\t\tif done {")
	w("\t\t\treturn")
	w("\t\t}")
	w("\t}")
	w("}")
	w("")
	w("// chunkStart is the first dispatch key of each chunk, ascending, so")
	w("// the trampoline can find the chunk that owns a key by bisection.")
	w("var chunkStart = [...]int32{")
	for _, chunk := range chunks {
		w("\t%d,", chunk[0].key)
	}
	w("}")
	w("")
	w("// chunkOf is the chunk whose key range contains key, or -1 below the")
	w("// first. A key inside a chunk's range but with no label of its own is")
	w("// caught by that chunk's own default.")
	w("func chunkOf(key int) int {")
	w("\tlo, hi := 0, len(chunkStart)")
	w("\tfor lo < hi {")
	w("\t\tmid := (lo + hi) / 2")
	w("\t\tif int(chunkStart[mid]) <= key {")
	w("\t\t\tlo = mid + 1")
	w("\t\t} else {")
	w("\t\t\thi = mid")
	w("\t\t}")
	w("\t}")
	w("\treturn lo - 1")
	w("}")

	var bad []error
	for c, chunk := range chunks {
		inChunk := func(target uint16, from chunkItem) (string, bool) {
			k := endKey(from, target)
			if owner[k] != c {
				return "", false
			}
			l, ok := label[k]
			return l, ok
		}
		// Emit the body first, so it is known whether ret_ is reached at
		// all: Go refuses a label nothing jumps to.
		var body bytes.Buffer
		bw := func(f string, args ...any) { fmt.Fprintf(&body, f+"\n", args...) }
		usesRet := false
		for _, it := range chunk {
			ctx := it.ctx
			ctx.Chunked = true
			ctx.InChunk = func(target uint16) (string, bool) {
				return inChunk(target, it)
			}
			out, err := ctx.Insn(it.ins)
			if r.CountCycles {
				out = append([]string{fmt.Sprintf("m.Tick(%d)",
					z80.CycleCost(it.ins.Op, it.ins.Sub))}, out...)
			}
			if err != nil {
				bad = append(bad, err)
				out = []string{fmt.Sprintf("m.unsupported(0x%04x)",
					it.ins.Addr)}
			}
			bw("L%s:", it.label)
			for _, s := range out {
				bw("\t%s", s)
				if containsRet(s) {
					usesRet = true
				}
			}
			if it.ins.FallsThrough() {
				s := ctx.jumpTo(it.ins.End())
				bw("\t%s", s)
				if containsRet(s) {
					usesRet = true
				}
			}
		}

		w("")
		w("func (m *M) runC%d() bool {", c)
		w("\tswitch %s {", keyExpr)
		for _, it := range chunk {
			w("\tcase 0x%x:", it.key)
			w("\t\tgoto L%s", it.label)
		}
		w("\tdefault:")
		w("\t\tm.noLabel(m.PC)")
		w("\t\treturn true")
		w("\t}")
		// Always: the chunk ends with a `goto ret_` of its own, which
		// is a use whether or not the body has one, and Go refuses a
		// label nothing uses just as firmly as one nothing defines.
		_ = usesRet
		w("")
		w("ret_:")
		w("\tm.PC = m.pop()")
		w("\tif m.PC == sentinel {")
		w("\t\treturn true")
		w("\t}")
		w("\tif m.retBail() {")
		w("\t\treturn true")
		w("\t}")
		w("\treturn false")
		w("")
		b.Write(body.Bytes())
		w("\tgoto ret_")
		w("}")
	}
	w("")
	w("// TranslatedAddrs is empty here: a banked cartridge's dispatch")
	w("// speaks offsets, not addresses, so the interpreter bridge never")
	w("// enters a chunked-banked translation. See canBridge in interp.go.")
	w("var TranslatedAddrs = []uint16{}")
	return finish(b, bad)
}

// containsRet reports whether a statement jumps to the chunk's ret_ label.
func containsRet(s string) bool {
	return bytes.Contains([]byte(s), []byte("goto ret_"))
}

// DefaultMaxChunk is how many instructions go in one function.
//
// Fifteen hundred is about six thousand lines, comfortably inside the range
// where the compiler's cost is linear-ish, and small enough that the cliff at
// twenty-odd thousand is nowhere in sight.
const DefaultMaxChunk = 1500
