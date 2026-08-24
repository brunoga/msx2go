package z80

import "strings"

// The parts of the BIOS that program the chip and clear the screen, and the
// work-area variables they read.
//
// The real BIOS does not hold the table addresses as constants: it keeps them
// in the work area, one block of five words per screen mode, and its setup
// routines program the chip from whichever block the mode calls for. A
// cartridge that moves a table and then asks for the screen gets its own
// layout back, which is why this is worth doing the same way rather than
// writing the default addresses into the registers directly.
//
// The defaults installed at boot are what a real MSX2 BIOS holds when a
// cartridge's INIT is reached, read from one at a breakpoint there.

const (
	linL40 = 0xF3AE // width of the 40-column screen, then 32, then current
	crtCnt = 0xF3B1 // rows on the screen
	txtNam = 0xF3B3 // SCREEN 0: name, colour, pattern, sprite attr, sprite pat
	t32Nam = 0xF3BD // SCREEN 1, same five words
	grpNam = 0xF3C7 // SCREEN 2
	mltNam = 0xF3D1 // SCREEN 3
	csrY   = 0xF3DC // cursor row, then column
	csrX   = 0xF3DD
	forClr = 0xF3E9 // foreground, background, border
	bakClr = 0xF3EA
	bdrClr = 0xF3EB
	scrMod = 0xFCAF // the screen mode in force, and the one before it
	oldScr = 0xFCB0
	dpPage = 0xFAF5 // the page on display, and the one video calls address
	acPage = 0xFAF6
)

// screenTableBase is the work-area block for a screen mode.
func screenTableBase(mode int) uint16 {
	switch mode {
	case 0:
		return txtNam
	case 1:
		return t32Nam
	case 2:
		return grpNam
	default:
		return mltNam
	}
}

// wa16 reads one of the work area's little-endian words.
func (m *M) wa16(a uint16) uint16 {
	return uint16(m.Mem[a]) | uint16(m.Mem[a+1])<<8
}

// vramBase is what the BIOS adds to every video-memory address a program
// hands it: in the paged screens, the active page's base. Measured on the
// running reference: Breaker's title sets ACPAGE to 2 and then asks for
// address 8000h, and the byte it gets is the one at 18000h. An MSX1 screen
// has no pages and the base is zero, which keeps every MSX1 machine exactly
// as it was.
func (m *M) vramBase() int {
	mode := m.Mem[scrMod]
	if mode < 5 {
		return 0
	}
	size := 0x8000
	if mode >= 7 {
		size = 0x10000
	}
	return int(m.Mem[acPage]) * size
}

// vramWrite writes one byte of video memory, over the whole of it rather than
// the first 16K an MSX1 has.
func (m *M) vramWrite(a int, v byte) {
	m.VDP.VRAM[m.VDP.phys(a)] = v
}

// spriteTables is where the sprites' attributes and patterns are, from the
// registers rather than the work area: a cartridge that moves them by writing
// the registers has moved them, whatever the work area still says.
func (m *M) spriteTables() (attr, patt int) {
	if m.VDP.V9938 {
		attr = int(m.VDP.Reg[5]&0xFC)<<7 | int(m.VDP.Reg[11]&0x03)<<15
	} else {
		attr = int(m.VDP.Reg[5]) << 7
	}
	return attr, int(m.VDP.Reg[6]&0x3F) << 11
}

// setReg writes a chip register and the work area's copy of it together, the
// way WRTVDP does, so a cartridge that reads the copy back sees the truth.
func (m *M) setReg(r, v byte) {
	m.VDP.WriteReg(r, v)
	m.saveReg(r, v)
}

