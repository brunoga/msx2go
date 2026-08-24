package z80

import "fmt"

// The MSX BIOS entry points a cartridge may call.
//
// They are shimmed rather than executed, because the BIOS is not part of the
// image being translated and is not ours to ship -- but their behaviour is
// part of the game's behaviour, so each one mirrors the documented register
// contract exactly, flags included where a caller depends on them.
//
// The set is open, and that is the honest position: a cartridge that calls one
// that is not here gets a panic naming the address, and msx2go reports the
// whole list at generation time so it is known before anything is built rather
// than found three levels in.
// biosEnablesInterrupts is the set of entry points the real BIOS documents
// as returning with interrupts enabled -- each contains an EI of its own.
//
// This is not trivia. A cartridge's INIT runs under DI of its own choosing,
// but the moment it asks the BIOS to touch the VDP or the PSG, interrupts
// come back on inside that call, and on real hardware the first frames of
// the game's ISR run in the middle of its own INIT. Games are built for it:
// Salamander raises a guard byte around its slow path precisely so those
// early interrupts do only the per-frame housekeeping. An INIT run atomically
// leaves that housekeeping undone, which is a state the real machine can
// never be in.
var biosEnablesInterrupts = map[uint16]bool{
	0x0041: true, 0x0044: true, 0x0047: true, 0x004A: true, 0x004D: true,
	0x0050: true, 0x0053: true, 0x0056: true, 0x0059: true, 0x005C: true,
	0x005F: true, 0x0090: true, 0x0093: true, 0x0096: true, 0x00C0: true,
	0x00D1: true, 0x00D5: true, 0x013E: true, 0x0141: true,
}

