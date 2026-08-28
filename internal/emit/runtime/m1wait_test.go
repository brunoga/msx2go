package z80

import "testing"

// An MSX inserts one wait state into every opcode fetch, so an instruction
// costs its data-sheet time plus one per byte of prefix-and-opcode. These are
// the shapes that has to hold for.
func TestEveryOpcodeFetchCostsAWaitState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		op, sub byte
		dataSht uint32
		fetches uint32
	}{
		{"nop", 0x00, 0, 4, 1},
		{"ld a,(hl)", 0x7E, 0, 7, 1},
		{"call nn, untaken", 0xC4, 0, 10, 1},
		{"jp nn", 0xC3, 0, 10, 1},
		{"set 3,h", 0xCB, 0xDC, 8, 2},
		{"bit 0,(hl)", 0xCB, 0x46, 12, 2},
		{"res 0,(hl)", 0xCB, 0x86, 15, 2},
		{"otir, one pass", 0xED, 0xB3, 16, 2},
		{"in a,(c)", 0xED, 0x78, 12, 2},
		{"ld (nn),bc", 0xED, 0x43, 20, 2},
		{"inc (ix+d)", 0xDD, 0x34, 23, 2},
		{"set 0,(ix+d)", 0xDD, 0xCB, 23, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.dataSht + tc.fetches*cycM1Wait
			if got := CycleCost(tc.op, tc.sub); got != want {
				t.Errorf("CycleCost = %d, want %d (%d on the data sheet, "+
					"%d fetch(es))", got, want, tc.dataSht, tc.fetches)
			}
		})
	}
}

// The loop this was measured on: Snatcher's hottest, at 5798h. One iteration
// costs 90.85 cycles on the reference machine, where the data sheet says 76
// and the twelve M1 cycles bring it to 88; the rest is the conditional call
// being taken on some passes.
//
// This machine charges 83, not 88, and the five are the `jr` back: a taken
// conditional costs more than an untaken one and neither charge point knows
// which it will be, so both charge the untaken cost on purpose -- see the note
// above cycBase. What is under test is that the twelve M1 waits are there.
func TestTheMeasuredLoopCostsWhatTheModelSays(t *testing.T) {
	loop := []struct {
		op, sub byte
		m1      uint32
	}{
		{0x7E, 0, 1},    // ld a,(hl)
		{0xCB, 0xDC, 2}, // set 3,h
		{0xB6, 0, 1},    // or (hl)
		{0xC4, 0, 1},    // call nz,57ED
		{0xCB, 0x9C, 2}, // res 3,h
		{0x23, 0, 1},    // inc hl
		{0x7D, 0, 1},    // ld a,l
		{0xE6, 0, 1},    // and 1Fh
		{0xFE, 0, 1},    // cp 1Fh
		{0x38, 0, 1},    // jr c,5798
	}
	var total, waits uint32
	for _, in := range loop {
		total += CycleCost(in.op, in.sub)
		waits += in.m1 * cycM1Wait
	}
	if waits != 12 {
		t.Fatalf("the loop has %d M1 waits, want 12", waits)
	}
	// 76 on the data sheet with the jr untaken is 71; plus twelve waits.
	const want = 71 + 12
	if total != want {
		t.Errorf("one iteration costs %d, want %d -- the reference machine "+
			"measures 90.85, of which 5 is the taken jr this deliberately "+
			"does not charge and about 3 is the call taken on some passes",
			total, want)
	}
}
