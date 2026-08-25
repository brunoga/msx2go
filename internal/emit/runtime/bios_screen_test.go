package z80

import "testing"

// What a bitmap screen leaves in the registers the BIOS owns. Each of these
// was read off the reference machine going into SCREEN 7 from the 80-column
// console, and each was wrong here in a way that did not show until a whole
// screen was compared byte for byte.
func TestABitmapScreenLeavesTheRegistersTheBIOSLeaves(t *testing.T) {
	m := &M{}
	m.VDP.Reset()
	m.VDP.VRAM = make([]byte, 0x20000)
	m.VDP.V9938 = true
	// The console before it: a colour table, a border, and 192 lines.
	m.VDP.WriteReg(3, 0x27)
	m.VDP.WriteReg(4, 0x02)
	m.VDP.WriteReg(9, 0x02) // the frequency bit alone
	m.Mem[bdrClr] = 0x04

	m.setScreenBitmap(7)

	// Registers 3 and 4 are a colour table, which a bitmap screen has
	// not got. The real BIOS leaves them where the last screen put them.
	if got := m.VDP.Reg[3]; got != 0x27 {
		t.Errorf("R3 = %02X, want 27 -- a bitmap screen does not own it", got)
	}
	if got := m.VDP.Reg[4]; got != 0x02 {
		t.Errorf("R4 = %02X, want 02 -- a bitmap screen does not own it", got)
	}
	// Register 7 is the border colour here, not a foreground pair.
	if got := m.VDP.Reg[7]; got != 0x04 {
		t.Errorf("R7 = %02X, want 04 -- the border, from BDRCLR", got)
	}
	// And a bitmap screen is 212 lines, with the machine's own frequency
	// bit left alone beside it.
	if got := m.VDP.Reg[9]; got != 0x82 {
		t.Errorf("R9 = %02X, want 82 -- 212 lines, keeping the "+
			"frequency bit", got)
	}
}

// Going back to text gives the 192 lines back, but only on a chip that has a
// register 9 to say so. Writing one is what makes this machine a V9938, so a
// TMS9918 cartridge calling SCREEN 1 must not be promoted by its own call.
func TestATextScreenGivesBackThe212Lines(t *testing.T) {
	m := &M{}
	m.VDP.Reset()
	m.VDP.VRAM = make([]byte, 0x20000)
	m.VDP.V9938 = true
	m.VDP.WriteReg(9, 0x82)
	m.setScreen(1)
	if got := m.VDP.Reg[9]; got != 0x02 {
		t.Errorf("R9 = %02X, want 02 -- a text screen is 192 lines", got)
	}

	msx1 := &M{}
	msx1.VDP.Reset()
	msx1.VDP.VRAM = make([]byte, 0x4000)
	msx1.setScreen(1)
	if msx1.VDP.V9938 {
		t.Error("an MSX1 cartridge asking for SCREEN 1 turned its " +
			"TMS9918 into a V9938")
	}
}