func (m *M) bios(addr uint16) {
	if m.Trace != nil {
		m.Trace(biosName(addr), uint16(m.A), m.HL())
	}
	if m.BiosTrace != nil {
		m.BiosTrace(addr)
	}
	m.tick(uint32(biosCost(addr)))
	// The block routines are loops over bytes, and their cost is the whole
	// reason a cartridge's handler overruns a frame. A shim that returns
	// instantly is not merely fast, it is a machine on which the game was
	// never tuned. See cycles.go.
	switch addr {
	case 0x0056, 0x0059, 0x005C:
		m.tick(uint32(m.BC()) * cycVRAMByte)
	}
	if biosEnablesInterrupts[addr] {
		m.IFF = true
		m.bootInterrupt()
	}
	switch addr {

	case 0x0047: // WRTVDP  B = value, C = register
		// Through WriteReg, not straight into the array: the register
		// number is six bits on a V9938, and masking it to three sends
		// register 9 -- which is where Space Manbow asks for a 212-line
		// screen -- into register 1, where it means something else
		// entirely.
		m.VDP.WriteReg(m.C&0x3F, m.B)
		m.saveReg(m.C&0x3F, m.B)

	// The address masks below are the size of the video memory the
	// machine has, not a hard-coded 3FFFh: fourteen bits is the MSX1
	// truth, and on an MSX1 the mask below *is* 3FFFh because the memory
	// is 16K. An MSX2 addresses 64K through these same entries, and
	// masking to 16K folded every write above 3FFFh back into the first
	// page -- Breaker's title program copies its compressed picture to
	// 8000h with LDIRVM, and ours put it in page 0, over the picture it
	// had already drawn there, while the decompressor read the zeroes
	// where the data should have been.
	case 0x004A: // RDVRM   HL = address -> A
		m.A = m.VDP.VRAM[m.VDP.phys(m.vramBase()+int(m.HL()))]

	case 0x004D: // WRTVRM  HL = address, A = value
		m.VDP.VRAM[m.VDP.phys(m.vramBase()+int(m.HL()))] = m.A

	case 0x0050: // SETRD   HL = address, set up for reading
		m.VDP.SetAddr(m.HL(), false)

	case 0x0053: // SETWRT  HL = address, set up for writing
		m.VDP.SetAddr(m.HL(), true)

	case 0x0093: // WRTPSG  A = register, E = value
		m.PSG.Write(m.A, m.E)

	case 0x0096: // RDPSG   A = register -> A
		m.A = m.PSG.Read(m.A)

	case 0x013E: // RDVDP   read status register -> A
		m.A = m.VDP.ReadStatus()

	case 0x0141: // SNSMAT  A = row -> A = matrix bits (0 = pressed)
		row := m.A & 0x0F
		if int(row) < len(m.Keys) {
			m.A = m.Keys[row]
		} else {
			m.A = 0xFF
		}

	// The slot registers. There is one flat address space here, with the
	// cartridge already paged in, so the slots are a fiction -- but it is
	// a fiction cartridges ask about, usually to find out which slot they
	// are in so they can page themselves back. Answering as an ordinary
	// machine -- BIOS in slot 0, this cartridge in slot 1, RAM in slot 3,
	// nothing expanded -- keeps that arithmetic working out to something
	// consistent, and the paging it then performs is a no-op because
	// everything is already where it needs to be.
	case 0x0138: // RSLREG  read the primary slot register -> A
		m.A = m.PrimarySlot

	case 0x013B: // WSLREG  A -> the primary slot register
		m.PrimarySlot = m.A

	case 0x000C: // RDSLT   A = slot, HL = address -> A
		m.A = m.slotRead(m.A, m.HL())

	case 0x0014: // WRSLT   A = slot, HL = address, E = value
		if m.slotHas(m.A, m.HL()) {
			m.wr(m.HL(), m.E)
		}

	case 0x0024: // ENASLT  A = slot, H = page: the pages are mapped
		// already, but the register has to agree, because isBIOS
		// reads it to tell page zero's BIOS from page zero's RAM.
		page := uint(m.H >> 6)
		m.PrimarySlot = m.PrimarySlot&^(3<<(2*page)) |
			(m.A&3)<<(2*page)
	case 0x0041: // DISSCR  blank the display
		m.VDP.Reg[1] &^= 0x40
	case 0x0044: // ENASCR
		m.VDP.Reg[1] |= 0x40

	// The four VDP block routines, in the order the BIOS puts them, and
	// leaving behind what the BIOS leaves behind.
	//
	// Getting the order wrong is not a subtle bug and it is a quiet one:
	// King's Valley calls none of them, so nothing here noticed until
	// Salamander asked 0056h to clear all 16K of video RAM, got a copy
	// from address zero instead, and drew its title screen over whatever
	// the last screen had left behind.
	//
	// What they leave in the registers matters just as much, and is not
	// guessable -- it was read out of C-BIOS at 026Dh, 0281h and 0297h.
	// LDIRVM in particular ends with `ex de,hl`, so it returns with HL
	// still the *destination* it was given and DE past the source. That
	// is not decoration: Salamander's font uploader at 4CC0h calls it
	// three times, once per Graphics 2 third, and steps to the next third
	// with `add hl,0800h` on the way round. Advance HL the way an LDIR
	// would and the second pass writes the font over the colour table at
	// C261h instead, which is 0261h once the VDP has masked it to
	// fourteen bits -- and every glyph on the title screen comes out
	// wrong.
	//
	// All three end their loops with B counted down to zero and C left
	// holding the data port they were writing to.

	case 0x0056: // FILVRM  HL = address, BC = count, A = value
		for i := uint16(0); i < m.BC(); i++ {
			m.VDP.VRAM[m.VDP.phys(m.vramBase()+int(m.HL())+int(i))] = m.A
		}
		// HL is only used to set the write address, and A is pushed and
		// popped around the loop. Both survive; the counters do not.
		m.setBC(0)

	case 0x0059: // LDIRMV  HL = VRAM, DE = destination, BC = count
		n := m.BC()
		for i := uint16(0); i < n; i++ {
			m.wr(m.DE()+i, m.VDP.VRAM[m.VDP.phys(m.vramBase()+int(m.HL())+int(i))])
		}
		// `push hl` ... `pop hl` around the loop; DE ends past the block.
		m.setDE(m.DE() + n)
		m.B, m.C, m.A = 0, VDPDataPort, 0

	case 0x005C: // LDIRVM  HL = source, DE = VRAM, BC = count
		n := m.BC()
		src, dst := m.HL(), m.DE()
		for i := uint16(0); i < n; i++ {
			m.VDP.VRAM[m.VDP.phys(m.vramBase()+int(dst)+int(i))] = m.rd(src + i)
		}
		// The closing `ex de,hl`: out come the destination and the
		// source-past-the-block, in that order. See above.
		m.setHL(dst)
		m.setDE(src + n)
		m.B, m.C, m.A = 0, VDPDataPort, 0

	case dskIO, dskChg, getDPB, choice, dskFmt, mtOff, dskBoot:
		// The disk ROM's raw sector calls. See diskROM.
		m.diskROM(addr)
	case dosBDOS, 0x0005: // the disk function call, from BASIC or from DOS
		m.dos()

	case 0x0038: // the interrupt entry, with no hook of the program's own
		m.biosInterrupt()

	case 0x005F: // CHGMOD  set up the screen mode in A
		// This did nothing, on the reasoning that a cartridge sets
		// the chip up itself anyway -- true of every cartridge here,
		// and not true of a disk program, which asks the BIOS the way
		// a program is supposed to. Snatcher asks, and a machine that
		// shrugged left it on the screen it booted in.
		//
		// The main ROM's own entry only knows the four text and tile
		// screens; the bitmap ones belong to the sub-ROM. Both are
		// set up the same way here, because both are this machine's.
		if m.A <= 3 {
			m.setScreen(int(m.A))
		} else {
			m.setScreenBitmap(int(m.A))
		}
		m.clsScreen()

	case 0x0062: // CHGCLR  the screen colours, from the work area
		// Read out of C-BIOS at 02D4h: register 7 takes the foreground
		// in its high nibble and the border in its low one, and in
		// SCREEN 1 -- and only there -- the colour table is repainted
		// foreground over background as well.
		fore := m.Mem[ForClr] & 0x0F
		back := m.Mem[BakClr] & 0x0F
		bord := m.Mem[BdrClr] & 0x0F
		m.VDP.Reg[7] = fore<<4 | bord
		if m.Mem[ScrMod] == 1 {
			base := int(m.VDP.Reg[3]) << 6
			for i := 0; i < 32; i++ {
				m.VDP.VRAM[(base+i)&0x3FFF] = fore<<4 | back
			}
		}

	case 0x0090: // GICINI  reset the sound chip
		for r := byte(0); r < 14; r++ {
			m.PSG.Write(r, 0)
		}
		m.PSG.Write(7, 0xB8)

	case 0x00C0: // BEEP

	case 0x0020: // DCOMPR  compare HL with DE: Z if equal, C if HL < DE
		// Reached as `rst 20h` -- one byte against three for a
		// sixteen-bit compare, which is why cartridges lean on it.
		hl, de := m.HL(), m.DE()
		m.Fz = hl == de
		m.Fc = hl < de
		m.Fs = false
		m.Fn = true

	case 0x00D5: // GTSTCK  A = which stick -> A = direction 0..8
		// The entry points are a jump table on a three-byte grid, and
		// these two were a slot low: D1h is not on the grid at all,
		// and D5h is GTSTCK, where this machine had GTTRIG. A
		// cartridge asking the cursor keys which way to walk was told
		// whether the trigger was down. Checked against the real
		// BIOS ROMs, whose tables run to 0159h on an MSX1, 0177h on
		// an MSX2 and 017Dh on an MSX2+.
		if m.A == 0 {
			m.A = stickDirection(m.cursorAsStick())
		} else {
			m.A = stickDirection(m.PSG.PortA)
		}

	case 0x00D8: // GTTRIG  A = which trigger -> A = FFh pressed
		which := m.A
		m.A = 0
		if which == 0 {
			// Trigger zero is the space bar.
			if m.Keys[8]&0x01 == 0 {
				m.A = 0xFF
			}
		} else if m.PSG.PortA&0x10 == 0 {
			m.A = 0xFF
		}

	case 0x00DB, 0x00DE: // GTPAD, GTPDL  no tablet, no paddle
		m.A = 0

	// ---- Screen setup -------------------------------------------------
	//
	// The BIOS programs the chip from the table addresses it keeps in the
	// work area rather than from constants, so a cartridge that moves a
	// table and then calls the setup routine gets its own layout back.
	// InstallSystemBytes seeds those words with what a real MSX2 BIOS has
	// at a cartridge's INIT.

	case 0x0078: // SETTXT  the 40-column text screen, chip only
		m.setScreen(0)
	case 0x007B: // SETT32  the 32-column text screen
		m.setScreen(1)
	case 0x007E: // SETGRP  the graphic screen
		m.setScreen(2)
	case 0x0081: // SETMLT  the multicolour screen
		m.setScreen(3)

	case 0x006C: // INITXT  set the screen and clear it
		m.setScreen(0)
		m.clsScreen()
	case 0x006F: // INIT32
		m.setScreen(1)
		m.clsScreen()
	case 0x0072: // INIGRP
		m.setScreen(2)
		m.clsScreen()
	case 0x0075: // INIMLT
		m.setScreen(3)
		m.clsScreen()

	case 0x00C3: // CLS
		m.clsScreen()

	case 0x00C6: // POSIT  H = column, L = row
		m.Mem[csrX] = m.H
		m.Mem[csrY] = m.L

	case 0x00D2: // TOTEXT  back to a text screen
		if m.Mem[scrMod] > 1 {
			m.setScreen(int(m.Mem[oldScr]) & 1)
			m.clsScreen()
		}

	// ---- Sprites ------------------------------------------------------

	case 0x0069: // CLRSPR  park every sprite
		attr, patt := m.spriteTables()
		size := 8
		if m.VDP.Reg[1]&0x02 != 0 {
			size = 32
		}
		_ = patt
		for i := 0; i < 32; i++ {
			// 209 is the BIOS's parking line: past the bottom of
			// every screen but not the end-of-list marker, so the
			// sprites stay addressable.
			m.vramWrite(attr+i*4, 209)
			m.vramWrite(attr+i*4+1, 0)
			m.vramWrite(attr+i*4+2, byte(i*size/8))
			m.vramWrite(attr+i*4+3, m.Mem[forClr]&0x0F)
		}

	case 0x0084: // CALPAT  A = sprite -> HL = its pattern's address
		_, patt := m.spriteTables()
		step := 8
		if m.VDP.Reg[1]&0x02 != 0 {
			step = 32
		}
		m.setHL(uint16(patt + int(m.A)*step))

	case 0x0087: // CALATR  A = sprite -> HL = its attribute's address
		attr, _ := m.spriteTables()
		m.setHL(uint16(attr + int(m.A)*4))

	case 0x008A: // GSPSIZ  A = bytes in a sprite, carry if 16 by 16
		m.A, m.Fc = 8, false
		if m.VDP.Reg[1]&0x02 != 0 {
			m.A, m.Fc = 32, true
		}

	// ---- Video memory over the whole 128K ------------------------------
	//
	// The MSX2 additions. The older four routines address 16K because
	// that is all an MSX1 has; these carry the full address, and a
	// cartridge that uses them on a screen above 16K needs them not to be
	// quietly masked down.

	// The four whole-address video-memory entries, at the addresses the
	// official table and the real ROM put them. They were first written
	// three bytes high -- 0177h through 0180h -- which are not entries at
	// all, so a program calling the real ones fell into the unknown-call
	// path while the implementations sat unreachable beside them.
	case 0x016E: // NSETRD  HL = address, set up for reading
		m.VDP.SetAddrFull(m.vramBase()+int(m.HL()), false)
	case 0x0171: // NSTWRT  HL = address, set up for writing
		m.VDP.SetAddrFull(m.vramBase()+int(m.HL()), true)
	case 0x0174: // NRDVRM  HL = address -> A
		// Hardware agrees: a byte back in A, every register preserved.
		m.tick(cycVRAMByte)
		m.A = m.VDP.VRAM[m.VDP.phys(m.vramBase()+int(m.HL()))]
	case 0x0177: // NWRVRM  HL = address, A = value
		m.tick(cycVRAMByte)
		m.vramWrite(m.vramBase()+int(m.HL()), m.A)

	// ---- Keyboard ------------------------------------------------------

	case 0x00A2: // CHPUT  put the character in A where the cursor is
		m.chPut(m.A)

	case 0x009C: // CHSNS  Z when nothing is waiting
		m.Fz = m.keyEvent(false) == 0
	case 0x009F: // CHGET  wait for a character
		// There is no waiting here: a shim that blocked would stop the
		// machine that produces the keypress. A cartridge polls this
		// in a loop and gets its character on the frame it arrives --
		// once. The real BIOS buffers a press as one character and
		// repeats only after its typematic delay; handing the held key
		// back on every poll made Breaker's menu race through its
		// options faster than a finger could leave the key.
		m.A = m.keyEvent(true)
	case 0x0156: // KILBUF  empty the keyboard buffer
	case 0x00B7: // BREAKX  carry when CTRL-STOP is held
		m.Fc = m.Keys[6]&0x02 == 0 && m.Keys[7]&0x10 == 0
	case 0x00BA, 0x00BD: // ISCNTC, CKCNTC  BASIC's break checks

	// ---- Odds and ends -------------------------------------------------

	case 0x003B: // INITIO  reset the sound chip and the ports
		for r := byte(0); r < 14; r++ {
			m.PSG.Write(r, 0)
		}
		m.PSG.Write(7, 0xB8)
	case 0x003E: // INIFNK  restore the function keys
	case 0x0099: // STRTMS  start background music
	case 0x014A: // ISFLIO  no file input or output is in progress
		m.A, m.Fz = 0, true
	case 0x014D: // OUTDLP  print a character
	case 0x0147: // FORMAT  no drive to format
		m.Fc = true

	case 0x00E1, 0x00E4, 0x00E7, 0x00EA, 0x00ED, 0x00F0, 0x00F3, 0x00F6,
		0x00F9: // the tape routines
		// There is no cassette. Report the failure rather than
		// pretending, so a loader gives up and says so instead of
		// waiting for a tone that is never coming.
		m.Fc = true

	case 0x016B: // BIGFIL  HL = video address, BC = length, A = value
		// The entry the official table and the real ROM both put here.
		// The hardware measurement fits it once read without prejudice:
		// BC comes back zero and everything else is untouched -- DE is
		// *preserved*, not consumed, which an earlier reading took for
		// a copy source. Breaker fills alternating lines of 00h and FFh
		// with it, 128 bytes a call.
		n := int(m.BC())
		m.tick(uint32(n) * cycVRAMByte)
		for i := 0; i < n; i++ {
			m.vramWrite(m.vramBase()+int(m.HL())+i, m.A)
		}
		m.setBC(0)

	case 0x015C, 0x015F: // the sub-ROM callers, routine named by IX
		if !m.subRom(m.IX) {
			m.subRomUnknown(m.IX)
		}

	case 0x0030: // CALLF  rst 30h, with the slot and address inline
		// The three bytes after the restart are the slot and the
		// address to call. Every page is mapped here, so the slot is
		// already right; what matters is stepping the return address
		// past those three bytes, or the caller returns into its own
		// arguments and executes them.
		ret := uint16(m.Mem[m.SP]) | uint16(m.Mem[m.SP+1])<<8
		target := uint16(m.Mem[ret+1]) | uint16(m.Mem[ret+2])<<8
		m.Mem[m.SP] = byte(ret + 3)
		m.Mem[m.SP+1] = byte((ret + 3) >> 8)
		m.run(target)

	case 0x001C: // CALSLT  call the routine at IX in the slot named by IY
		// Every page is mapped here, so the slot is already right and
		// this is an ordinary call -- unless the routine is one this
		// machine shims rather than holds, which is how a game that
		// found the disk ROM by scanning the slots calls DSKIO. Going
		// straight to IX there would execute whatever RAM is at 4010h.
		// Which slot is not a guess here: CALSLT is told, in IY. A
		// routine in the BIOS's slot is a BIOS routine even while
		// page zero holds something else, and that is the whole
		// point of the call -- it is how a program running under the
		// disk operating system, whose page zero is its own RAM,
		// reaches the BIOS at all. Deciding by what page zero holds
		// instead sent Snatcher's screen setup into its own data.
		inBIOSSlot := m.IX < 0x4000 && byte(m.IY>>8)&3 == slotBIOS
		if m.isBIOS(m.IX) || inBIOSSlot {
			m.bios(m.IX)
		} else {
			m.run(m.IX)
		}

	default:
		m.biosUnknown(addr)
	}
}