// setScreen programs the chip for one of the four screen modes an MSX1 BIOS
// knows, from the table addresses in the work area.
func (m *M) setScreen(mode int) {
	t := screenTableBase(mode)
	nam, col := m.wa16(t), m.wa16(t+2)
	cgp, atr, pat := m.wa16(t+4), m.wa16(t+6), m.wa16(t+8)
	colour := m.Mem[forClr]&0x0F<<4 | m.Mem[bakClr]&0x0F

	switch mode {
	case 0:
		// The text screen has no colour table and no sprites.
		m.setReg(0, 0x00)
		m.setReg(1, 0xF0)
		m.setReg(2, byte(nam>>10))
		m.setReg(4, byte(cgp>>11))
	case 1:
		m.setReg(0, 0x00)
		m.setReg(1, 0xE0)
		m.setReg(2, byte(nam>>10))
		m.setReg(3, byte(col>>6))
		m.setReg(4, byte(cgp>>11))
	case 2:
		// The graphic screen's colour and pattern tables cover the
		// whole of the name table's three thirds, and the low bits of
		// registers 3 and 4 are the mask that says so. Writing the
		// address alone leaves the mask at zero, which folds all
		// three thirds onto the first.
		m.setReg(0, 0x02)
		m.setReg(1, 0xE0)
		m.setReg(2, byte(nam>>10))
		m.setReg(3, byte(col>>6)|0x7F)
		m.setReg(4, byte(cgp>>11)|0x03)
	default:
		m.setReg(0, 0x00)
		m.setReg(1, 0xE8)
		m.setReg(2, byte(nam>>10))
		m.setReg(3, byte(col>>6))
		m.setReg(4, byte(cgp>>11))
	}
	if mode > 0 {
		m.setReg(5, byte(atr>>7))
		m.setReg(6, byte(pat>>11))
	}
	m.setReg(7, colour)
	if mode == 0 {
		m.setReg(7, m.Mem[forClr]&0x0F<<4|m.Mem[bdrClr]&0x0F)
	}
	m.Mem[oldScr] = m.Mem[scrMod]
	m.Mem[scrMod] = byte(mode)
	m.Mem[csrX], m.Mem[csrY] = 1, 1
}

// setScreenBitmap sets one of the V9938's bitmap screens, which the main
// BIOS cannot do: screens 4 to 8 belong to the sub-ROM, and a program reaches
// them through EXTROM. Breaker asks for screen 5 that way, and a machine that
// answers "not implemented" leaves the chip in whatever mode it was in while
// the program fills memory with a picture -- which is a garbled screen.
//
// The table addresses are the ones the BIOS uses for page 0: the picture at
// the bottom of memory, the sprite attributes and patterns above it. Screens
// 7 and 8 put their sprite tables in the second 64K, which is what register
// 11 is for.
func (m *M) setScreenBitmap(mode int) {
	var r0, r5, r6, r11 byte
	switch mode {
	// Register 0's mode bits, from the V9938's own table: M5 M4 M3 are
	// 010 for graphic 3, 011 for graphic 4, 100 for graphic 5, 101 for
	// graphic 6 and 111 for graphic 7. The first cut of this table had
	// every one of them a mode high -- screen 5 got screen 6's bits --
	// and every command the chip then ran used the wrong line width.
	case 4: // still a tile screen, but with the V9938's sprites
		r0, r5, r6, r11 = 0x04, 0xEF, 0x03, 0x00
	case 5:
		r0, r5, r6, r11 = 0x06, 0xEF, 0x0F, 0x00
	case 6:
		r0, r5, r6, r11 = 0x08, 0xEF, 0x0F, 0x00
	case 7:
		r0, r5, r6, r11 = 0x0A, 0xF7, 0x1E, 0x01
	default: // 8
		r0, r5, r6, r11 = 0x0E, 0xF7, 0x1E, 0x01
	}
	m.setReg(0, r0)
	m.setReg(1, 0x60) // displaying, vertical interrupt on, no mode bits
	m.setReg(2, 0x1F) // page zero
	m.setReg(3, 0xFF)
	m.setReg(4, 0x03)
	m.setReg(5, r5)
	m.setReg(6, r6)
	m.setReg(7, 0)
	m.setReg(11, r11)
	m.Mem[oldScr] = m.Mem[scrMod]
	m.Mem[scrMod] = byte(mode)
	m.Mem[csrX], m.Mem[csrY] = 1, 1
}

