package z80

// Run executes cartridge code from an entry point, returning when the matching
// ret pops the sentinel it pushed.
//
// In a generated module this file is not present: rom_gen.go supplies a Run
// made of thirty thousand labels, one per instruction the tracer proved
// reachable, and that is the whole point of the translation. Inside msx2go
// itself there is no such file, so Run interprets -- which is what lets the
// same machine be driven over a cartridge that has never been translated at
// all. See interp.go for why that matters to discovery.
func (m *M) Run(entry uint16) {
	// The stack level to measure a return against is the one run recorded
	// on the way in, not the one here: for a delivered interrupt they
	// differ by the register frame deliver pushed, which a handler may pop
	// itself. See runFrom in nolabel.go.
	m.push(sentinel)
	m.PC = entry
	m.idle, m.halted = false, false
	m.Interpret(m.runMark, maxInterpSteps)
}

// RunAt is Run without the sentinel: enter at entry with whatever the
// stack holds and interpret to the current mark. The generated modules
// translate it; here everything is the interpreter anyway.
func (m *M) RunAt(entry uint16) {
	m.PC = entry
	m.idle, m.halted = false, false
	m.Interpret(m.runMark, maxInterpSteps)
}

// TranslatedAddrs is empty here: nothing is translated, so the bridge in
// interp.go never fires. The generated rom_gen.go supplies the real list.
var TranslatedAddrs = []uint16{}
