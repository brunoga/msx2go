package z80

import "fmt"

// Boot and Frame reproduce what the machine does around the cartridge, which
// is very little: run the cartridge's INIT, then deliver an interrupt sixty
// times a second.
//
// A cartridge declares itself in a sixteen-byte header at the start of its
// first page: the letters "AB", then INIT, STATEMENT, DEVICE and TEXT as
// sixteen-bit addresses. The BIOS calls INIT with the cartridge paged in, and
// a game's INIT sets the hardware up, installs an interrupt hook and then --
// almost always -- sits in a one-instruction loop, `jr $`, which only the
// interrupt escapes. The translation compiles that self-jump to a return, so
// Boot stops exactly where the idle loop begins without a step budget or a
// heuristic about how long INIT ought to take.
//
// The hook is whichever of H.KEYI or H.TIMI the cartridge filled in. H.KEYI
// is called at the very start of the BIOS interrupt handler, earlier and
// cheaper than H.TIMI, which is why games that want the whole frame use it.
const (
	// hKeyI and hTimI are the two interrupt hooks a cartridge can install,
	// each a three-byte `jp nnnn` the BIOS calls into.
	hKeyI = 0xFD9A
	hTimI = 0xFD9F
	// hStke is the hook the BIOS calls once it has finished setting the
	// machine up. A cartridge that wants to start after the BIOS rather
	// than during it puts a jump there and returns from its INIT.
	hStke = 0xFEDA
)

// Header is the cartridge header at the base of the first page.
type Header struct {
	// Valid says the "AB" signature was there.
	Valid bool
	// Init is the entry point the BIOS calls. Zero means the cartridge has
	// none, which for a game means something is wrong.
	Init uint16
	// Statement, Device and Text are the BASIC hooks. Games leave them
	// zero; they are read so that a cartridge which is not a game can be
	// recognised rather than misdiagnosed.
	Statement, Device, Text uint16
}

// ReadHeader reads the cartridge header at a logical address, which for a
// cartridge in the usual slot is 4000h.
func (m *M) ReadHeader(base uint16) Header {
	h := Header{Valid: m.Mem[base] == 'A' && m.Mem[base+1] == 'B'}
	h.Init = m.rd16(base + 2)
	h.Statement = m.rd16(base + 4)
	h.Device = m.rd16(base + 6)
	h.Text = m.rd16(base + 8)
	return h
}

// Boot runs the cartridge's initialisation and leaves the machine ready for
// Frame. It reports an error rather than hanging if the cartridge does not
// look like a game.
func (m *M) Boot(base uint16) error {
	m.InstallSystemBytes()
	m.IFF = false
	m.IM = 1
	// Where the BIOS leaves the stack. A game's INIT sets its own almost
	// immediately; this is only so that the few pushes before it go
	// somewhere sensible.
	m.SP = 0xF380

	h := m.ReadHeader(base)
	if !h.Valid {
		return fmt.Errorf("z80: no AB signature at %04Xh", base)
	}
	if h.Init == 0 {
		return fmt.Errorf("z80: cartridge at %04Xh has no INIT", base)
	}
	return m.runEntry(h.Init, "INIT")
}