// stickDirection turns the joystick port's active-low bits into the eight-way
// number GTSTCK reports, which is 1 for up and goes round clockwise.
func stickDirection(port byte) byte {
	up := port&0x01 == 0
	down := port&0x02 == 0
	left := port&0x04 == 0
	right := port&0x08 == 0
	switch {
	case up && right:
		return 2
	case up && left:
		return 8
	case down && right:
		return 4
	case down && left:
		return 6
	case up:
		return 1
	case right:
		return 3
	case down:
		return 5
	case left:
		return 7
	}
	return 0
}

func biosName(a uint16) string {
	switch a {
	case 0x0047:
		return "WRTVDP"
	case 0x004A:
		return "RDVRM"
	case 0x004D:
		return "WRTVRM"
	case 0x0050:
		return "SETRD"
	case 0x0053:
		return "SETWRT"
	case 0x0093:
		return "WRTPSG"
	case 0x0096:
		return "RDPSG"
	case 0x013E:
		return "RDVDP"
	case 0x0141:
		return "SNSMAT"
	}
	return fmt.Sprintf("BIOS_%04X", a)
}

// BIOSImplemented is every entry point the switch above handles, with the
// name it is known by. msx2go checks a cartridge's calls against this and
// says what is missing.
var BIOSImplemented = map[uint16]string{
	0x0047: "WRTVDP", 0x004A: "RDVRM", 0x004D: "WRTVRM",
	0x0050: "SETRD", 0x0053: "SETWRT", 0x0093: "WRTPSG",
	0x0096: "RDPSG", 0x013E: "RDVDP", 0x0141: "SNSMAT",
	0x0020: "DCOMPR",
	0x0138: "RSLREG", 0x013B: "WSLREG", 0x000C: "RDSLT",
	0x0014: "WRSLT", 0x0024: "ENASLT", 0x001C: "CALSLT",
	0x0030: "CALLF",
	0x0038: "KEYINT", 0x003B: "INITIO", 0x003E: "INIFNK",
	0x0069: "CLRSPR", 0x006C: "INITXT", 0x006F: "INIT32",
	0x0072: "INIGRP", 0x0075: "INIMLT", 0x0078: "SETTXT",
	0x007B: "SETT32", 0x007E: "SETGRP", 0x0081: "SETMLT",
	0x0084: "CALPAT", 0x0087: "CALATR", 0x008A: "GSPSIZ",
	0x0099: "STRTMS", 0x009C: "CHSNS", 0x009F: "CHGET",
	0x00B7: "BREAKX", 0x00BA: "ISCNTC", 0x00BD: "CKCNTC",
	0x00C3: "CLS", 0x00C6: "POSIT", 0x00D2: "TOTEXT",
	0x00D8: "GTTRIG", 0x00DB: "GTPAD", 0x00DE: "GTPDL",
	0x0144: "PHYDIO", 0x0147: "FORMAT", 0x014A: "ISFLIO",
	0x014D: "OUTDLP", 0x0156: "KILBUF", 0x0159: "CALBAS",
	0x015C: "SUBROM", 0x015F: "EXTROM", 0x016B: "BIGFIL",
	0x016E: "NSETRD", 0x0171: "NSTWRT", 0x0174: "NRDVRM",
	0x0177: "NWRVRM", 0x0041: "DISSCR",
	0x0044: "ENASCR", 0x0056: "FILVRM", 0x0059: "LDIRMV",
	0x005C: "LDIRVM", 0x005F: "CHGMOD", 0x0062: "CHGCLR",
	0x0090: "GICINI",
	0x00C0: "BEEP", 0x00D1: "GTSTCK", 0x00D5: "GTTRIG",
}

