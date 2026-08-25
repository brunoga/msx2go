package z80

import (
	"fmt"
	"os"
)

// What an instruction costs.
//
// The rest of this machine deliberately has no notion of time: a translated
// instruction is a Go statement and takes as long as it takes. That is fine
// until a cartridge's interrupt handler does more work than a frame's worth of
// cycles, because on the hardware the next interrupt then arrives *while the
// handler is still running*, and a handler that guards itself against
// re-entry -- most do -- sees the guard set and returns at once. The game's
// logic advances once per two or three frames, and that is not a defect of the
// game, it is how it was tuned.
//
// A machine that finishes every handler instantly never overruns, so it never
// hits the guard, so it runs the game at the full frame rate: Salamander's
// gameplay comes out three times too fast. Counting cycles is what makes the
// overrun visible. See M.Frame.
//
// These are the published Z80 T-state counts. Where an instruction's cost
// depends on what it did -- a conditional that branched, a block instruction
// that repeated -- the extra is charged at the point the decision is made, in
// interp.go.

// CPUClock is the Z80's rate on an MSX, in T-states per second. It is twice
// the PSG's Clock, which is the same crystal divided.
const CPUClock = 3579545

// cycBase is the cost of each unprefixed opcode, taking the *untaken* side of
// any conditional. CB, DD, ED and FD are prefixes and cost nothing here; their
// tables charge the whole instruction.
var cycBase = [256]uint16{
	// 0x00
	4, 10, 7, 6, 4, 4, 7, 4, 4, 11, 7, 6, 4, 4, 7, 4,
	// 0x10  djnz and jr are the untaken costs
	8, 10, 7, 6, 4, 4, 7, 4, 12, 11, 7, 6, 4, 4, 7, 4,
	// 0x20
	7, 10, 16, 6, 4, 4, 7, 4, 7, 11, 16, 6, 4, 4, 7, 4,
	// 0x30
	7, 10, 13, 6, 11, 11, 10, 4, 7, 11, 13, 6, 4, 4, 7, 4,
	// 0x40  ld r,r' -- 7 when either side is (HL)
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	7, 7, 7, 7, 7, 7, 4, 7, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0x80  alu a,r -- 7 for (HL)
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	4, 4, 4, 4, 4, 4, 7, 4, 4, 4, 4, 4, 4, 4, 7, 4,
	// 0xC0  ret cc / call cc are the untaken costs; CB DD ED FD are prefixes
	5, 10, 10, 10, 10, 11, 7, 11, 5, 10, 10, 0, 10, 17, 7, 11,
	5, 10, 10, 11, 10, 11, 7, 11, 5, 4, 10, 11, 10, 0, 7, 11,
	5, 10, 10, 19, 10, 11, 7, 11, 5, 4, 10, 4, 10, 0, 7, 11,
	5, 10, 10, 4, 10, 11, 7, 11, 5, 6, 10, 4, 10, 0, 7, 11,
}

// A taken conditional costs more than an untaken one -- five more for a jr,
// six for a ret, seven for a call -- and neither the interpreter nor the
// translated code charges it.
//
// The translated code cannot: the extra is only knowable at the branch, and
// working it out there would mean threading a cycle charge into every
// conditional the emitter writes. The interpreter could, and did, and that was
// worse than not doing it -- because the two then kept different clocks, ran
// different overruns, and drifted apart. Interpreting a cartridge and running
// its translation have to give the same answer; that invariant is what the
// sweep, the pruning and every comparison in this project rest on, and it is
// worth more than a few percent of timing accuracy.
//
// So both undercount branch-heavy code by the same few percent, on purpose.
// The fix is to make the emitter charge it, not to make the interpreter
// diverge.

// Prefix and index costs. A DD or FD prefix is one extra fetch; reaching for
// (IX+d) instead of (HL) costs the displacement fetch and the addition.
const (
	cycPrefix = 4
	cycIndex  = 8
)

