//go:build !msxdiscover

package z80

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// runFinished unwinds out of translated code when the interpreter has taken
// over and run the rest of the way. Go has no other way back: the generated
// function is a forest of labels with no return in reach of the place that
// gave up, and falling through would run whatever label came next in the file.
type runFinished struct{}

// hijacked unwinds out of a delivered interrupt whose handler abandoned the
// interrupt and became the main thread -- reset the stack, jumped away, and
// will never return. See deliver.
type hijacked struct{}

// run is Run with the hand-off caught. Everything in this package that enters
// translated code goes through it rather than calling Run directly.
func (m *M) run(entry uint16) { m.runFrom(entry, m.SP) }

// runFrom is run with the stack level named rather than taken from where the
// call happens to start.
//
// It matters for a delivered interrupt. deliver pushes the ten-word register
// frame the BIOS pushes before calling a handler, and a handler is entitled to
// pop that frame itself rather than returning through it -- Space Manbow's
// does. Marking the level *after* those pushes then tells the interpreter the
// call is over the moment the handler restores its registers, and the rest of
// the handler never runs: the game stops responding to its start button, which
// is what the whole of it was doing. The mark belongs below the frame, where
// deliver was entered.
func (m *M) runFrom(entry uint16, mark uint16) {
	was := m.runMark
	m.runMark = mark
	defer func() { m.runMark = was }()
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(runFinished); ok {
				return
			}
			if _, ok := r.(hijacked); ok {
				// A nested interrupt's handler took the machine
				// over. Returning lets this level's deliver see
				// the stack above its own frame and cascade the
				// same conclusion outward.
				return
			}
			panic(r)
		}
	}()
	if m.transStale {
		// The floppy's code is not the code that was translated: run
		// it the way run_stub does, wholly interpreted, which is
		// always correct.
		m.push(sentinel)
		m.PC = entry
		m.idle, m.halted = false, false
		m.Interpret(m.runMark, maxInterpSteps)
		return
	}
	m.Run(entry)
}

// noLabel is reached when the translated code jumps somewhere the tracer never
// proved reachable.
//
// By default the interpreter takes over from there and runs the rest of the
// way, which makes the translation's coverage a question of how fast the game
// is rather than whether it works at all. The address is written down, because
// an address reached this way is code and translating it next time is free
// speed. See interp.go.
//
// Fussy, on, is the older answer: stop, loudly, naming the address and the
// bank. A static translation that quietly carried on would be executing
// whatever label happened to come next, which is a wrong answer with no
// symptom -- and while msx2go is being taught a new cartridge, that report is
// worth more than a game that limps.
func (m *M) noLabel(a uint16) {
	if !m.Fussy {
		m.fellBack(a)
		m.PC = a
		// A fresh trajectory: whatever settled into an idle loop
		// before was a different one. Interpret stops the moment it
		// sees this flag, so leaving it set means the interpreter is
		// handed an entry point and returns without running an
		// instruction of it -- which is what happened to the first
		// disk module, whose interrupt handler is entirely
		// interpreted because none of it is translated yet. Run in
		// run_stub.go has always cleared it for the same reason.
		//
		// A halt is the same kind of flag and needs the same
		// treatment: it means "stopped, waiting for an interrupt",
		// and a stale one makes Interpret return without running an
		// instruction of the entry point it was handed. Leaving it
		// set here wedged every cartridge whose INIT halts, but only
		// in a generated module -- msxrun enters Interpret directly
		// and never sees this path -- which is the kind of divergence
		// between interpreting a cartridge and running its
		// translation that this project exists to not have.
		m.idle, m.halted = false, false
		// And with the stack level this call began at. Interpret
		// stops when the stack unwinds past it, which is how it knows
		// the routine it was handed has returned -- a `ret` that pops
		// something other than the sentinel, which is what a routine
		// that jumps out through the stack does, otherwise runs on
		// into whatever follows. Run in run_stub.go has always passed
		// it; this passed zero, and Space Manbow ran off into its own
		// variables the moment a game was started.
		m.Interpret(m.runMark, maxInterpSteps)
		panic(runFinished{})
	}
	where := fmt.Sprintf("%04Xh", a)
	if off := m.Offset(a); off >= 0 && m.mem.mapper.BankSize > 0 &&
		m.mem.mapper.Name != "none" {
		where = fmt.Sprintf("%04Xh in bank %d (offset %05Xh)",
			a, off/m.mem.mapper.BankSize, off)
	}
	panic(fmt.Sprintf("z80: jumped to %s, which the tracer never "+
		"reached. Either the trace stopped short -- a bank switch it "+
		"could not evaluate, an indirect jump whose table it did not "+
		"find -- or this is data being executed. Run msx2go -discover "+
		"to feed this address back into the trace and try again.",
		where))
}

// fellBack records an address the interpreter had to take over at, with the
// banks in force, in the form msx2go -sites reads.
func (m *M) fellBack(a uint16) {
	var s string
	if m.mem.mapper.Name == "none" {
		s = fmt.Sprintf("%04X", a)
	} else {
		b := make([]string, 0, len(m.mem.bank))
		for _, n := range m.mem.bank {
			b = append(b, strconv.Itoa(n))
		}
		s = fmt.Sprintf("%04X %s", a, strings.Join(b, ","))
	}
	if m.fallbackSeen == nil {
		m.fallbackSeen = map[string]bool{}
	}
	if !m.fallbackSeen[s] {
		m.fallbackSeen[s] = true
		m.discovered = append(m.discovered, s)
	}
}