// subRom is a routine in the MSX2 sub-ROM, named by the address in IX and
// reached through EXTROM or SUBROM. It reports whether it knew the routine.
func (m *M) subRom(ix uint16) bool {
	switch ix {
	case 0x00D1: // CHGMOD  A = the screen to set up
		if m.A <= 3 {
			m.setScreen(int(m.A))
		} else {
			m.setScreenBitmap(int(m.A))
		}
		m.clsScreen()
		return true
	// The sub-ROM's entries are four bytes each -- an EI and a jump -- not
	// the three the main ROM uses, so they sit at 013Dh, 0141h, 0145h and
	// so on. Read off the real sub-ROM rather than assumed: 013Dh reads
	// the screen mode, points at a table and calls a block writer, which
	// is the palette being set to its defaults.
	case 0x00C9, 0x00CD: // NVBXLN and NVBXFL  a box, outlined or filled
		// BASIC's LINE (x1,y1)-(x2,y2),c,B and ,BF. The start point
		// arrives in BC and DE, the end point in GXPOS and GYPOS, the
		// colour in ATRBYT and the logical operation in LOGOPR --
		// read off the real sub-ROM, whose routines work from exactly
		// those. A bitmap screen draws into the active page.
		x1, y1 := int(m.BC()), int(m.DE())
		x2 := int(m.Mem[0xFCB3]) | int(m.Mem[0xFCB4])<<8 // GXPOS
		y2 := int(m.Mem[0xFCB5]) | int(m.Mem[0xFCB6])<<8 // GYPOS
		if x2 < x1 {
			x1, x2 = x2, x1
		}
		if y2 < y1 {
			y1, y2 = y2, y1
		}
		if m.Mem[scrMod] >= 5 {
			page := int(m.Mem[acPage]) * 256
			y1, y2 = y1+page, y2+page
		}
		clr := m.Mem[0xF3F2]     // ATRBYT
		op := m.Mem[0xFB02] & 15 // LOGOPR
		m.tick(uint32((x2-x1+1)*(y2-y1+1)) * cycVRAMByte / 4)
		for yy := y1; yy <= y2; yy++ {
			for xx := x1; xx <= x2; xx++ {
				if ix == 0x00C9 && yy != y1 && yy != y2 &&
					xx != x1 && xx != x2 {
					continue // outline only
				}
				m.VDP.setPixel(xx, yy, clr, op)
			}
		}
		m.A = 0
		m.Fc = false
		return true

	case 0x0115: // CHGCLR  take the colours from the work area
		// Identified from the real sub-ROM, whose routine here reads
		// BAKCLR. On a text or tile screen register 7 carries the
		// foreground and the background; on a bitmap screen it
		// carries only the border, because the rest is the palette.
		if m.Mem[scrMod] <= 3 {
			m.setReg(7, m.Mem[forClr]&0x0F<<4|m.Mem[bakClr]&0x0F)
		} else {
			m.setReg(7, m.Mem[bdrClr]&0x0F)
		}
		return true
	case 0x019D: // copy a disk file into video memory
		// BASIC's COPY "file" TO (x,y), which is why no synthetic probe
		// of this entry ever returned: the routine opens a file through
		// the disk ROM, and a probe has no disk context to give it.
		// Read off the running reference, every byte accounted for:
		// the block at HL holds a pointer to a quoted filename and the
		// destination corner; the file's first two words are the
		// rectangle's width and height in pixels; the body is pixels,
		// packed as the screen packs them. The routine arms LMMC and
		// streams the body through register 44.
		if m.Disk == nil {
			return false
		}
		blk := m.HL()
		namePtr := uint16(m.Mem[blk]) | uint16(m.Mem[blk+1])<<8
		name := ""
		if m.Mem[namePtr] == '"' {
			for i := namePtr + 1; m.Mem[i] != '"' && m.Mem[i] != 0; i++ {
				name += string(rune(m.Mem[i]))
			}
		}
		data, ok := m.Disk.Open(strings.ToUpper(name))
		if !ok || len(data) < 4 {
			m.A, m.Fc = 0, true
			return true
		}
		nx := int(data[0]) | int(data[1])<<8
		ny := int(data[2]) | int(data[3])<<8
		dx := int(m.Mem[blk+4]) | int(m.Mem[blk+5])<<8
		dy := int(m.Mem[blk+6]) | int(m.Mem[blk+7])<<8
		m.lmmcStream(dx, dy, nx, ny, data[4:])
		m.A = 0
		m.Fc = false
		return true

	case 0x0195: // copy a block of main RAM into video memory
		// BASIC's COPY array TO (x,y). Measured off the running
		// reference like 019Dh, which it mirrors with the source in
		// RAM instead of on disk: the block at HL is the source
		// address, a byte count, and the destination corner; the
		// source starts with the rectangle's width and height and
		// carries pixels packed as the screen packs them. On return
		// A is zero with carry clear and zero set, BC and DE are
		// zero, and HL points at the last body byte consumed.
		blk := m.HL()
		src := int(m.Mem[blk]) | int(m.Mem[blk+1])<<8
		dx := int(m.Mem[blk+4]) | int(m.Mem[blk+5])<<8
		dy := int(m.Mem[blk+6]) | int(m.Mem[blk+7])<<8
		nx := int(m.Mem[src&0xFFFF]) | int(m.Mem[(src+1)&0xFFFF])<<8
		ny := int(m.Mem[(src+2)&0xFFFF]) | int(m.Mem[(src+3)&0xFFFF])<<8
		// The rectangle says how much to stream -- a pixel a byte at
		// most, fewer in the packed modes; the streamer stops the
		// moment the command engine is satisfied. The block's second
		// word is *not* a byte count: Breaker's typed text passes 40
		// there for a 10-by-18 glyph, and the real sub-ROM's inner
		// copy still moves all 180 pixels. Capping the body at that
		// word cut every letter off four rows in.
		n := nx * ny
		if n > len(m.VDP.VRAM) {
			n = len(m.VDP.VRAM)
		}
		body := make([]byte, 0, n)
		for i := 4; i < 4+n; i++ {
			body = append(body, m.Mem[(src+i)&0xFFFF])
		}
		used := m.lmmcStream(dx, dy, nx, ny, body)
		m.setBC(0)
		m.setDE(0)
		m.setHL(uint16(src + 4 + used - 1))
		m.A = 0
		m.Fc = false
		m.Fz = true
		return true

	case 0x0191: // copy a rectangle described by the block at HL
		// Measured on real hardware rather than guessed. The routine
		// waits for the command engine, points the indirect register
		// pointer at register 32, and pushes fifteen bytes through
		// the indirect port: the fourteen at (HL) -- which are
		// exactly registers 32 to 45, the source and destination
		// corners, the width and height, the colour and the argument
		// -- and then 90h into register 46, which is the copy. HL
		// comes back advanced past the block and A holding the
		// command.
		//
		// Breaker calls this a thousand times to draw its screen. A
		// machine that answers "not implemented" leaves the loop
		// going round for ever, which is why it ground away doing
		// seven times the block transfers of real hardware and never
		// finished.
		m.subRomBlit(0x90)
		return true

	case 0x013D: // INIPLT  the palette the machine starts with
		m.initPalette()
		return true
	case 0x014D: // SETPLT  D = entry, A = red and blue, E = green
		// Measured on real hardware, three calls: it writes A then E
		// to the palette port for the entry in D, and leaves A
		// holding E. The guess this replaces had the routine at the
		// wrong entry *and* the registers the wrong way round, which
		// is what measuring instead of remembering is for.
		m.VDP.SetPalette(m.D&0x0F, m.A, m.E)
		m.paletteShadow(m.D&0x0F, m.A, m.E)
		m.A = m.E
		return true
	}
	return false
}