// cycED is the cost of each ED-prefixed opcode, prefix included. The block
// instructions are charged per iteration in interp.go; the entry here is one
// pass. Undefined codes are two-byte no-ops.
func cycED(sub byte) uint16 {
	switch sub {
	case 0x43, 0x53, 0x63, 0x73, 0x4B, 0x5B, 0x6B, 0x7B: // ld (nn),dd / ld dd,(nn)
		return 20
	case 0x67, 0x6F: // rrd, rld
		return 18
	case 0x42, 0x52, 0x62, 0x72, 0x4A, 0x5A, 0x6A, 0x7A: // sbc/adc hl,dd
		return 15
	case 0x45, 0x4D, 0x55, 0x5D, 0x65, 0x6D, 0x75, 0x7D: // retn, reti
		return 14
	case 0x47, 0x4F, 0x57, 0x5F: // ld i,a / ld r,a / ld a,i / ld a,r
		return 9
	case 0x44, 0x4C, 0x54, 0x5C, 0x64, 0x6C, 0x74, 0x7C: // neg
		return 8
	case 0x46, 0x4E, 0x56, 0x5E, 0x66, 0x6E, 0x76, 0x7E: // im
		return 8
	}
	if sub >= 0xA0 && sub <= 0xBB { // the block instructions, one pass
		return 16
	}
	if sub&0xC7 == 0x40 || sub&0xC7 == 0x41 { // in r,(c) / out (c),r
		return 12
	}
	return 8
}

// cycBlockRepeat is what each iteration of a repeating block instruction costs
// beyond the last one.
//
// It is 21, not 5. A repeating block instruction does not run its loop inside
// the processor: it decrements PC and is *fetched and executed again*, so every
// pass but the last costs the whole 21 T-states, and only the final pass costs
// 16. Charging the difference instead of the whole thing undercounts an OTIR
// of a hundred bytes by sixteen hundred cycles, and a handler that pushes
// sprites to video memory that way then looks three times cheaper than it is.
const cycBlockRepeat = 21

// cycHalt is what a halted processor costs per step: it fetches nothing and
// the clock runs on, four T-states at a time, until an interrupt arrives.
const cycHalt = 4

// tick charges cycles to the frame's budget. It takes a wide count because a
// block instruction over a whole page costs far more than a byte's worth: an
// LDIR of sixteen kilobytes is a third of a million T-states.
func (m *M) tick(n uint32) {
	m.Cyc += uint64(n)
	if m.Timed() {
		m.dueVblank()
		m.dueLine()
		m.dueIRQ()
	}
}

// Tick is tick, for generated code to call. Every translated instruction calls
// it, which makes it the one place in a translated program where time can be
// seen to pass -- and so the only place an interrupt can arrive in the middle
// of something, which is what the hardware does and what a handler that
// overruns its frame depends on.
func (m *M) Tick(n uint32) {
	m.Cyc += uint64(n)
	if m.Timed() {
		m.dueVblank()
		m.dueLine()
		m.dueIRQ()
	}
}

// status2 derives S#2's raster bits from the clock: where in the frame the
// beam would be. VR (bit 6) holds through the vertical blanking; HR (bit 5)
// rises in the horizontal blanking at the end of every line -- roughly the
// last quarter of each line's cycles, which is close enough for a loop whose
// iterations cost a tenth of a line.
func (m *M) status2() byte {
	perLine := m.FrameCycles() / 262
	if perLine == 0 {
		return 0
	}
	pos := m.Cyc - m.frameOrigin
	// Our frame begins at the vertical blank; the flags speak the
	// display's language, where line 0 is the top of the picture. Getting
	// this wrong is not cosmetic: a game gates its video uploads on VR,
	// and one that is told "displaying" during the blanking and
	// "blanking" mid-display uploads at exactly the wrong times.
	line := (pos/perLine + uint64(m.VDP.Lines())) % 262
	inLine := pos % perLine
	var s byte
	if line >= uint64(m.VDP.Lines()) {
		s |= 0x40
	}
	// The horizontal retrace covers the first quarter of the line, not the
	// last. Calibrated against the reference machine through the game that
	// depends on it: Space Manbow's handler waits for this flag to rise
	// before a raster-timed transfer, and counting how many times it looks
	// says which way round the line is. With the flag at the end of the
	// line the handler read it six times a transfer; with it at the start,
	// 1.06 times against the reference machine's 1.05.
	if inLine < perLine/4 {
		s |= 0x20
	}
	// The command engine's two flags: still working, and wants the next
	// byte. See VDP.Busy and VDP.TransferReady.
	if m.VDP.Busy() {
		s |= 0x01
	}
	if m.VDP.TransferReady() {
		s |= 0x80
	}
	return s
}