// MSX page-0 system bytes the ROM reads directly: the VDP data ports. The
// game's VRAM writers fetch these and then use `out (c),a`.
const (
	// VDPDataPort is what the block routines leave in C, having used it
	// as the port for their OUTI and INI loops.
	VDPDataPort = 0x98

	// Locale is the byte a cartridge reads to find out what kind of machine
	// it is on. See InstallSystemBytes.
	Locale = 0x002B

	// Version is the MSX generation: 0 for an MSX1, 1 for an MSX2. A
	// cartridge that finds a zero here on a machine with a V9938 in it
	// configures itself for hardware it is not running on.
	Version = 0x002D

	// RG0SAV and RG8SAV are where the BIOS keeps its copy of the VDP
	// registers -- 0 to 7 in page three's low area, 8 to 23 at the top of
	// memory, MSX2 only. A cartridge that wants to change one bit of a
	// write-only register reads the saved copy, edits it and writes it
	// back, so what sits here ends up in the chip.
	RG0SAV = 0xF3DF
	RG8SAV = 0xFFE7

	// The screen colours and the current mode, in the BIOS work area.
	// CHGCLR reads them; a cartridge sets them.
	ForClr = 0xF3E9
	BakClr = 0xF3EA
	BdrClr = 0xF3EB
	ScrMod = 0xFCAF

	VDPDataRead  = 0x0006
	VDPDataWrite = 0x0007
)