// subRomGroup says what an unimplemented sub-ROM entry belongs to, from a
// survey of the real sub-ROM: what each entry jumps to, and which ports and
// work-area variables that routine touches. A failure that names the group is
// one somebody can act on; a bare address is a puzzle.
func subRomGroup(ix uint16) string {
	switch {
	case ix >= 0x0091 && ix <= 0x00C1:
		return "the maths pack"
	case ix >= 0x00D9 && ix <= 0x00F1:
		return "screen-table setup"
	case ix == 0x0109 || ix == 0x010D || ix == 0x0129:
		return "video-memory access"
	case ix == 0x0131 || ix == 0x0145 || ix == 0x0149 || ix == 0x014D:
		return "the palette"
	case ix == 0x0115 || ix == 0x0119:
		return "the screen colours"
	case ix >= 0x018D && ix <= 0x01A1:
		return "the graphics primitives"
	}
	return "not on the sub-ROM's four-byte entry grid"
}

// subRomBlit hands the fourteen-byte block at HL to the command engine and
// runs cmd, the way the sub-ROM's rectangle routines do.
// lmmcStream arms an LMMC -- CPU-to-VRAM logical move -- at (dx,dy)
// sized nx by ny and feeds it body bytes, packed as the current screen
// packs pixels: two to a byte on screens 5 and 7, four on screen 6, one
// on screen 8. It stops when the command engine reports done and
// returns how many body bytes it consumed.
func (m *M) lmmcStream(dx, dy, nx, ny int, body []byte) int {
	m.VDP.WriteReg(36, byte(dx))
	m.VDP.WriteReg(37, byte(dx>>8))
	m.VDP.WriteReg(38, byte(dy))
	m.VDP.WriteReg(39, byte(dy>>8))
	m.VDP.WriteReg(40, byte(nx))
	m.VDP.WriteReg(41, byte(nx>>8))
	m.VDP.WriteReg(42, byte(ny))
	m.VDP.WriteReg(43, byte(ny>>8))
	m.VDP.WriteReg(45, 0)
	m.VDP.WriteReg(46, 0xB0) // LMMC, IMP
	perByte := 2
	switch m.Mem[scrMod] {
	case 6:
		perByte = 4
	case 8:
		perByte = 1
	}
	m.tick(uint32(len(body)) * cycVRAMByte)
	used := 0
	for _, b := range body {
		if !m.VDP.XferActive() {
			break
		}
		used++
		switch perByte {
		case 1:
			m.VDP.XferWrite(b)
		case 2:
			m.VDP.XferWrite(b >> 4)
			if m.VDP.XferActive() {
				m.VDP.XferWrite(b & 0x0F)
			}
		default:
			for sh := 6; sh >= 0 && m.VDP.XferActive(); sh -= 2 {
				m.VDP.XferWrite(b >> sh & 3)
			}
		}
	}
	return used
}