// rasterLine is where the beam would be right now, in lines from the top of
// the frame.
func (m *M) rasterLine() int {
	perLine := m.FrameCycles() / 262
	if perLine == 0 {
		return 0
	}
	return int((m.Cyc - m.frameOrigin) / perLine % 262)
}

// rasterDisplayLine is rasterLine in the display's coordinates: our frame
// begins at the vertical blank, so our line 0 is the hardware's line 212 (or
// 192), and the display's line 0 is fifty lines into our frame. A write made
// during the blanking returns -1: it lands before the visible frame and is
// part of its base state.
func (m *M) rasterDisplayLine() int {
	l := (m.rasterLine() + m.VDP.Lines()) % 262
	if l >= m.VDP.Lines() {
		return -1
	}
	return l
}

// FrameOrigin is the cycle the current frame began at.
func (m *M) FrameOrigin() uint64 { return m.frameOrigin }

// dueVblank raises the vertical-blank flag on the clock, for the main-thread
// shape of cartridge. A handler is entitled to sit and poll S#0 for the *next*
// frame -- on the hardware the flag rises every sixtieth of a second no matter
// what the processor is doing -- and a flag that can only rise between host
// frames never satisfies a poll inside one. Armed per frame by InterpretRun;
// handler-shaped games never arm it, and stay exactly as verified.
func (m *M) dueVblank() {
	// fDue is the next boundary at which a vertical blank is still owed.
	// Raising it here advances fDue, so the frame that starts afterwards
	// knows the flag is already up and does not raise it again.
	if m.fDue != 0 && m.Cyc >= m.fDue {
		m.fDue += m.FrameCycles()
		m.VDP.StartFrame()
	}
}

// armLine schedules the line-interrupt flag for this frame: FH rises when the
// raster reaches the line register 19 names, measured in lines from the
// vertical blank the frame just delivered.
//
// It cannot be an event delivered alongside the vblank, because a handler is
// entitled to sit in `di` and *poll* for it -- Space Manbow's ISR does exactly
// that, spinning on S#1 until the raster catches up, and only then moving the
// screen. A flag that can only rise between interrupts never rises inside that
// loop, and the game scrolls nothing, ever. Rising out of tick, mid-ISR, is
// what the raster does.
func (m *M) armLine(frameStart uint64) {
	_ = frameStart
	m.rearmLine()
}

// rearmLine schedules the next line interrupt from the registers as they
// stand right now. It runs at the top of each frame and again on every write
// to register 19 or 23, because the compare is *relative to the vertical
// scroll* -- the interrupt fires when the raster reaches display line
// (R19 - R23) & 255, measured on the reference machine: Space Manbow's split
// lands at (DBh - C0h) & FFh = line 27, the bottom of its HUD, not at line
// 219 of anything. And since a split handler re-points R19 for the next
// split, re-arming on the write is what lets several line interrupts land in
// one frame, each where the game asked.
//
// Only when the cartridge has asked for line interrupts: the reference
// machine reads S#1 as zero through Space Manbow's whole intro, with IE1 off
// and R19 at zero, so the flag does not latch unarmed. A stale FH would greet
// every vblank ISR that checks S#1 first and send it down the line branch
// forever.
func (m *M) rearmLine() {
	if !m.VDP.LineIRQEnabled() {
		// IE1 down masks the interrupt; it does not cancel the
		// compare. Whatever was already scheduled stays scheduled,
		// and dueLine decides at firing time whether the request is
		// allowed through -- which is where the enable belongs.
		//
		// Clearing it here was the other half of the same mistake as
		// never re-aiming on register 0. A handler that turns IE1
		// off, does its work and turns it back on wiped its own
		// pending split on the way past: Space Manbow lost one split
		// in three, and every third frame drew the whole screen in
		// the status panel's page and scroll -- flashing garbage over
		// the playfield, and a panel that looked frozen.
		//
		// Returning rather than recomputing also keeps a machine that
		// never enables line interrupts -- every MSX1 cartridge --
		// from scheduling one it has no hardware for.
		return
	}
	perLine := m.FrameCycles() / 262
	if perLine == 0 {
		return
	}
	d := uint64(m.VDP.Reg[19]-m.VDP.Reg[23]) & 0xFF
	our := d + 262 - uint64(m.VDP.Lines())
	due := m.frameOrigin + our*perLine
	if m.IRQTrace != nil {
		m.IRQTrace(fmt.Sprintf("arm for %d at", int(our)), m.rasterLine())
	}
	if due <= m.Cyc {
		if m.IRQTrace != nil {
			m.IRQTrace(fmt.Sprintf("TOO LATE for %d at", int(our)), m.rasterLine())
		}
		// Already behind the raster: this frame's chance has passed,
		// and the next frame's armLine will see it.
		m.fhDue = 0
		return
	}
	m.fhDue = due
}