// runEntry runs a program's entry point until it settles into the idle loop a
// game leaves for its interrupt handler, or until the machine decides the
// entry point *is* the game loop. It is the whole of booting that a cartridge
// and a disk have in common: one arrives here from a cartridge header, the
// other from the BLOAD that a disk's BASIC loader ended with.
//
// what names the entry point in anything it has to report.
func (m *M) runEntry(entry uint16, what string) error {
	// Run pushes a sentinel so it knows when to return, and a game's INIT
	// loads its own stack pointer within a few instructions and never
	// returns at all -- so those two bytes are bookkeeping left behind in
	// a machine that has moved on. Put back what was there, because the
	// sentinel is an implementation detail of this translation and has no
	// business being visible in the machine's memory.
	sp0 := m.SP
	was := [2]byte{m.Mem[sp0-2], m.Mem[sp0-1]}

	m.idle, m.halted = false, false
	m.lastIRQ, m.Cyc = 0, 0
	// While INIT runs, the BIOS calls it makes deliver interrupts, the
	// way the real BIOS's own EIs do. See bootInterrupt.
	m.booting = true
	m.VDP.BootVblank = true
	if m.MainThread {
		// The game loop is the entry point: run it interpreted,
		// because it will have to be stopped at an instruction
		// boundary and resumed from a PC, and translated code can do
		// neither. See bootRun.
		m.bootRun(entry)
	} else {
		m.run(entry)
		// A halt during INIT is not INIT finishing: the hardware
		// waits for the next interrupt and carries on. Treating the
		// first halt as "settled into an idle loop" classified a
		// halt-driven cartridge as handler-shaped and froze its main
		// thread at the halt for ever -- the FRS edition of Space
		// Manbow boots that way.
		//
		// And an INIT that halts *repeatedly* is not setting up: the
		// halt loop is its main loop. Say so after a handful, and
		// hand it to the main-thread frame engine, which delivers an
		// interrupt every frame with no cap and clears the halt --
		// exactly the cadence the hardware gives it. Riding the
		// boot-time interrupt path instead ran into its 256-delivery
		// guard partway through the FRS logo, which is why that logo
		// stopped two strips in.
		for halts := 0; m.halted && !m.idle && !m.MainThread; halts++ {
			if halts >= 8 {
				m.MainThread = true
				break
			}
			m.halted = false
			m.bootInterrupt()
			m.Interpret(0, maxInterpSteps)
			if m.bootStop {
				m.bootStop = false
				break
			}
		}
	}
	m.booting = false
	m.VDP.BootVblank = false
	if m.SP != sp0-2 {
		m.Mem[sp0-2], m.Mem[sp0-1] = was[0], was[1]
	}
	if !m.idle {
		// Two shapes of cartridge end up here.
		//
		// One has finished: its INIT set the machine up, installed an
		// interrupt hook and returned, and everything from now on
		// happens in the handler. King's Valley and Salamander are
		// this shape, and it is the shape the translated build is
		// built around -- a frame is one call to the handler.
		//
		// The other has not finished at all: its game loop *is* INIT,
		// running in the main thread with the handler only ticking
		// counters underneath it. Space Manbow is this shape. There is
		// nothing wrong with such a cartridge and nothing to report
		// about it -- what there is, is a machine that has to keep
		// running it rather than call it again per frame.
		//
		// Telling them apart is easy: the second one is still running.
		// MainThread is set by the machine itself, in dueIRQ, when INIT
		// has run for ten seconds without settling.
		// The first shape has two endings, not one. King's Valley
		// settles into `jr $` and never returns; King's Valley II
		// installs its hook and *returns*, which is just as finished
		// -- the BIOS it returns to would idle, and everything after
		// that happens in the handler either way. Requiring the idle
		// loop turned the second ending into "this may not be a
		// game".
		//
		// So the question is not how INIT ended but whether it left
		// something to run: a hook means the handler shape, no hook
		// and not still running means there is genuinely nothing
		// here.
		// A third way for a cartridge to take over, and the one King's
		// Valley II uses: install the hook at FEDAh and return. The
		// BIOS calls that hook once it has finished setting the
		// machine up, so a cartridge that wants to start *after* the
		// BIOS rather than in the middle of it puts a restart, its
		// slot and its address there, and gets out of the way.
		//
		// Nothing here had ever called it, so the cartridge did
		// exactly what it was written to do and this machine
		// concluded it was not a game.
		if m.Mem[hStke] != 0xC9 {
			m.run(hStke)
			// A cartridge that starts from this hook does not
			// return from it: it takes the stack and runs. The
			// interpreter stops the moment the stack rises past
			// where the call began, so what comes back here is a
			// game a few instructions in, not a game that
			// finished. It is the main thread from now on, and
			// the frame engine carries it on from where it is.
			if !m.idle {
				if _, hooked := m.InterruptEntry(); !hooked {
					m.MainThread = true
				}
			}
		}
		if _, hooked := m.InterruptEntry(); !hooked && !m.MainThread {
			return fmt.Errorf("z80: %s at %04Xh returned without "+
				"installing an interrupt hook and without a "+
				"running main loop; this may not be a game",
				what, entry)
		}
	}
	// The BIOS enables interrupts once a cartridge's INIT has handed
	// control back -- but only then. A cartridge that is *still running*
	// has its own idea of whether interrupts are welcome, and King's
	// Valley II is in the middle of installing its interrupt hook: it
	// writes the jump byte, then the address three instructions later,
	// with interrupts off across both. Forcing them on here delivered one
	// into the gap, and the half-written hook jumped to whatever the hook
	// table happened to hold.
	if !m.MainThread {
		m.IFF = true
	}
	return nil
}