func (m *M) subRomBlit(cmd byte) {

	p := m.HL()
	for i := uint16(0); i < 14; i++ {
		m.VDP.WriteReg(byte(cmdSX+i), m.Mem[p+i])
	}
	m.VDP.WriteReg(cmdCMD, cmd)
	// The exit state, measured on the reference: HL past the block, B
	// zero and C the indirect port -- the shape an OTIR leaves -- DE the
	// rectangle's height, A the command, and the carry clear. The carry
	// is the part that matters: it is the routine's failure flag, and
	// leaving it wherever it happened to sit made the caller's loop
	// read success as failure and stop after one copy of the thousand it
	// meant to make.
	m.setHL(p + 14)
	m.setBC(0x009B)
	m.setDE(uint16(m.Mem[p+10]) | uint16(m.Mem[p+11])<<8)
	m.A = cmd
	m.Fc = false
}

// initPalette is the sixteen colours an MSX2 powers up with, as three bits
// each of red, green and blue.
func (m *M) initPalette() {
	def := [16][3]byte{
		{0, 0, 0}, {0, 0, 0}, {1, 6, 1}, {3, 7, 3},
		{1, 1, 7}, {2, 3, 7}, {5, 1, 1}, {2, 6, 7},
		{7, 1, 1}, {7, 3, 3}, {6, 6, 1}, {6, 6, 4},
		{1, 4, 1}, {6, 2, 5}, {5, 5, 5}, {7, 7, 7},
	}
	for i, c := range def {
		m.VDP.SetPalette(byte(i), c[0]<<4|c[2], c[1])
		m.paletteShadow(byte(i), c[0]<<4|c[2], c[1])
	}
}