// dueLine raises the line-interrupt flag when its raster time comes, and
// delivers the interrupt for it where the cartridge asked for one and is in a
// position to take it. A handler polling under `di` takes the flag alone.
//
// The request outlives the moment it is raised. The raster does not wait for
// a main thread sitting inside `di` -- a block copy, a bank switch -- but the
// interrupt does: the hardware holds INT asserted while FH stands, and the
// processor takes it at the next `ei`. Dropping it instead left one frame in
// twenty of Space Manbow with no split at all, the whole screen drawn in the
// status panel's mode and its scroll, which is the flash. Measured: the
// reference machine writes register 19 exactly twice in each of 401 gameplay
// frames, ours in only 284 of 300.
func (m *M) dueLine() {
	if m.fhDue != 0 && m.Cyc >= m.fhDue {
		m.fhDue = 0
		m.VDP.StartLine()
		m.fhHeld = true
		if m.IRQTrace != nil {
			m.IRQTrace("raised", m.rasterLine())
		}
	}
	if !m.fhHeld {
		return
	}
	// The flag is cleared by the handler's own read of S#1, and the
	// request goes with it.
	if !m.VDP.FHPending() || !m.VDP.LineIRQEnabled() {
		m.fhHeld = false
		return
	}
	if !m.IFF || m.nest >= maxNest || m.booting {
		if m.IRQTrace != nil {
			m.IRQTrace("held", m.rasterLine())
		}
		return
	}
	if entry, ok := m.InterruptEntry(); ok {
		m.fhHeld = false
		if m.IRQTrace != nil {
			m.IRQTrace("delivered", m.rasterLine())
		}
		m.deliver(entry)
	}
}

// FrameRunaway is how many frames' worth of cycles one frame may spend before
// the machine decides it is not coming back. A heavy frame costs three or four;
// a level load can cost twenty. A hundred is not a slow frame, it is a loop
// with no way out.
//
// Without cycle counting a translated program that gets stuck simply stops:
// Run never returns, the harness never draws again, and the sound thread keeps
// playing the last registers it was given, so it sounds alive and looks dead.
// Counting makes it reportable -- and recoverable, since the interpreter
// fallback already has the machinery to unwind out of translated code.
const FrameRunaway = 100

// BootRunaway is how long INIT may run before the machine concludes that INIT
// *is* the game loop rather than a preamble to one. Ten seconds of machine
// time: no cartridge spends that setting itself up.
const BootRunaway = 600

// BootStuck is how long INIT may go with interrupts enabled and none arriving
// before the clock steps in. One second, which is a long time to be waiting:
// King's Valley and Salamander both get theirs from the BIOS well inside it
// and are untouched by this, and Space Manbow -- which waits on a counter no
// BIOS call of ours will ever bump -- gets moving.
const BootStuck = 60

// maxNest is how deep interrupts may stack. The hardware has no limit but a
// cartridge that lets them stack deeply has already lost; this stops a runaway
// from taking the Go stack with it.
const maxNest = 4