// saveReg keeps the BIOS's copy of a VDP register in step with the chip, as
// the real WRTVDP does. Registers are write-only, so a cartridge that wants
// to change one bit reads the saved byte, edits it and writes it back --
// which only works if what it reads is what was last written.
//
// Space Manbow's setup writes register 8 once with the transparency bit set,
// and its interrupt handler thereafter rebuilds the register from the saved
// copy twice a frame, turning the sprite plane off over its status panel and
// on again below. With nothing saving that first write, the handler read the
// boot value back for the rest of the game and the bit was lost within a
// frame of being set.
func (m *M) saveReg(r, v byte) {
	switch {
	case r < 8:
		m.Mem[RG0SAV+uint16(r)] = v
	case r < 24:
		m.Mem[RG8SAV+uint16(r)-8] = v
	}
}

// InstallSystemBytes populates the page-0 bytes the ROM depends on.
func (m *M) InstallSystemBytes() {
	m.Mem[VDPDataRead] = 0x98
	m.Mem[VDPDataWrite] = 0x98
	// The character set, and the pointer that names it. CGPNT in the
	// work area mirrors it: slot byte, then the address.
	copy(m.Mem[fontTable:], biosFont)
	m.Mem[0x0004] = byte(fontTable & 0xFF)
	m.Mem[0x0005] = byte(fontTable >> 8)
	m.Mem[0xF91F] = 0
	m.Mem[0xF920] = byte(fontTable & 0xFF)
	m.Mem[0xF921] = byte(fontTable >> 8)
	// The locale byte. Bit 7 is the machine's vertical frequency -- clear
	// for 60 Hz, set for 50 -- and a cartridge that asks is entitled to an
	// answer that matches the rate its interrupts actually arrive at.
	// Salamander reads it thirteen times; King's Valley never does.
	//
	// The low bits say character set and date format. Zero is a Japanese
	// machine, which is what the cartridges this runs are made for.
	m.Mem[Locale] = 0
	if m.Hz == 50 {
		// A 50Hz machine says so in the locale byte, and the real
		// European BIOS pairs it with the international character
		// set; a cartridge that reads either is entitled to a
		// machine that is consistent about it.
		m.Mem[Locale] = 0x91
		m.Mem[Locale+1] = 0x11
	}
	m.Mem[Version] = 1
	// The MSX2 work area's screen-and-memory byte, which the sub-ROM
	// maintains on a real machine and which a program is entitled to
	// read before it has touched the chip at all. Snatcher reads bits
	// one and two of it before doing anything else and refuses to run
	// on a machine that answers with less than two -- so a machine
	// that leaves it at zero gets the game's own "this will not run
	// here" message and nothing else. Four is what the reference
	// machine holds.
	m.Mem[0xFAFC] = 0x04

	// The slot layout, which is a fiction here -- everything is paged in
	// already -- but has to be a *consistent* fiction, because a
	// cartridge reads it and acts on it. D4h is what the reference
	// machine's port A8h reads while a game runs: page zero the BIOS's
	// slot 0, pages one and two the cartridge's slot 1, page three RAM in
	// slot 3. Left at zero it says every page is slot zero, which is
	// nothing like this machine, and a cartridge that saves the register,
	// switches page zero to RAM to run code there and puts it back
	// computes a value that changes nothing.
	//
	// EXPTBL and SLTTBL go with it: slot 3 expanded, its subslot register
	// A0h. Read off the reference rather than invented.
	// F4h is what a real MSX2 BIOS's slot register reads at INIT: page
	// zero the BIOS, page one the cartridge, pages two and three RAM in
	// slot 3. The cartridge pages its own upper half in itself -- Konami
	// mappers read RSLREG, compute their slot and write it back -- which
	// is why starting with page two already claimed for the cartridge,
	// as D4h did, was a fiction no real machine shows.
	m.PrimarySlot = 0xF4
	copy(m.Mem[0xFCC1:], []byte{0x00, 0x00, 0x00, 0x80})
	copy(m.Mem[0xFCC5:], []byte{0x00, 0x00, 0x00, 0xA0})
	// Where the RAM is, one byte per page: expanded slot three, subslot
	// two, which is what the slot table above already says and what the
	// reference machine holds. A program that wants RAM in a page does
	// not assume where it is -- it reads these and calls ENASLT with
	// what it finds. Left at zero that call asks for slot zero, which
	// is the BIOS, and Snatcher put the BIOS where its own code should
	// have been and ran off into it.
	for a := uint16(0xF341); a <= 0xF344; a++ {
		m.Mem[a] = 0x8B
	}

	// Page zero as the BIOS leaves it, before anything else can write
	// over it. A program running under the disk operating system has
	// its own RAM there and still asks the BIOS's slot for these bytes
	// -- the video ports at 0006h and 0007h above all, which MSX-DOS's
	// own entry jump at 0005h happens to sit on top of.
	m.bios0 = append([]byte(nil), m.Mem[:0x4000]...)

	m.installWorkArea()

	// The hook table. Every one of the BIOS's expansion hooks is five
	// bytes in the work area, and the machine fills all of them with a
	// single `ret` at power-on so that a hook nobody has claimed returns
	// at once. A cartridge that installs one writes `jp nnnn` over the
	// top; a program that *calls* one -- King's Valley Plus's interrupt
	// handler calls H.TIMI two instructions in -- needs to find something
	// there. Left as zeros the call slides through six hundred nops and
	// out of the work area, which is where the disk version went.
	for a := hKeyI; a <= 0xFFC9; a++ {
		m.Mem[a] = 0xC9
	}

	// And the chip itself, which the BIOS has already programmed by the
	// time a cartridge runs: a real machine enters a cartridge's INIT
	// showing SCREEN 0 -- the 40-column text screen the boot messages
	// were printed on -- not a VDP with every register zero. Read off a
	// real MSX2 BIOS at a breakpoint on the cartridge's INIT, which is
	// the only moment that is the right one to sample: earlier is the
	// BIOS mid-setup, later is the cartridge's own work.
	//
	// A cartridge that sets every register it uses never notices. Space
	// Manbow does not set register 3, and on a real machine finds the
	// 80h the BIOS left there.
	//
	// Only the first eight: registers 8 and up exist on a V9938, and
	// writing one is how this machine learns it is talking to a V9938 at
	// all. Setting them here would make every machine an MSX2. See
	// goV9938.
	copy(m.VDP.Reg[:8], []byte{0x00, 0x60, 0x06, 0x80, 0x00, 0x36, 0x07, 0x04})

	// The BIOS's saved registers, as a stock MSX2 leaves them at boot.
	// Read from the reference machine at three seconds, before any
	// cartridge has had a chance to change them.
	//
	// RG8SAV's bit 3 is VR, which tells the V9938 its memory is 64K or
	// more. Space Manbow reads the saved byte, keeps that bit and writes
	// its own on top; finding a zero here, it configured the chip for a
	// machine with a sixteenth of the memory and put its sprite and name
	// tables somewhere else entirely.
	copy(m.Mem[RG0SAV:], []byte{0x00, 0x60, 0x06, 0x80, 0x00, 0x36, 0x07, 0x04})
	m.Mem[RG8SAV] = 0x08
	if m.Hz == 50 {
		// Register 9's saved copy carries the frequency bit the BIOS
		// chose, and the chip agrees with it.
		m.Mem[RG8SAV+1] = 0x02
		m.VDP.Reg[9] |= 0x02
	}
}

