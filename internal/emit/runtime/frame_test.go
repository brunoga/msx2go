package z80

import "testing"

// The frame after an overrunning one still gets one interrupt, not a burst.
//
// A handler that costs more than a frame is ordinary -- the next interrupt
// arrives while it is still running -- so a frame routinely begins with the
// clock already well past the mark the last one left. mainThreadFrame set
// that mark *after* the handler returned, so throughout the handler's run
// lastIRQ still held the previous frame's value, by then a full budget in the
// past. The handler's own `ei` was all dueIRQ needed to deliver a second
// interrupt on top of the first, and another every budget after that, each
// nested inside the last. Snatcher's opening ran its handler four to six
// times a frame, three deep, and played at four times its speed.
//
// runFrame, the frame engine for the other shape of cartridge, has always set
// the mark before running the handler. This is the same rule for the
// main-thread shape.
//
// The handler counts its own entries in the machine's memory rather than
// through a Go hook, so what is counted is the machine entering it.
func TestAFrameAfterAnOverrunGetsOneInterrupt(t *testing.T) {
	const (
		handler = 0x9000
		mainLop = 0x8000
		counter = 0xA000
	)
	m := &M{MainThread: true, Hz: 60}
	m.ClearKeys()
	m.IFF = true
	m.SP = 0xF380
	m.PC = mainLop

	// H.KEYI, as a cartridge leaves it: a jump to the handler.
	m.Mem[hKeyI] = 0xC3
	m.Mem[hKeyI+1], m.Mem[hKeyI+2] = byte(handler&0xFF), byte(handler>>8)

	// The handler: enable interrupts, count the entry, do a little work,
	// return. The work is well under a frame, so one interrupt is the only
	// right answer however the clock stands when it starts.
	copy(m.Mem[handler:], []byte{
		0xFB,             // ei
		0x21, 0x00, 0xA0, // ld hl,A000h
		0x34,       // inc (hl)
		0x06, 0x00, // ld b,0
		0x10, 0xFE, // djnz $        -- about 3300 cycles
		0xC9, // ret
	})
	// The main thread: a loop that burns cycles and never finishes, which
	// is the shape this frame engine exists for.
	copy(m.Mem[mainLop:], []byte{
		0x00,       // nop
		0x18, 0xFD, // jr 8000h
	})

	// Where an overrunning handler leaves the clock: several frames past
	// the mark the last frame set.
	m.lastIRQ = 0
	m.Cyc = 5 * m.FrameCycles()

	if err := m.Frame(); err != nil {
		t.Fatal(err)
	}
	if got := m.Mem[counter]; got != 1 {
		t.Errorf("the handler was entered %d times in one frame, want 1 -- "+
			"the clock still owed an interrupt when it started", got)
	}
}