// dueIRQ delivers an interrupt that has come due while something else was
// still running.
//
// This is the whole of what a frame budget is for. A handler that does more
// work than a frame has cycles for gets interrupted part way through, exactly
// as it would on the hardware; the interrupt runs the handler again, the
// cartridge's own re-entry guard sends it straight back out, and the
// interrupted work carries on. What that buys is the thing skipping the frame
// outright gets wrong: the sound driver lives at the top of the handler, above
// the guard, so it keeps running once per frame while the game's logic
// advances once per three. Skip the handler and the music slows down with it.
// budgetOf is one frame's cycles, for readability at the call site.
func budgetOf(m *M) uint64 { return m.FrameCycles() }

func (m *M) dueIRQ() {
	if m.booting && m.Cyc > BootRunaway*m.FrameCycles() && !m.bootStop {
		// INIT has been running for ten seconds of machine time without
		// settling. It is not going to: this cartridge's game loop is
		// INIT itself. See Boot.
		//
		// The hand-back happens at the next instruction *boundary*, not
		// here. This is called from tick, in the middle of an
		// instruction whose opcode is already fetched; a panic from
		// here leaves PC pointing into the operand bytes, and a main
		// thread resumed there executes an address as an opcode and is
		// dead within a frame.
		m.MainThread = true
		if m.interpDepth > 0 {
			// Interpreted INIT: stop at the next instruction
			// boundary, cleanly.
			m.bootStop = true
		} else {
			// Translated INIT: there is no boundary to stop at --
			// the position is Go control flow, not a PC -- so unwind
			// and let Boot report the shape. A cartridge that lands
			// here should be regenerated, so its metadata says
			// MainThread and its INIT interprets from the start.
			panic(runFinished{})
		}
	}
	if m.frameStart != 0 && m.Cyc-m.frameStart > FrameRunaway*m.FrameCycles() {
		m.runaway()
	}
	if !m.IFF || m.nest >= maxNest {
		return
	}
	// A vertical blank that came due while the main thread was inside
	// `di`, taken now that it is not. The flag is cleared by whoever
	// reads S#0, and the request goes with it.
	if m.fHeld {
		if !m.VDP.FPending() {
			if m.IRQTrace != nil {
				m.IRQTrace("vblank DROPPED (flag read away)", 0)
			}
			m.fHeld = false
		} else if entry, ok := m.InterruptEntry(); ok {
			m.fHeld = false
			m.lastDeliver = m.Cyc
			if m.IRQTrace != nil {
				m.IRQTrace("vblank delivered late", 0)
			}
			m.deliver(entry)
			return
		} else {
			// Held for a hook that does not exist. The BIOS takes
			// it instead; see below.
			m.fHeld = false
			m.lastDeliver = m.Cyc
			m.VDP.AckVblank()
		}
	}
	// During INIT the interrupts a cartridge gets are the ones the BIOS
	// lets in during its own calls -- see bootInterrupt, which King's
	// Valley's boot sequence was checked against at length.
	//
	// That model has nothing to offer a cartridge whose INIT enables
	// interrupts itself and then waits for one without calling the BIOS.
	// Space Manbow does exactly that: `ei` at 4306h, then a spin on a
	// counter its handler increments. It makes a handful of BIOS calls
	// before that, so "has the other model delivered anything" is the
	// wrong question -- the right one is whether it has delivered anything
	// *lately*. A second of machine time with interrupts enabled and
	// nothing arriving is a cartridge waiting for something that is never
	// coming, and then the clock delivers.
	if m.booting && m.Cyc-m.lastDeliver < BootStuck*budgetOf(m) {
		return
	}
	budget := m.FrameCycles()
	// lastIRQ can legitimately sit in the future -- the frame loop sets it
	// to the end of the frame to say "the clock owes nothing until then" --
	// and an unsigned subtraction against a future value underflows into
	// an enormous one. The symptom was an interrupt delivered on every
	// single main-thread instruction, which starved Space Manbow to one
	// instruction per frame and walked it into the ROM header's padding.
	if m.Cyc < m.lastIRQ || m.Cyc-m.lastIRQ < budget {
		return
	}
	entry, ok := m.InterruptEntry()
	if !ok {
		// The BIOS' own handler takes it: there is nothing of the
		// cartridge's to run, but the interrupt still happens and the
		// vertical-blank flag is still read away. Leaving it pending
		// saves it for the instant a hook appears -- which is the
		// instant a cartridge writes the jump byte, one instruction
		// before the address it jumps to.
		m.lastIRQ += budget
		m.lastDeliver = m.Cyc
		m.VDP.AckVblank()
		return
	}
	m.lastIRQ += budget
	if m.hasRAMMapper() && m.Cyc-m.lastIRQ >= budget {
		// More than one frame's worth of cycles has gone by since this
		// interrupt came due, so the ones in between did not happen.
		// The clock does not owe them: a vertical blank is a moment,
		// and a processor that was busy through several has missed
		// several, not earned a burst.
		//
		// Paying the debt back is what the accumulating form does, and
		// it delivers them as fast as the check runs -- one every few
		// thousand cycles instead of one every sixty thousand. A game
		// that waits on a counter its handler increments then races
		// itself: Snatcher's opening waits for exactly sixty-four, and
		// with interrupts arriving eight times too often the counter
		// stepped past sixty-four between the load and the compare,
		// every time, deterministically. It waited for ever, two
		// instructions from finishing.
		//
		// Disk machines only, which is where this was measured. Every
		// cartridge timing in this project was checked against the
		// reference machine with the debt being paid back -- Space
		// Manbow's loading phase and Castle Excellent's whole first
		// minute among them -- and those measurements stand until the
		// same check can be made for them. The physical argument says
		// a cartridge should have this too; the measurements have not
		// been redone, so it does not get it yet.
		m.lastIRQ = m.Cyc
	}
	m.lastDeliver = m.Cyc
	if !m.booting {
		// During INIT the interrupts are not frames anyone is counting;
		// afterwards each one is a frame the harness would otherwise
		// deliver itself.
		m.irqTaken++
	}
	// Note: an interrupt is a vertical blank, so raising the flag here
	// looks right and measures worse -- Salamander matches the reference
	// on 363 of 400 checkpoints without it and 352 with. Something else is
	// wrong if both cannot be true, and until that is found the
	// measurement wins.
	m.armLine(m.Cyc)
	m.deliver(entry)
}