// oldKey and newKey are the BIOS's keyboard tables: the matrix as the
// interrupt handler last read it, and the read before that.
const (
	oldKey = 0xFBDA
	newKey = 0xFBE5
)

// jiffy is the BIOS's frame counter. Its interrupt handler advances it once
// per interrupt and programs read it to time themselves.
const jiffy = 0xFC9E

// biosInterrupt is the BIOS's own interrupt handler, reached when an
// interrupt arrives and the program has installed no hook for deliver to run
// in its place.
//
// A cartridge always installs one, so nothing needed this until a program
// running under the disk operating system put the BIOS back into page zero --
// which is what Snatcher's loader does before starting the game, because from
// then on it wants to be an ordinary MSX program again -- and let the
// interrupt it had just enabled arrive. Page zero was the BIOS once more, so
// 0038h was a BIOS entry, and it was one with no shim behind it.
//
// Two things it does are visible from outside. It reads status register zero,
// which is what acknowledges the interrupt the chip is asking for, and it
// advances the frame counter. The hooks it would call after that are the ones
// deliver runs instead of this when the program has installed any.
//
// The read is of register zero specifically. The hardware reads whichever
// register 15 selects and a program is expected to leave that at zero for
// exactly this reason; a machine that inherited a program's own selection
// here would answer the acknowledgement out of the wrong register and leave
// the interrupt standing.
func (m *M) biosInterrupt() {
	was := m.VDP.Reg[15]
	m.VDP.Reg[15] = 0
	m.VDP.ReadStatus()
	m.VDP.Reg[15] = was
	m.wr16(jiffy, m.rd16(jiffy)+1)
	// The keyboard scan: the handler reads the matrix into NEWKEY, with
	// the previous state moved to OLDKEY first. A program reads these
	// tables rather than the hardware, and the tables are active-low the
	// way the matrix is -- cleared RAM reads as every key on the keyboard
	// held down at once, and a program waiting for the machine's keys to
	// come up waits for the rest of its life.
	for i := 0; i < 11; i++ {
		m.Mem[oldKey+uint16(i)] = m.Mem[newKey+uint16(i)]
		m.Mem[newKey+uint16(i)] = m.Keys[i]
	}
}