// bootInterrupt delivers an interrupt in the middle of a BIOS call, which is
// where the real machine delivers them during a cartridge's INIT: the BIOS's
// own EI lets the pending vblank in.
//
// It nests: the translated ISR runs to its ret inside the shim that let it
// happen, on the same Z80 stack, exactly as the hardware nests it. How many
// arrive is cycle timing and this machine has none -- but the games this
// exists for guard their init against the ISR's slow path and leave only
// drain-until-done housekeeping to run early, so "at every opportunity"
// converges to the same state as "the ten the cycles allowed".
func (m *M) bootInterrupt() {
	if !m.booting || m.inISR {
		return
	}

	entry, ok := m.InterruptEntry()
	if !ok {
		// No cartridge hook yet -- but the interrupt still happens.
		// The BIOS has a handler of its own, and it runs whether a
		// cartridge has hooked it or not: it reads the status
		// register, which clears the vertical-blank flag, and gets on
		// with the keyboard and the clock.
		//
		// Leaving it pending instead saves it up for the exact moment
		// a hook appears -- and a hook appears the instant a cartridge
		// writes the jump byte, one instruction before it writes the
		// address. King's Valley II wrote C3h to the hook, and the
		// vblank that had been waiting through the whole of its INIT
		// went straight into a jump whose destination was still the
		// hook table's filler.
		m.VDP.AckVblank()
		m.lastDeliver = m.Cyc
		return
	}
	// A runaway guard, far above what any real INIT sees. An INIT that
	// asks for thousands of interrupts is looping on something else.
	if m.bootIRQs >= 256 {
		return
	}
	m.bootIRQs++
	m.lastDeliver = m.Cyc
	// A delivery is a vblank, and the ISR is entitled to see its flag.
	m.VDP.StartFrame()
	// Through deliver, which saves everything, builds the stack frame the
	// real BIOS builds, and puts the machine back afterward. A hook that
	// unwinds that frame itself needs it during INIT exactly as much as it
	// does later. See deliver.
	m.inISR = true
	m.deliver(entry)
	m.inISR = false
}

// restoreFrom puts back everything the BIOS saves around an interrupt.
func (m *M) restoreFrom(s *M) {
	m.A, m.B, m.C, m.D, m.E, m.H, m.L = s.A, s.B, s.C, s.D, s.E, s.H, s.L
	m.Fs, m.Fz, m.Fh, m.Fp, m.Fn, m.Fc = s.Fs, s.Fz, s.Fh, s.Fp, s.Fn, s.Fc
	m.A2, m.B2, m.C2, m.D2, m.E2, m.H2, m.L2 = s.A2, s.B2, s.C2, s.D2,
		s.E2, s.H2, s.L2
	m.Fs2, m.Fz2, m.Fh2, m.Fp2, m.Fn2, m.Fc2 = s.Fs2, s.Fz2, s.Fh2, s.Fp2,
		s.Fn2, s.Fc2
	m.IX, m.IY, m.PC, m.SP = s.IX, s.IY, s.PC, s.SP
	m.idle, m.halted = s.idle, s.halted
}