// deliver runs the cartridge's hook once, as an interrupt -- with the stack
// frame the real BIOS builds first.
//
// C-BIOS's handler at 199Ah pushes HL, DE, BC, AF, swaps to the shadow set,
// pushes those four, then IY and IX, and only then calls the hook. That frame
// is not bookkeeping the hook may ignore; it is part of the interface. A hook
// is entitled to unwind it *itself* -- pop the ten words, ei, ret straight to
// the interrupted code, skipping the BIOS epilogue -- and Space Manbow's does
// exactly that: its slow path ends at 4110h, which bumps the frame counter its
// main thread waits on and then pops its own way out. Call that hook with a
// bare stack and its epilogue eats whatever lies there and returns into it;
// here that was the ROM header's padding, four hundred frames in.
//
// The sentinel goes where the hardware pushed the interrupted PC, so both
// exits work: a hook that rets lands on the sentinel run pushes; one that
// unwinds the frame pops down to this one. Either way Interpret stops there
// and the saved machine is put back.
// deliverCatching is deliver with the hijack unwind caught, for callers that
// sit at an instruction boundary already -- the top of a frame -- where there
// is no half-done instruction to abort.
func (m *M) deliverCatching(entry uint16) {
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(hijacked); ok {
				return
			}
			panic(r)
		}
	}()
	m.deliver(entry)
}