// paletteShadow keeps the BIOS's copy of the palette in video memory: two
// bytes an entry -- red and blue, then green -- at a table whose address
// depends on the screen, in the active page. Measured on the real machine:
// setting the palette from screen 5 with the third page active writes
// 17682h on. The addresses are the BIOS's own table of them.
func (m *M) paletteShadow(entry, rb, g byte) {
	var table int
	switch m.Mem[scrMod] {
	case 0:
		table = 0x0400
	case 1, 3:
		table = 0x2020
	case 2, 4:
		table = 0x1B80
	case 5, 6:
		table = 0x7680
	default:
		table = 0xFA80
	}
	at := m.vramBase() + table + 2*int(entry)
	m.vramWrite(at, rb)
	m.vramWrite(at+1, g)
}

// clsScreen clears whatever the screen in force shows: the characters on a
// text screen, the pixels on a graphic one.
func (m *M) clsScreen() {
	mode := int(m.Mem[scrMod])
	t := screenTableBase(mode)
	nam, cgp := m.wa16(t), m.wa16(t+4)
	switch mode {
	case 0:
		m.fillVRAM(int(nam), int(m.Mem[linL40])*int(m.Mem[crtCnt]), ' ')
	case 1:
		m.fillVRAM(int(nam), 32*24, ' ')
	case 2:
		// Clearing the patterns is what blanks a graphic screen; the
		// name table is the fixed grid that addresses them.
		m.fillVRAM(int(cgp), 6144, 0)
	case 4:
		// Graphic 3 is a tile screen with the same three thirds of
		// patterns as screen 2.
		m.fillVRAM(int(cgp), 6144, 0)
	case 5, 6, 7, 8:
		// A bitmap screen clears to colour zero -- the backdrop shows
		// through -- over the 212 lines the screen can show and no
		// further. Measured on the real machine: entering screen 5
		// zeroes lines 0-211 and the boot logo's 55h debris below
		// line 212 survives.
		size := 212 * 128
		if mode >= 7 {
			size = 212 * 256
		}
		m.fillVRAM(m.vramBase(), size, 0)
	default:
		m.fillVRAM(int(nam), 32*24, 0)
	}
	m.Mem[csrX], m.Mem[csrY] = 1, 1
}

func (m *M) fillVRAM(at, n int, v byte) {
	m.tick(uint32(n) * cycVRAMByte)
	for i := 0; i < n; i++ {
		m.vramWrite(at+i, v)
	}
}

// keyWaiting is the character the key matrix is showing, or zero for none.
//
// There is no keyboard buffer here and nothing to fill one: a cartridge polls
// CHGET and gets the key on the frame it is pressed. The rows below are the
// ones whose meaning is fixed across every MSX keyboard; the national layouts
// differ in the symbols, which is why only the letters, the digits, the space
// and the return are decoded.
func (m *M) keyWaiting() byte {
	for bit := uint(0); bit < 8; bit++ {
		if m.Keys[0]&(1<<bit) == 0 {
			return byte('0' + bit)
		}
	}
	for bit := uint(0); bit < 2; bit++ {
		if m.Keys[1]&(1<<bit) == 0 {
			return byte('8' + bit)
		}
	}
	// A and B finish row two; C to Z fill rows three, four and five.
	for bit := uint(6); bit < 8; bit++ {
		if m.Keys[2]&(1<<bit) == 0 {
			return byte('A' + bit - 6)
		}
	}
	for row := 3; row <= 5; row++ {
		for bit := uint(0); bit < 8; bit++ {
			if m.Keys[row]&(1<<bit) == 0 {
				return byte('C' + (row-3)*8 + int(bit))
			}
		}
	}
	if m.Keys[8]&0x01 == 0 {
		return ' '
	}
	if m.Keys[7]&0x80 == 0 {
		return 13 // return
	}
	return 0
}