// Frame delivers one VDP interrupt: exactly one game frame.
func (m *M) Frame() error {
	if m.MainThread {
		return m.mainThreadFrame()
	}
	entry, ok := m.InterruptEntry()
	if !ok {
		return fmt.Errorf("z80: no interrupt hook installed at H.KEYI " +
			"or H.TIMI; the cartridge has nothing to run per frame")
	}
	m.frames++
	m.VDP.StartFrame()
	return m.runFrame(entry)
}

// mainThreadFrame is a frame of the other shape of cartridge: the game loop
// is INIT, still running, and a frame is the hardware's -- raise the vertical
// blank, deliver the interrupt, then let the main thread run for one frame's
// worth of cycles. The interrupts inside it come from the clock: the line
// interrupt at register 19's raster line, the next vblank for anything that
// polls across the boundary. The main thread itself runs interpreted -- its
// position is a PC, which translated code cannot resume from -- while the
// delivered handler runs translated where a translation exists.
func (m *M) mainThreadFrame() error {
	m.frames++
	frameStart := m.Cyc
	m.frameOrigin = frameStart
	m.VDP.StartLog()
	m.VDP.StartFrame()
	m.fDue = frameStart + m.FrameCycles()
	// The handler runs outside the frame's budget, and the budget is the
	// main thread's alone.
	//
	// Left as it was -- the previous frame's limit still in force while
	// the handler ran -- a main thread that had used its whole budget,
	// which a loading phase does, had already reached it, so the
	// interpreter returned at the top of its loop without executing an
	// instruction of the handler: the interrupt was delivered and did
	// nothing. Space Manbow lost a third of its handlers that way.
	//
	// Setting the *new* frame's limit before delivering instead is worse:
	// a handler that outlasts the frame is then cut off partway, which
	// leaves the machine in the middle of an interrupt and the game
	// showing nothing at all. Neither limit belongs to the handler.
	m.cycLimit = 0
	m.armLine(frameStart)
	if entry, ok := m.InterruptEntry(); ok {
		if m.IFF && m.nest < maxNest {
			if m.IRQTrace != nil {
				m.IRQTrace("vblank delivered", 0)
			}
			m.deliverCatching(entry)
		} else {
			if m.IRQTrace != nil {
				m.IRQTrace("vblank held (IFF off)", 0)
			}
			// The main thread is inside `di` -- a block copy, a
			// bank switch -- as the frame turns. The hardware
			// holds INT asserted while the vertical-blank flag
			// stands and the processor takes it at the next `ei`,
			// so the frame is late, not lost. Dropping it here
			// left the frame with no handler at all: no split, no
			// scroll, the whole screen drawn in the status
			// panel's state. See dueIRQ.
			m.fHeld = true
		}
	}
	m.lastIRQ = frameStart + m.FrameCycles()
	m.cycLimit = frameStart + m.FrameCycles()
	m.idle, m.halted = false, false
	for {
		m.Interpret(0, mainThreadQuota)
		if !m.halted || m.idle || m.Cyc >= m.cycLimit {
			break
		}
		// Halted, with frame budget left. The processor has stopped
		// fetching; the clock has not stopped, the raster keeps
		// scanning, and the interrupt it is waiting for is still
		// coming. Let time pass in the four-cycle steps a halt
		// actually costs, so the split due later in this frame
		// arrives -- taking it un-halts the processor and the loop
		// resumes at the instruction after the halt.
		//
		// Ending the frame here instead made a halt-driven main loop
		// run frames a fifth of their length: the FRS Space Manbow
		// lost one split in three and drew every third frame of the
		// playfield in the status panel's page and scroll.
		for m.halted && m.Cyc < m.cycLimit {
			m.tick(cycHalt)
		}
	}
	m.cycLimit = 0
	return nil
}

// mainThreadQuota is a step backstop for a main-thread frame; the cycle limit
// is the real bound, and a frame that hits this instead is spinning on
// something that is never coming.
const mainThreadQuota = 2_000_000