func (m *M) deliver(entry uint16) {
	m.nest++
	saved := *m
	m.IFF = false
	m.push(sentinel)
	m.push(m.HL())
	m.push(m.DE())
	m.push(m.BC())
	m.push(m.AF())
	m.exx()
	m.exAF()
	m.push(m.HL())
	m.push(m.DE())
	m.push(m.BC())
	m.push(m.AF())
	m.push(m.IY)
	m.push(m.IX)
	// Where the stack now stands, with the frame on it. A handler that
	// returns normally rets through its sentinel and never pops past this
	// level; one that *abandons* the interrupt -- Space Manbow's press
	// handler does `ld sp,F0F0h` and jumps into the main loop, becoming
	// the main thread -- sends SP above it in one instruction, and that
	// is the tell.
	frameSP := m.SP
	// Every hook the cartridge installed, in the order the BIOS calls
	// them, inside the one interrupt. See InterruptEntries.
	hooks := m.InterruptEntries()
	if len(hooks) == 0 {
		hooks = []uint16{entry}
	}
	// The handler runs outside the frame's budget: the budget is the main
	// thread's alone, and a limit that cut a handler off at its first
	// instruction turned "an interrupt near the end of a busy frame" into
	// "an interrupt that never happened".
	limit := m.cycLimit
	m.cycLimit = 0
	// On a disk machine the hooks are reached through the BIOS's own
	// interrupt handler, and two things it does before any hook are part
	// of the machine: it reads status register zero, which acknowledges
	// the interrupt, and it advances the frame counter at FC9Eh.
	// Snatcher's intro paces itself on that counter -- its engine ticked
	// on a machine where nothing counted frames, so its script never
	// moved and the screen stayed on the first scene. Cartridge machines
	// are left as they were: the counter is measurable there too, but
	// every digest in this project's history was taken without it, and
	// none of those games reads it.
	if m.hasRAMMapper() {
		m.biosInterrupt()
	}
	for _, h := range hooks {
		m.runFrom(h, frameSP)
		if m.SP > frameSP {
			if m.IRQTrace != nil {
				m.IRQTrace(fmt.Sprintf("HIJACK pc=%04X sp=%04X frameSP=%04X from", m.PC, m.SP, frameSP), m.rasterLine())
			}
			m.cycLimit = limit
			// The handler did not return: it reset the stack and
			// jumped away, and on the hardware the interrupted
			// main thread is simply gone. Restoring the saved
			// state here is what resurrecting the dead looks
			// like: the old thread comes back to a machine whose
			// memory has moved on -- the game restarts a level
			// over half-changed state, which is a white screen, a
			// corrupt status panel, a ship that cannot die.
			//
			// So do what the hardware does: nothing. The machine
			// continues from wherever the handler went, and that
			// *is* the main thread now. Read from the cartridge:
			// 416Fh loads the new state, resets SP and jumps to
			// the main loop at 4357h; 42F2h, the soft restart,
			// has the same shape.
			//
			// The panic aborts the instruction the interrupt
			// arrived inside. The clock ticks partway through an
			// instruction, so this deliver may be running with an
			// opcode half-executed above it; letting that opcode
			// finish against the handler's state executes the
			// old thread's tail on the new thread's registers,
			// which is how the machine ended up running data at
			// 363Ah. Every interpreter loop catches this at its
			// instruction boundary and re-checks its own stack
			// mark, so the unwind cascades out of any nesting to
			// exactly the context that now owns the machine.
			m.nest--
			panic(hijacked{})
		}
	}
	m.cycLimit = limit
	m.restoreFrom(&saved)
	m.nest--
	m.IFF = true
	// Taking an interrupt resumes a halted processor: it carries on at
	// the instruction after the halt. restoreFrom put back the state as
	// it was when the interrupt arrived, halt and all, so this has to be
	// said after it.
	m.halted = false
}

// FrameCycles is how many T-states one frame of this machine has: the CPU's
// rate divided by the vertical frequency, scaled by CPUScale.
//
// CPUScale is the knob for what kind of machine this is. At 1 it is a stock
// 3.58 MHz Z80 and a handler that overruns a frame costs the game its next
// interrupt, which is what the hardware does and what the game was tuned for.
// Raise it and the handler stops overrunning: the game runs every frame,
// smoothly, and faster. Lower it and it overruns more.
//
// Counting is only skipped entirely when CPUScale is zero, which is the older
// behaviour -- no cycles, no overrun, every frame does full work.
func (m *M) FrameCycles() uint64 {
	hz := m.Hz
	if hz == 0 {
		hz = 60
	}
	scale := m.CPUScale
	if scale <= 0 {
		scale = 1
	}
	return uint64(float64(CPUClock) / float64(hz) * scale)
}

