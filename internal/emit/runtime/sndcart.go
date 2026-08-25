package z80

import "fmt"

// A Konami sound cartridge in a slot of its own.
//
// The SCC on a Konami game cartridge is part of that cartridge's mapper:
// bank 3Fh in page two turns a window of ROM into the chip's registers, so a
// game that has one is the game that carries it, and scc.go reaches the chip
// through setBank for exactly that reason. A disk game carries nothing. The
// sound such a game was written for arrives the other way -- as a cartridge
// that is nothing *but* the chip, in a slot the program goes looking for.
// Snatcher shipped with one, the RA-004, and its options screen reports which
// slot it found rather than offering a choice.
//
// A program reaches it two ways, and both are modelled. It can select the
// slot in page two and read and write it directly, which is what the slot
// register does; or it can leave the paging alone and go through RDSLT and
// WRSLT, which name the slot per access. Snatcher's scan uses the second --
// measured on the reference machine, which walks page two through slots
// zero, one and two at around five seconds and then leaves it on RAM.
//
// Only page two is modelled. The real cartridge answers over 4000h-BFFFh,
// but everything that makes a sound is in the upper half -- the bank
// register at 9000h, the SCC's registers at 9800h, the SCC-I's at B800h and
// its mode register at BFFEh -- and page one on a disk machine is already
// spoken for by the disk ROM. See slotHas.
type SoundCart struct {
	// Slot is the primary slot the cartridge answers in. Slot two is the
	// one this machine's slot fiction leaves empty, and it is the slot
	// openMSX puts the cartridge in, so it is the default.
	Slot byte
	// Plus makes it an SCC-I -- five independent waveforms and a second
	// register window -- rather than a plain SCC.
	Plus bool

	// bank is what the two 8K bank registers of the page-two window hold.
	// Only their effect matters: 3Fh in the lower one shows the SCC's
	// registers, 80h in the upper one shows the SCC-I's.
	bank [2]byte
	// sccRegs and plusRegs are the two register windows as they read
	// back. They are the chip's own memory, not the window's: taking a
	// bank away hides them, it does not wipe them, and a program checks
	// exactly that -- Snatcher writes a byte, banks away, writes again,
	// banks back and expects the first byte still to be there.
	sccRegs  [sccEnd - sccBase]byte
	plusRegs [sccPlusEnd - sccPlusBase]byte
	// mode is the SCC-I's mode register, which lives at the top of the
	// cartridge rather than in either window and answers whatever bank
	// is selected.
	mode byte
	// paged says the slot register is showing the cartridge in page two,
	// in which case win is also what m.Mem holds there.
	paged bool
	// under is the RAM the cartridge is standing in front of while it is.
	// One flat memory means those bytes have to live somewhere, the same
	// bargain setRAMSegment makes for a mapper segment.
	under [0x4000]byte
}

// The bank registers of the page-two window, and the window itself.
const (
	sndPage2Lo = 0x8000
	sndPage2Hi = 0xC000
	// Writing ROM is how a Konami mapper is switched; these are the two
	// addresses that do it for the 8000h-BFFFh half.
	sndBankLo = 0x9000
	sndBankHi = 0xB000
	// An empty cartridge address reads as FFh, which is what the bus
	// settles to when nothing is driving it.
	sndEmpty = 0xFF
)

// SoundCartIn puts a sound cartridge in a slot, and is how a game with no
// cartridge of its own gets the chip it was written for. Passing plus makes
// it an SCC-I.
// Only slot two is free: zero is the BIOS, one is the cartridge -- or, on a
// disk machine, the disk ROM the game finds DSKIO through -- and three is
// RAM. Putting the chip in any of those takes away something the machine
// needs, so it is refused rather than silently breaking the boot.
func (m *M) SoundCartIn(slot byte, plus bool) error {
	if slot&3 != sndFreeSlot {
		return fmt.Errorf("a sound cartridge can only go in slot %d here: "+
			"slot %d already holds %s", sndFreeSlot, slot&3,
			slotHolder(slot&3))
	}
	m.SndCart = &SoundCart{Slot: slot & 3, Plus: plus}
	m.SCC.Plus = plus
	m.sndRepage()
	return nil
}

// sndFreeSlot is the one slot this machine leaves empty, and the one openMSX
// puts a sound cartridge in.
const sndFreeSlot = 2