// keyEvent is the character interface's view of the matrix: a press is one
// character, delivered once. consume says this is CHGET taking it; CHSNS
// only looks. The key must come up before it counts again.
func (m *M) keyEvent(consume bool) byte {
	c := m.keyWaiting()
	if c == 0 {
		m.keySeen = 0
		return 0
	}
	if c == m.keySeen {
		return 0
	}
	if consume {
		m.keySeen = c
	}
	return c
}

// installWorkArea seeds the variables the routines above read, with what a
// real MSX2 BIOS holds when a cartridge's INIT is reached.
func (m *M) installWorkArea() {
	copy(m.Mem[linL40:], []byte{0x25, 0x1D, 0x1D, 0x18, 0x0E})
	copy(m.Mem[txtNam:], []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x00, // SCREEN 0
		0x00, 0x18, 0x00, 0x20, 0x00, 0x00, 0x00, 0x1B, 0x00, 0x38, // SCREEN 1
		0x00, 0x18, 0x00, 0x20, 0x00, 0x00, 0x00, 0x1B, 0x00, 0x38, // SCREEN 2
		0x00, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x1B, 0x00, 0x38, // SCREEN 3
	})
	copy(m.Mem[0xF3DB:], []byte{0x01, 0x01, 0x01, 0x00}) // click, cursor, flag
	copy(m.Mem[forClr:], []byte{0x0F, 0x04, 0x04})
	copy(m.Mem[scrMod:], []byte{0x01, 0x01})
}

// Which slot holds what. Every page is mapped at once here, so the slots are
// a fiction -- but a cartridge that goes looking for itself has to find
// itself, once, in the slot the machine says it is in, and find nothing in
// the others. King's Valley II's INIT walks the slot table reading a
// signature byte out of each one; told that every slot holds the cartridge,
// it cannot decide which is its own and gives up without installing its
// interrupt hook, which looks from outside like a cartridge that is not a
// game.
//
// These match the slot register this machine reports at a cartridge's INIT:
// page zero the BIOS in slot 0, pages one and two the cartridge in slot 1,
// page three RAM in slot 3.
const (
	slotBIOS = 0
	slotCart = 1
	slotRAM  = 3
)

// slotHas reports whether an address exists in a slot at all.
func (m *M) slotHas(sl byte, a uint16) bool {
	page := a >> 14
	switch sl & 3 {
	case slotBIOS:
		return page == 0
	case slotCart:
		return page == 1 || page == 2
	case slotRAM:
		return true
	}
	return false
}

// slotRead is what reading an address in a slot gives. An address the slot
// does not hold reads as FFh, which is what an empty slot reads as.
//
// The one place this model has to lie is RAM: a real machine has 64K of it
// in slot 3, separate from the cartridge, and this machine has one flat
// memory where the cartridge already sits. So slot 3 answers with the real
// memory only where the RAM actually lives, and with zero -- an untouched
// byte -- where the cartridge is paged in. A cartridge searching for its own
// signature then finds it in exactly one place.
func (m *M) slotRead(sl byte, a uint16) byte {
	if !m.slotHas(sl, a) {
		return 0xFF
	}
	if sl&3 == slotRAM && a < 0xC000 {
		return 0
	}
	return m.Mem[a]
}