// Discovered is every address the interpreter had to take over at.
func (m *M) Discovered() []string { return m.discovered }

// DispatchNote is called wherever the translated code hands an address back
// to be looked up -- an indirect jump, a computed return, a jump into another
// chunk. It does nothing here and is inlined away; the discovery build
// records what it sees. See nolabel_discover.go.
func (m *M) DispatchNote() {}

// biosUnknown stops, loudly. The entry point is not part of the image, so it
// cannot be translated; it has to be written into bios.go, and a game that
// reaches one deserves better than a wrong answer.
// biosOnGrid reports whether an address is one of the BIOS's jump-table
// entries. The table is a three-byte grid from 0000h, and its last entry is
// 0159h on an MSX1, 0177h on an MSX2 and 017Dh on an MSX2+ -- counted from
// the real ROMs. An address on the grid is a routine this machine has not
// written yet; one off it is a program that has run off into data.
func biosOnGrid(a uint16) bool { return a <= 0x017D && a%3 == 0 && a >= 0x0038 }

// subRomUnknown reports a sub-ROM routine nobody has written yet, naming what
// it belongs to. It returns failure rather than stopping, for the same reason
// the main table does.
func (m *M) subRomUnknown(ix uint16) {
	if m.Fussy {
		panic(fmt.Sprintf("z80: the sub-ROM routine at %04Xh (%s) is "+
			"not implemented", ix, subRomGroup(ix)))
	}
	if m.warnedBIOS == nil {
		m.warnedBIOS = map[uint16]bool{}
	}
	if !m.warnedBIOS[ix] {
		m.warnedBIOS[ix] = true
		fmt.Fprintf(os.Stderr, "z80: sub-ROM %04Xh is not implemented "+
			"(%s); returning failure\n", ix, subRomGroup(ix))
	}
	m.A, m.Fc = 0, true
}

func (m *M) biosUnknown(addr uint16) {
	if !m.Fussy && biosOnGrid(addr) {
		// Say so once and carry on with a failure the caller can
		// read: carry set, A zero. A cartridge that calls a routine
		// nobody has written yet then behaves as though the machine
		// lacked whatever it asked for, which is a game that runs
		// with something missing rather than a game that stops.
		if m.warnedBIOS == nil {
			m.warnedBIOS = map[uint16]bool{}
		}
		if !m.warnedBIOS[addr] {
			m.warnedBIOS[addr] = true
			// IX and IY matter for the two that call into the
			// sub-ROM: they name the routine being asked for.
			fmt.Fprintf(os.Stderr, "z80: BIOS %04Xh (%s) is not "+
				"implemented; returning failure "+
				"(ix=%04X iy=%04X a=%02X hl=%04X)\n",
				addr, biosName(addr), m.IX, m.IY, m.A, m.HL())
		}
		m.A, m.Fc = 0, true
		return
	}
	// Where it was called from matters more than which entry it was. The
	// low addresses are the restart vectors, and a program reaching one
	// has almost always not called it: it has run off into data, where FFh
	// is `rst 38h`, F7h is `rst 30h` and C7h is `rst 00h`. So say where
	// the machine was and what the stack thinks called it, which is the
	// difference between "add a shim" and "something derailed".
	ret := uint16(m.Mem[m.SP]) | uint16(m.Mem[m.SP+1])<<8
	how := "It is not part of the image and has to be shimmed; add it to bios.go"
	if addr < 0x40 && addr%8 == 0 {
		how = "This is a restart vector, which is what running off into " +
			"data looks like: FFh is `rst 38h`. Something jumped " +
			"somewhere it should not have."
	}
	panic(fmt.Sprintf("z80: unimplemented BIOS call %04Xh (%s) at pc=%04Xh "+
		"sp=%04Xh, stack says %04Xh called it. %s",
		addr, biosName(addr), m.PC, m.SP, ret, how))
}

// bridgeInto hands a called routine to the translation mid-interpretation:
// the interpreter executed the call, so the real return address is on the
// stack, and the mark is the stack level just after it -- the routine is
// over the moment the stack rises past it, exactly the rule Interpret
// stops by. runMark is saved and restored so a nested noLabel inside the
// bridged run interprets to this level and no further.
func (m *M) bridgeInto(entry, mark uint16) {
	was := m.runMark
	m.runMark = mark
	m.bridgeDepth++
	defer func() { m.bridgeDepth-- }()
	defer func() {
		m.runMark = was
		if r := recover(); r != nil {
			if _, ok := r.(runFinished); ok {
				return
			}
			if _, ok := r.(hijacked); ok {
				// The routine took the machine over; the
				// interpreter's own mark checks see the new
				// stack next iteration.
				return
			}
			panic(r)
		}
	}()
	m.RunAt(entry)
}

// retBail is the translation's exit check at every ret, after the
// sentinel. Two reasons to hand back: the stack rose above the mark --
// the routine the caller wanted is over, which is the interpreter's own
// stopping rule -- or a bridged run has spent the frame's cycle budget.
// The program counter was just popped, so execution resumes exactly here
// in the interpreter: bailing is a deoptimisation, never a divergence.
func (m *M) retBail() bool {
	if m.runMark != 0 && m.SP > m.runMark {
		return true
	}
	return m.bridgeDepth > 0 && m.cycLimit != 0 && m.Cyc >= m.cycLimit
}