// Timed reports whether this machine charges for the time its work takes.
func (m *M) Timed() bool { return m.CPUScale >= 0 }

// cycVRAMByte is what one byte of a BIOS block transfer costs: the OUTI or the
// OUT/DEC/JR that carries it, read out of C-BIOS's loops at 0278h and 02A4h.
const cycVRAMByte = 26

// biosCost is the entry-and-exit cost of a BIOS call, before whatever work it
// then does. A shim is a Go function and takes no time at all; the routine it
// stands for is a subroutine on a 3.58 MHz Z80, and a cartridge that calls one
// a hundred times a frame is spending real cycles on it.
//
// These are rounded from the C-BIOS implementations. Exactness is not the
// point -- what matters is that calling the BIOS costs something, because a
// game whose handler overruns a frame does so largely through these.
func biosCost(addr uint16) uint16 {
	switch addr {
	case 0x0141: // SNSMAT: selects a row on the PSG and reads it back
		return 180
	case 0x0093, 0x0096: // WRTPSG, RDPSG
		return 90
	case 0x0047: // WRTVDP
		return 90
	case 0x004A, 0x004D: // RDVRM, WRTVRM: set the address, then one byte
		return 80
	case 0x0050, 0x0053: // SETRD, SETWRT
		return 60
	case 0x0056, 0x0059, 0x005C: // the block routines, before their bytes
		return 70
	case 0x000C, 0x0014, 0x0024: // RDSLT, WRSLT, ENASLT: slot arithmetic
		return 200
	case 0x0138, 0x013B, 0x013E: // RSLREG, WSLREG, RDVDP
		return 40
	}
	return 50
}

// CycleCost is what one decoded instruction costs, for code that is translated
// rather than interpreted. op is the opcode or the prefix, sub the byte after
// a prefix.
//
// Conditional instructions are charged their untaken cost; the extra a taken
// branch costs is not knowable from the opcode alone, and the translated code
// does not stop to work it out. That leaves the count a few percent low on
// branch-heavy code, which is the one approximation in here and is written
// down rather than hidden.
//
// A repeating block instruction is charged one pass; the rest are charged by
// the loop itself in machine.go, which is shared with the interpreter.
func CycleCost(op, sub byte) uint32 {
	switch op {
	case 0xCB:
		if sub&7 == 6 {
			if sub >= 0x40 && sub < 0x80 {
				return 12 // bit n,(hl)
			}
			return 15
		}
		return 8
	case 0xED:
		return uint32(cycED(sub))
	case 0xDD, 0xFD:
		if sub == 0xCB {
			return 23
		}
		n := uint32(cycPrefix) + uint32(cycBase[sub])
		if usesIndex(sub) {
			n += cycIndex
		}
		return n
	}
	return uint32(cycBase[op])
}

// usesIndex reports whether an opcode reaches memory through (HL), and so
// through (IX+d) when a prefix has redirected it.
func usesIndex(op byte) bool {
	switch {
	case op == 0x34 || op == 0x35 || op == 0x36:
		return true
	case op == 0x76:
		return false
	case op >= 0x40 && op < 0x80:
		return op&7 == 6 || (op>>3)&7 == 6
	case op >= 0x80 && op < 0xC0:
		return op&7 == 6
	}
	return false
}

// runaway abandons a frame that is never going to end.
//
// It unwinds out of the translated code the same way the interpreter fallback
// does, so the machine is left at a frame boundary and the next frame starts
// clean. What it cannot do is say where the loop was -- translated code carries
// its position in Go's control flow, not in m.PC -- so it reports the last
// address the dispatch saw, which is where to start looking.
func (m *M) runaway() {
	m.frameStart = m.Cyc
	if m.OnRunaway != nil {
		m.OnRunaway(m.PC, m.Banks())
	} else {
		fmt.Fprintf(os.Stderr, "z80: frame %d has run for %d frames' worth "+
			"of cycles without finishing; abandoning it. Last dispatch was "+
			"%04Xh, banks %v.\n", m.frames, FrameRunaway, m.PC, m.Banks())
	}
	panic(runFinished{})
}
