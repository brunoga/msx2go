//go:build msxdiscover

package z80

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The discovery build: reaching an address with no label writes it down and
// gives up on the frame, rather than stopping the program.
//
// Static analysis of a megaROM runs out of certainty long before it runs out
// of program, and every place it gave up is code that will not be translated.
// Running the cartridge settles it -- the address and the paging in force are
// exactly the pair the tracer needs to carry on from -- and giving up on one
// frame rather than the whole run means a single pass finds every place the
// cartridge goes that the trace missed, not just the first.
//
// The frame that is abandoned leaves the machine part-way through its work, so
// what happens next is not the cartridge's own behaviour any more. That is
// accepted deliberately: this build exists to map the program, not to play it.
func (m *M) noLabel(a uint16) {
	banks := make([]string, 0, len(m.mem.bank))
	for _, b := range m.mem.bank {
		banks = append(banks, strconv.Itoa(b))
	}
	line := fmt.Sprintf("%04x %s", a, strings.Join(banks, ","))
	for _, seen := range m.discovered {
		if seen == line {
			return
		}
	}
	m.discovered = append(m.discovered, line)
	// From here on the machine is off its own path, so nothing more it
	// does is evidence about the cartridge.
	m.dispatchStopped = true
	// Reported the moment it happens, not at exit: the machine may well
	// not reach exit -- past this point it is running on fake returns and
	// anything can follow -- and a miss that is never printed is a miss
	// the discovery loop never learns about. The stack goes with it, for
	// the human reading the log.
	stack := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		stack = append(stack, fmt.Sprintf("%04x", m.rd16(m.SP+uint16(i*2))))
	}
	fmt.Fprintf(os.Stderr, "MSX2GO-NOLABEL %s # sp=%04x stack=%s\n",
		line, m.SP, strings.Join(stack, " "))
}

// DispatchNote records a dynamic transfer.
//
// This is the useful half of discovery, and it is worth more than the crash
// is. A static trace stops at exactly those transfers whose target is not in
// the instruction -- `jp (hl)` through a table indexed at run time, a return
// address computed and pushed, a bank switch it could not evaluate -- and
// every one of them resolves, at run time, to an address that is certainly
// code, because the machine is about to execute it.
//
// So rather than learn one address per run by crashing into it, record every
// dynamic transfer the cartridge makes while it is still on its own correct
// path. A single run yields hundreds, all of them ground truth. Recording
// stops at the first missing label: past that the machine is running on fake
// returns and what it does next is nobody's cartridge.
func (m *M) DispatchNote() {
	if m.dispatchStopped {
		return
	}
	banks := make([]string, 0, len(m.mem.bank))
	for _, b := range m.mem.bank {
		banks = append(banks, strconv.Itoa(b))
	}
	line := fmt.Sprintf("%04x %s", m.PC, strings.Join(banks, ","))
	if m.dispatchSeen == nil {
		m.dispatchSeen = map[string]bool{}
	}
	if m.dispatchSeen[line] {
		return
	}
	m.dispatchSeen[line] = true
	fmt.Fprintf(os.Stderr, "MSX2GO-TRANSFER %s\n", line)
}

// biosUnknown in this build records and carries on. Once a fake return has
// happened the machine is off the cartridge's real path, and whatever it
// calls down there is a symptom; stopping on it would hide every miss that
// comes after.
func (m *M) biosUnknown(addr uint16) {
	fmt.Fprintf(os.Stderr, "MSX2GO-BADBIOS %04x\n", addr)
}

// Discovered is every address this run reached that had no label, with the
// paging that was in force, in the form msx2go -discover reads back.
func (m *M) Discovered() []string { return m.discovered }