// slotHolder names what a slot already has in it, for the refusal above.
func slotHolder(sl byte) string {
	switch sl {
	case slotBIOS:
		return "the BIOS"
	case slotCart:
		return "the cartridge, or the disk ROM on a disk machine"
	case slotRAM:
		return "RAM"
	}
	return "nothing"
}

// at is what the cartridge shows at one address: a register window where a
// bank register has opened one, and otherwise a cartridge with no ROM in it.
func (c *SoundCart) at(a uint16) byte {
	switch {
	case a >= sccBase && a < sccEnd && c.sccWindowLive():
		return c.sccRegs[a-sccBase]
	case a >= sccPlusBase && a < sccPlusEnd && c.plusWindowLive():
		return c.plusRegs[a-sccPlusBase]
	}
	return sndEmpty
}

// sndSelected reports whether the slot register is showing the sound
// cartridge in page two.
func (m *M) sndSelected() bool {
	c := m.SndCart
	return c != nil && (m.PrimarySlot>>4)&3 == c.Slot
}

// sndRepage brings the cartridge into page two or takes it away again,
// swapping the RAM underneath it out of the way and back.
//
// Reads are one indexed load into m.Mem and must stay that way -- see
// read_plain.go -- so what the cartridge shows has to be *put* in the
// address space rather than answered for on demand. That is the same reason
// the SCC's registers are mirrored rather than indirected.
func (m *M) sndRepage() {
	c := m.SndCart
	if c == nil {
		return
	}
	want := m.sndSelected()
	if want == c.paged {
		return
	}
	c.paged = want
	if want {
		copy(c.under[:], m.Mem[sndPage2Lo:sndPage2Hi])
		m.sndShow()
		return
	}
	copy(m.Mem[sndPage2Lo:sndPage2Hi], c.under[:])
}

// sndShow puts the cartridge's whole page-two view into the address space,
// which is where a plain read will find it.
func (m *M) sndShow() {
	c := m.SndCart
	if c == nil || !c.paged {
		return
	}
	for a := sndPage2Lo; a < sndPage2Hi; a++ {
		m.Mem[a] = c.at(uint16(a))
	}
}

// sccWindowLive and plusWindowLive say which set of registers the bank
// registers have chosen. Only one of them is ever a window; the other is
// cartridge with nothing behind it.
func (c *SoundCart) sccWindowLive() bool  { return c.bank[0] == sccBank }
func (c *SoundCart) plusWindowLive() bool { return c.Plus && c.bank[1] == sccPlusBank }

// sndCartRead is what the cartridge shows at an address in page two,
// whether it is paged in or being read through RDSLT.
func (c *SoundCart) sndCartRead(a uint16) byte {
	if a < sndPage2Lo || a >= sndPage2Hi {
		return sndEmpty
	}
	return c.at(a)
}

// sndCartWrite is a write to the cartridge at an address in page two,
// whether it is paged in or being written through WRSLT.
func (m *M) sndCartWrite(a uint16, v byte) {
	c := m.SndCart
	if c == nil || a < sndPage2Lo || a >= sndPage2Hi {
		return
	}
	switch {
	// The mode register is part of the cartridge rather than of either
	// window, so it answers whatever bank is selected -- which is how a
	// program switches an SCC-I over in the first place, before it has
	// any window to write through.
	case c.Plus && a >= sccModeReg:
		c.mode = v
		m.SCC.PlusMode = v&sccModePlus != 0

	case a >= sccBase && a < sccEnd && c.sccWindowLive():
		m.SCC.Write(a, v)
		c.sccRegs[a-sccBase] = v

	case a >= sccPlusBase && a < sccPlusEnd && c.plusWindowLive():
		m.SCC.WritePlus(a, v)
		c.plusRegs[a-sccPlusBase] = v

	// A bank register is selected by writing to ROM, so these come after
	// the windows: while a window is showing, it is the window that
	// answers, and only ROM is a bank register.
	case a >= sndBankLo && a < sndBankLo+0x800:
		c.bank[0] = v
	case a >= sndBankHi && a < sndBankHi+0x800:
		c.bank[1] = v

		// Anything else is a cartridge address with nothing behind it,
		// and a write there goes nowhere, as a write to ROM does.
	}
	m.sndShow()
}