// runFrame delivers one interrupt, or lets the cartridge's own re-entry guard
// eat it.
//
// A handler that costs more than a frame is not unusual and is not a bug: on
// the hardware the next interrupt arrives while it is still running, and a
// handler that guards against re-entry -- Salamander's guard is at E205h --
// returns from it at once. The game's logic advances once per two or three
// frames and it was tuned that way. A machine that finishes every handler
// instantly never overruns and so runs those games at the full frame rate:
// Salamander's gameplay comes out three times too fast.
//
// So the frame's cycles are a budget. Spend more than one frame has and the
// next frames go to paying it off, which is what the guard does on the
// hardware. Idle time cannot be banked -- the credit is capped at a frame --
// because a real machine gets no faster for having been idle.
func (m *M) runFrame(entry uint16) error {
	if !m.Timed() {
		m.run(entry)
		return nil
	}
	// An interrupt this frame may already have been taken, mid-flight,
	// inside a handler that overran the frame before it. See dueIRQ.
	if m.irqTaken > 0 {
		m.irqTaken--
		return nil
	}
	m.lastIRQ = m.Cyc
	m.frameStart = m.Cyc
	// The frame's line interrupt comes due on the clock partway through,
	// so a handler that polls for it under `di` sees it rise. See armLine.
	m.armLine(m.Cyc)
	m.run(entry)
	m.frameStart = 0
	return nil
}

// InterruptEntry is where a frame's work begins: the hook the cartridge
// installed, as a `jp nnnn` at H.KEYI or H.TIMI.
func (m *M) InterruptEntry() (uint16, bool) {
	for _, h := range [...]uint16{hKeyI, hTimI} {
		if m.Mem[h] == 0xC3 {
			return m.rd16(h + 1), true
		}
	}
	return 0, false
}

// InterruptEntries is every hook a cartridge has installed, in the order the
// BIOS calls them: H.KEYI at the very start of its interrupt handler, H.TIMI
// after the keyboard scan and the clock.
//
// A cartridge may install both, and one that does expects both to run --
// Space Manbow puts different work in each and the reference machine enters
// them three hundred times apiece over three hundred frames. Calling only the
// first one found gave its loading phase two thirds of the work it was
// written for, and a phase that takes half again as long as it should is a
// game that misses the keypress meant to start it.
func (m *M) InterruptEntries() []uint16 {
	var out []uint16
	for _, h := range [...]uint16{hKeyI, hTimI} {
		if m.Mem[h] == 0xC3 {
			out = append(out, m.rd16(h+1))
		}
	}
	return out
}

// Idle is what a self-jump compiles to.
//
// `jr $` on the hardware spins until an interrupt takes the processor
// somewhere else; here there is nothing to spin for, so the translated code
// returns out of Run and the caller delivers the interrupt itself. The stack
// is deliberately left alone: code that reaches an idle loop has abandoned
// whatever was on it -- a game's INIT loads SP with its own value before
// looping -- so unwinding to the sentinel would be undoing something the
// cartridge meant.
func (m *M) Idle() { m.idle = true }

// Stopped reports whether the processor has nothing left to run: idle in
// its self-jump, or halted waiting for an interrupt. ResumeFromHalt clears
// the halt the way a delivered interrupt does, for a driver -- the
// explorer -- that stands in for one.
func (m *M) Stopped() (idle, halted bool) { return m.idle, m.halted }

// ResumeFromHalt continues a halted processor at the instruction after the
// halt. See Stopped.
func (m *M) ResumeFromHalt() { m.halted = false }

// Idling reports whether the last Run ended at a self-jump rather than a ret.
func (m *M) Idling() bool { return m.idle }

// Frames counts calls to Frame.
func (m *M) Frames() int { return m.frames }

// SetKeyRow sets one row of the keyboard matrix as SNSMAT reports it: a 0 bit
// means pressed.
func (m *M) SetKeyRow(row int, bits byte) {
	if row >= 0 && row < len(m.Keys) {
		m.Keys[row] = bits
	}
}

// BootIRQs is how many interrupts were delivered during INIT. The real
// machine's count is set by cycle timing, which this machine does not have, so
// the number is worth checking against a reference before trusting a game that
// counts them.
func (m *M) BootIRQs() int { return m.bootIRQs }
