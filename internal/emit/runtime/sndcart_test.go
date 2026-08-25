package z80

import "testing"

// The probe a program uses to find the chip, as Snatcher's does it: write a
// byte through a register window, bank the window away, write again where it
// used to be, bank it back, and read. The byte has to still be there. The
// registers are the chip's own memory, and banking hides them rather than
// wiping them -- a machine that clears the window on the way out answers
// this test with "no cartridge here", which is what it did before.
func TestBankingAwayHidesTheRegistersRatherThanWipingThem(t *testing.T) {
	for _, tc := range []struct {
		name       string
		plus       bool
		bankAt     uint16 // the bank register that opens the window
		bankOpen   byte
		windowAt   uint16 // the first byte of the window itself
		modeNeeded bool
	}{
		{"SCC", false, 0x9000, sccBank, sccBase, false},
		{"SCC+", true, 0xB000, sccPlusBank, sccPlusBase, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &M{}
			if err := m.SoundCartIn(2, tc.plus); err != nil {
				t.Fatal(err)
			}
			// An SCC-I comes up answering as a plain SCC; the mode
			// register switches it over, and it is not in either
			// window -- it answers whatever bank is selected.
			if tc.modeNeeded {
				m.sndCartWrite(sccModeReg+1, sccModePlus)
				if !m.SCC.PlusMode {
					t.Fatal("the mode register did not take, " +
						"so the SCC-I never switched over")
				}
			}
			m.sndCartWrite(tc.bankAt, tc.bankOpen)
			m.sndCartWrite(tc.windowAt, 0xAC)
			if got := m.SndCart.sndCartRead(tc.windowAt); got != 0xAC {
				t.Fatalf("through the open window: %02X, want AC", got)
			}

			// Bank it away. The window is gone, and a write there
			// goes nowhere.
			m.sndCartWrite(tc.bankAt, 0x00)
			if got := m.SndCart.sndCartRead(tc.windowAt); got != sndEmpty {
				t.Errorf("with the window banked away: %02X, want FF", got)
			}
			m.sndCartWrite(tc.windowAt, 0x00)

			// Bank it back. The first byte is still there.
			m.sndCartWrite(tc.bankAt, tc.bankOpen)
			if got := m.SndCart.sndCartRead(tc.windowAt); got != 0xAC {
				t.Errorf("after banking back: %02X, want AC -- "+
					"the registers were wiped rather than hidden", got)
			}
		})
	}
}

// The fifth channel is the whole reason the SCC-I exists: it gets a waveform
// of its own instead of sharing the fourth's.
func TestTheFifthChannelSharesUntilTheSCCIIsSwitchedOver(t *testing.T) {
	var s SCC
	s.Wave[3][0], s.Wave[4][0] = 11, 22

	if got := s.waveOf(4)[0]; got != 11 {
		t.Errorf("a plain SCC: channel five plays %d, want 11 -- "+
			"the fourth channel's table", got)
	}
	s.Plus = true
	if got := s.waveOf(4)[0]; got != 11 {
		t.Errorf("an SCC-I still in SCC mode: channel five plays %d, "+
			"want 11 -- software written before it expects sharing", got)
	}
	s.PlusMode = true
	if got := s.waveOf(4)[0]; got != 22 {
		t.Errorf("an SCC-I switched over: channel five plays %d, want 22 -- "+
			"its own table", got)
	}
}

// Only slot two is free. Putting the chip anywhere else takes away something
// the machine needs, and on a disk machine slot one is where the game finds
// DSKIO -- which does not fail loudly, it just stops booting.
func TestASoundCartridgeOnlyFitsTheFreeSlot(t *testing.T) {
	for _, sl := range []byte{0, 1, 3} {
		m := &M{}
		if err := m.SoundCartIn(sl, false); err == nil {
			t.Errorf("slot %d was accepted; it already holds %s",
				sl, slotHolder(sl))
		}
	}
	m := &M{}
	if err := m.SoundCartIn(sndFreeSlot, false); err != nil {
		t.Errorf("slot %d was refused: %v", sndFreeSlot, err)
	}
}
