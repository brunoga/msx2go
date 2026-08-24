package z80

import "image/color"

// VDP is a TMS9918A model that keeps only what the game can observe: 16 KB of
// video RAM, the eight write-only registers, the address latch, and the
// status register. There is no rasteriser here -- rendering reads VRAM
// directly (see internal/gfx), because the game's "graphics" *are* its VRAM
// writes.
type VDP struct {
	// VRAM is 16K on a TMS9918 and 128K on a V9938. It is a slice rather
	// than an array so that an MSX1 machine stays 16K -- every digest and
	// every comparison against a reference is over the whole of it, and
	// padding it with 112K of zeroes would move all of those for nothing.
	// See goV9938.
	VRAM []byte

	// Reg is the register file. A TMS9918 has eight and a V9938 has
	// forty-seven; there is no cost to carrying the space, and masking a
	// register number down to three bits -- which is what this did -- means
	// an MSX2 cartridge writing register 15 silently writes register 7.
	Reg [64]byte

	// V9938 says the cartridge has named a register only that chip has.
	// Nothing configures this: a cartridge that uses the chip says so by
	// using it. See goV9938.
	V9938 bool

	// addr is the fourteen bits the cartridge wrote, kept for the OnWrite
	// hook; at is the full address including register 14's high bits.
	addr uint16
	at   int
	// pal is the sixteen palette entries, and palLatch the first byte of
	// the two a palette write takes.
	pal      [16]color.RGBA
	palLatch byte
	palHalf  bool
	// A transfer between the processor and video memory, set up by LMMC,
	// HMMC or LMCM and moved on a byte at a time. See vdpcmd.go.
	xfer      int
	xferWhole bool
	xferX0    int
	xferX     int
	xferY     int
	xferI     int
	xferJ     int
	xferNX    int
	xferNY    int
	xferSX    int
	xferSY    int
	xferOp    byte

	// What the command engine leaves for status registers 7, 8 and 9:
	// the colour POINT read, and where SRCH stopped.
	stat7       byte
	stat8       byte
	stat9       byte
	borderFound bool

	// fh is status register 1's line-interrupt flag.
	fh        bool
	latched   bool
	first     byte
	status    byte
	readAhead byte

	// Frame is bumped once per interrupt so the status register can report
	// the vertical blank the ROM polls for.
	Frame uint64

	// BootVblank makes every status read report a vertical blank, which is
	// only right while the machine is booting. See ReadStatus.
	BootVblank bool

	// OnReg, when set, is called for every register write with the register
	// number the cartridge actually named -- which on a V9938 goes up to
	// 46, and which this chip does not have.
	OnReg func(reg, v byte)

	// OnStatus, when set, is called with the status register each read asks
	// for.
	OnStatus func(n byte)

	// ReArm, when set, is called after a write to register 19 or 23, so
	// the machine can re-aim the line interrupt. See rearmLine.
	ReArm func()

	// RegLine, when set, supplies the raster line a register write lands
	// on, so a frame's writes can be replayed by the renderer. A game that
	// splits its screen -- a HUD held still over a scrolling playfield --
	// writes the scroll registers twice per frame, at two raster
	// positions, and a renderer that sees only the final values draws the
	// playfield's scroll over the HUD's rows: the top of the screen churns
	// while the rest moves smoothly, which reads as garbage in motion.
	RegLine func() int

	// Cycles reports the machine's clock, for timing how long the command
	// engine holds its busy flag. Nil while booting or in a machine with
	// no clock, and then commands finish at once.
	Cycles func() uint64

	// busyUntil is when the command in flight stops reporting busy.
	busyUntil uint64

	// SplitLog is this frame's register writes, by raster line, oldest
	// first. StartFrame clears it.
	SplitLog []RegEvent

	// Status2, when set, supplies the raster-position bits of status
	// register 2 -- VR and HR, vertical and horizontal retrace -- which
	// only a machine with a clock can know. See ReadStatus.
	Status2 func() byte

	// OnCmd, when set, is called for every command the engine is asked to
	// run.
	OnCmd func(cmd byte, sx, sy, dx, dy, nx, ny int, arg byte)

	// OnPal, when set, is called for every byte written to the palette port.
	OnPal func(b byte)

	// OnWrite, when set, is called for every byte that lands in VRAM
	// through the data port. It exists for the comparison tools: the
	// game's own writers go straight to the port rather than through
	// WRTVRM, so a BIOS-level trace sees almost nothing.
	OnWrite func(addr uint16, b byte)
}

func (v *VDP) Reset() {
	if v.VRAM == nil {
		v.VRAM = make([]byte, 0x4000)
	}
	v.addr, v.at, v.latched, v.status = 0, 0, false, 0
	v.pal = Palette
}

// goV9938 turns this into the chip an MSX2 has, the first time a cartridge
// shows that it is expecting one. Nothing has to be configured and no ROM has
// to be recognised: naming a register a TMS9918 does not have is the evidence.
func (v *VDP) goV9938() {
	if v.V9938 {
		return
	}
	v.V9938 = true
	grown := make([]byte, 0x20000)
	copy(grown, v.VRAM)
	v.VRAM = grown
}

// WriteReg sets a register, and does whatever setting it means.
func (v *VDP) WriteReg(r, b byte) {
	if r > 7 {
		v.goV9938()
	}
	if v.OnReg != nil {
		v.OnReg(r, b)
	}
	if int(r) < len(v.Reg) {
		if v.RegLine != nil && splitReg(r) {
			v.SplitLog = append(v.SplitLog, RegEvent{
				Line: v.RegLine(), Reg: r, Old: v.Reg[r], New: b,
			})
		}
		v.Reg[r] = b
	}
	switch r {
	case 0, 19, 23:
		// The line interrupt's compare is relative to the vertical
		// scroll, so a write to either re-aims it -- and register 0
		// carries IE1, which arms the thing at all.
		//
		// Register 0 matters because the compare is continuous on the
		// chip: it does not care when the enable was set, only that
		// the raster reaches the line. Space Manbow's handler turns
		// IE1 *off*, writes the scroll and the line, and turns it back
		// on -- so a machine that only re-aims on the scroll and line
		// writes aims at nothing, having thrown the schedule away
		// while the enable was down. Its third interrupt of the frame
		// never arrived, and with it the transfers that interrupt was
		// there to perform.
		if v.ReArm != nil {
			v.ReArm()
		}
	case cmdCMD:
		// Writing the command register starts the command.
		v.Execute(b)
	case cmdCLR:
		// While a transfer into video memory is running, this is not
		// a register any more: it is the port the bytes arrive
		// through, one per write, until the rectangle is full.
		if v.xfer == xferIn {
			v.XferWrite(b)
		}

	case 14:
		// The high bits of the video memory address. The low fourteen
		// were set by the address write; this replaces the rest.
		v.at = v.at&0x3FFF | int(b&0x07)<<14
	}
}

// At is the full video-memory address the chip is pointing at, register 14's
// bits included.
func (v *VDP) At() int { return v.at }

// mask is how much video memory there is to address.
func (v *VDP) mask() int { return len(v.VRAM) - 1 }

// phys maps a logical video-memory address to the byte that holds it.
// Screens 7 and 8 interleave the two 64K banks -- bit 0 of the address
// picks the bank, so consecutive logical bytes alternate between them and
// the chip can fetch two at once. Everything goes through this mapping
// while those modes are in force: the processor's port, the command
// engine, the display, and the BIOS entries. Breaker leans on it: the
// loader BLOADs its sheets under screen 5's linear addressing and the
// game reads them back through screen 8's interleaved view.
func (v *VDP) phys(a int) int {
	if m := v.Mode(); m == ModeGraphic6 || m == ModeGraphic7 {
		a = a>>1 | (a&1)<<16
	}
	return a & v.mask()
}

// setAddr takes the fourteen bits an address write carries, keeping whatever
// register 14 has put above them.
func (v *VDP) setAddr(a uint16) {
	v.addr = a & 0x3FFF
	v.at = (v.at&^0x3FFF | int(v.addr)) & v.mask()
}

// bump advances the address after a read or a write, carrying into register
// 14's bits the way the chip does.
func (v *VDP) bump() {
	v.at = (v.at + 1) & v.mask()
	v.addr = uint16(v.at) & 0x3FFF
}

// WriteIndirect is port 9Bh: a register write aimed by register 17, which is
// how an MSX2 cartridge usually sets the chip up -- Space Manbow sets almost
// everything this way and writes registers 0 to 7 directly not at all. Bit 7
// of register 17 holds the pointer still; otherwise it walks.
func (v *VDP) WriteIndirect(b byte) {
	v.goV9938()
	r := v.Reg[17] & 0x3F
	v.WriteReg(r, b)
	if v.Reg[17]&0x80 == 0 {
		v.Reg[17] = v.Reg[17]&0x80 | (r+1)&0x3F
	}
}

// WritePalette is port 9Ah. A colour takes two writes: red and blue, then
// green, three bits each.
func (v *VDP) WritePalette(b byte) {
	v.goV9938()
	if v.OnPal != nil {
		v.OnPal(b)
	}
	if !v.palHalf {
		v.palLatch = b
		v.palHalf = true
		return
	}
	v.palHalf = false
	i := v.Reg[16] & 0x0F
	r := v.palLatch >> 4 & 0x07
	bl := v.palLatch & 0x07
	g := b & 0x07
	v.pal[i] = color.RGBA{level(r), level(g), level(bl), 255}
	// Writing a colour steps to the next one, so a whole palette is
	// sixteen pairs of writes and one register.
	v.Reg[16] = (v.Reg[16] + 1) & 0x0F
}

// SetPalette writes one palette entry directly, as the sub-ROM's SETPLT
// does: rb holds red in its high nibble and blue in its low one, g the green,
// three bits each. It is the same arithmetic as WritePalette without the
// two-write handshake, because the caller is a routine and not a port.
func (v *VDP) SetPalette(i, rb, g byte) {
	v.goV9938()
	v.pal[i&0x0F] = color.RGBA{
		level(rb >> 4 & 0x07), level(g & 0x07), level(rb & 0x07), 255,
	}
}

// GetPalette reads one palette entry back as the three-bit levels it was
// written with, which is what the sub-ROM's GETPLT hands back.
func (v *VDP) GetPalette(i byte) (r, g, b byte) {
	c := v.pal[i&0x0F]
	return c.R >> 5, c.G >> 5, c.B >> 5
}

// level spreads three bits of colour over eight.
//
// The multiply has to happen wider than a byte: n*255 overflows for anything
// above one, and every colour then comes out the same dark grey -- which looks
// like a game that has faded out rather than a palette that is wrong.
func level(n byte) byte { return byte(int(n) * 255 / 7) }

// BorderRGBA is the colour of the surround, through whichever palette this
// chip has. On a V9938 that is the one the cartridge programmed: Space Manbow
// sets register 7 to FFh and then programs colour 15 to black, so reading the
// index through the fixed MSX1 table paints a white border around a game that
// should have none.
func (v *VDP) BorderRGBA() color.RGBA {
	if !v.V9938 {
		return BorderColour(v.Reg[:])
	}
	// Screen 8 has no palette anywhere: register 7 is the backdrop's
	// colour byte itself.
	if v.Mode() == ModeGraphic7 {
		return grb332(v.Reg[7])
	}
	n := v.Reg[7] & 0x0F
	if n == 0 {
		return v.pal[0]
	}
	return v.pal[n]
}

// Palette16 is the sixteen colours in force, which on a V9938 the cartridge
// chooses and on a TMS9918 are fixed.
func (v *VDP) Palette16() [16]color.RGBA { return v.pal }

// WriteCtrl handles port 99h: two writes set an address (bit 14 selects a
// write), or set a register when bit 15 of the pair is set.
func (v *VDP) WriteCtrl(b byte) {
	if !v.latched {
		v.first = b
		v.latched = true
		return
	}
	v.latched = false
	switch b & 0xC0 {
	case 0x80: // register write
		v.WriteReg(b&0x3F, v.first)
	case 0x40: // set up for writing VRAM
		v.setAddr(uint16(v.first) | uint16(b&0x3F)<<8)
	default: // set up for reading VRAM
		v.setAddr(uint16(v.first) | uint16(b&0x3F)<<8)
		v.readAhead = v.VRAM[v.phys(v.at)]
		v.bump()
	}
}

func (v *VDP) WriteData(b byte) {
	if v.OnWrite != nil {
		v.OnWrite(v.addr, b)
	}
	v.VRAM[v.phys(v.at)] = b
	v.bump()
	v.latched = false
}

func (v *VDP) ReadData() byte {
	b := v.readAhead
	v.readAhead = v.VRAM[v.phys(v.at)]
	v.bump()
	v.latched = false
	return b
}

// ReadStatus returns the status register register 15 selects, which on a
// TMS9918 is only ever S#0.
//
// A V9938 has ten, and a cartridge that reads one this machine does not model
// gets zero -- which is "nothing has happened", the answer least likely to
// send it somewhere strange. Space Manbow selects a status register 68,000
// times in ten seconds, so this is not a corner.
func (v *VDP) ReadStatus() byte {
	if v.OnStatus != nil {
		v.OnStatus(v.Reg[15] & 0x0F)
	}
	if n := v.Reg[15] & 0x0F; n != 0 {
		v.latched = false
		if n == 1 {
			// S#1 bit 0 is FH, the line-interrupt flag; reading it
			// clears it. An ISR on a machine with line interrupts
			// enabled reads this first, to tell "the raster reached
			// register 19's line" from "the frame ended" -- the two
			// arrive as separate interrupts and carry different work.
			// Space Manbow reads it 45 times a frame and keeps its
			// scrolling in the line branch.
			s := byte(0)
			if v.fh {
				s = 1
				v.fh = false
			}
			return s
		}
		if n == 2 {
			// S#2: bit 7 is the transfer-ready flag, bit 0 the
			// command engine's busy flag, which stands until the
			// command has had the time the chip would have taken --
			// see cmdCycles. Bits
			// 6 and 5 are the vertical and horizontal retrace flags,
			// and those cannot be constants: a split-screen ISR spins
			// on bit 5 waiting for the horizontal blanking to start
			// and then to end -- Space Manbow does, at 42A4h, to time
			// a register change to the raster -- and a flag that
			// never moves spins it forever. The machine installs
			// Status2 to derive them from its clock.
			s := byte(0x80)
			if v.Busy() {
				s |= 0x01
			}
			if v.borderFound {
				s |= 0x10 // BD: SRCH found what it was after
			}
			if v.Status2 != nil {
				s |= v.Status2()
			}
			return s
		}
		switch n {
		case 7:
			// The colour POINT read, or the next byte of a
			// transfer out of video memory.
			if v.xfer == xferOut {
				return v.XferRead()
			}
			return v.stat7
		case 8:
			return v.stat8
		case 9:
			// Only the low bit means anything; the rest read as
			// ones.
			return v.stat9 | 0xFE
		}
		return 0
	}
	s := v.status
	v.status &^= 0x80
	v.latched = false
	// While the machine boots there is no frame clock, and a cartridge's
	// INIT is entitled to wait for a vertical blank -- the real machine
	// always delivers one eventually. Answering "yes, vblank" to every
	// boot-time poll is what "eventually" means here.
	if v.BootVblank {
		s |= 0x80
	}
	return s
}

// StartFrame raises the vertical-blank flag the interrupt handler polls, and
// works out what else status register 0 has to say about the frame that has
// just been displayed.
func (v *VDP) StartFrame() {
	v.status |= 0x80
	v.Frame++
	v.scanSprites()
}

// scanSprites fills in the rest of status register 0: bit 5 when two sprites
// touched, bit 6 and a sprite number when more of them landed on one line
// than the chip can draw.
//
// The chip works these out while it displays, so they describe the frame just
// gone -- which is the sprite table as it stood before this frame's handler
// starts moving things. A game that polls collision and is told "nothing ever
// touches anything" makes decisions no real machine would: a shooter whose
// shots pass through everything, or one that reads the flag to decide whether
// it is still alive.
func (v *VDP) scanSprites() {
	if v.Reg[8]&0x02 != 0 || v.Reg[1]&0x40 == 0 {
		return // sprite plane off, or the display is blanked
	}
	mask := len(v.VRAM) - 1
	// Sprite mode 2 is the V9938's: a colour byte per line, and eight to a
	// line rather than four.
	mode2 := v.V9938
	attr := int(v.Reg[5]&0x7F) << 7
	if mode2 {
		attr = int(v.Reg[5]&0xFC)<<7 | int(v.Reg[11]&0x03)<<15
	}
	patt := int(v.Reg[6]&0x3F) << 11
	colTab := attr - 512
	size, scale := 8, 1
	if v.Reg[1]&0x02 != 0 {
		size = 16
	}
	if v.Reg[1]&0x01 != 0 {
		scale = 2
	}
	lines := v.Lines()
	stop, perLine := byte(208), 4
	if mode2 {
		perLine = 8
		if lines != 192 {
			stop = 216
		}
	}
	// Where each sprite sits, once, rather than re-reading the table for
	// every one of two hundred lines.
	type sprite struct{ y, x, pat, col int }
	var list []sprite
	for i := 0; i < 32; i++ {
		y := int(v.VRAM[(attr+i*4)&mask])
		if byte(y) == stop {
			break
		}
		list = append(list, sprite{
			y:   y + 1,
			x:   int(v.VRAM[(attr+i*4+1)&mask]),
			pat: int(v.VRAM[(attr+i*4+2)&mask]),
			col: int(v.VRAM[(attr+i*4+3)&mask]),
		})
	}
	var row [256]int8
	for y := 0; y < lines; y++ {
		for i := range row {
			row[i] = -1
		}
		on := 0
		for i, s := range list {
			dy := y - s.y
			if dy < 0 || dy >= size*scale {
				continue
			}
			on++
			if on > perLine {
				// More than the chip draws. The number reported
				// is the one that could not be, and the flag
				// stays until the status register is read.
				if v.status&0x40 == 0 {
					v.status |= 0x40
					v.status = v.status&^0x1F | byte(i)&0x1F
				}
				break
			}
			line := dy / scale
			col := s.col & 0x0F
			early := s.col&0x80 != 0
			if mode2 {
				cb := v.VRAM[(colTab+i*16+(line&0x0F))&mask]
				col = int(cb & 0x0F)
				early = cb&0x80 != 0
			}
			if col == 0 {
				continue // transparent, and transparent does not touch
			}
			x0 := s.x
			if early {
				x0 -= 32
			}
			pat := s.pat
			if size == 16 {
				pat &= 0xFC
			}
			for dx := 0; dx < size*scale; dx++ {
				cx := dx / scale
				at := patt + pat*8 + line
				if size == 16 && cx >= 8 {
					at += 16
				}
				if v.VRAM[at&mask]&(0x80>>uint(cx%8)) == 0 {
					continue
				}
				px := x0 + dx
				if px < 0 || px >= 256 {
					continue
				}
				if row[px] >= 0 {
					v.status |= 0x20
				}
				row[px] = int8(i)
			}
		}
	}
}

// StartLog begins a fresh frame's register log. It is separate from
// StartFrame because the vblank flag also rises from the clock, mid-frame,
// for a handler polling across the boundary -- and that must not wipe the
// log the renderer is about to read.
func (v *VDP) StartLog() { v.SplitLog = v.SplitLog[:0] }

// StartLine raises the line-interrupt flag: the raster has reached the line
// register 19 names. On the hardware this is a second interrupt in every
// frame, distinct from the vertical blank and usually carrying different work.
func (v *VDP) StartLine() { v.fh = true }

// FHPending reports the line-interrupt flag without consuming it.
func (v *VDP) FHPending() bool { return v.fh }

// AckVblank clears the vertical-blank flag, as the BIOS' own interrupt
// handler does by reading status register 0.
func (v *VDP) AckVblank() { v.status &^= 0x80 }

// FPending reports the vertical-blank flag without consuming it.
func (v *VDP) FPending() bool { return v.status&0x80 != 0 }

// LineIRQEnabled reports IE1, register 0 bit 4: whether the cartridge asked
// for that second interrupt at all.
func (v *VDP) LineIRQEnabled() bool { return v.Reg[0]&0x10 != 0 }

// SetAddrFull points the chip at a full video-memory address, register
// 14's bits included, the way the MSX2's NSETRD and NSTWRT do: the real
// BIOS folds the address's top bits and the active page into register 14
// before writing the fourteen-bit remainder. SetAddr below keeps the old
// register 14 bits, which is what the sixteen-K SETRD and SETWRT do --
// using it for the new entries silently pinned every access to whatever
// bank the chip was left in.
func (v *VDP) SetAddrFull(a int, write bool) {
	v.at = a & v.mask()
	v.addr = uint16(v.at) & 0x3FFF
	v.latched = false
	if !write {
		v.readAhead = v.VRAM[v.phys(v.at)]
		v.bump()
	}
}

// SetAddr points the VDP at a VRAM address for writing, as SETWRT does.
func (v *VDP) SetAddr(a uint16, write bool) {
	v.setAddr(a)
	v.latched = false
	if !write {
		v.readAhead = v.VRAM[v.phys(v.at)]
		v.bump()
	}
}

// ScreenText reads the name table back as text.
//
// It is a debugging aid and it is honest about being a crude one: a cartridge
// chooses its own character codes, and King's Valley's font puts '0' at 10h
// and 'A' at 21h, so a generic reader can only show the codes that happen to
// be ASCII and mark the rest. It is still the quickest way to wait for a
// prompt instead of guessing frame counts.
func ScreenText(m *M) string {
	base := int(m.VDP.Reg[2]&0x0F) << 10
	b := make([]byte, 0, 24*33)
	for row := 0; row < 24; row++ {
		for col := 0; col < 32; col++ {
			c := m.VDP.VRAM[(base+row*32+col)&0x3FFF]
			if c >= 0x20 && c < 0x7F {
				b = append(b, c)
			} else if c == 0 {
				b = append(b, ' ')
			} else {
				b = append(b, '.')
			}
		}
		b = append(b, '\n')
	}
	return string(b)
}

// Mode is which screen mode the registers describe. The bits are spread across
// two registers and were never renumbered as the chip grew, so they are pulled
// out here once rather than decoded at each place that cares.
type ScreenMode int

const (
	ModeGraphic1 ScreenMode = iota // SCREEN 1
	ModeGraphic2                   // SCREEN 2
	ModeGraphic3                   // SCREEN 4
	ModeGraphic4                   // SCREEN 5: 256x212, four bits a pixel
	ModeGraphic5                   // SCREEN 6
	ModeGraphic6                   // SCREEN 7
	ModeGraphic7                   // SCREEN 8
	ModeText
	ModeMulticolour
	ModeUnknown
)

// Mode reads M1 to M5 out of registers 0 and 1.
func (v *VDP) Mode() ScreenMode {
	m3 := v.Reg[0]&0x02 != 0
	m4 := v.Reg[0]&0x04 != 0
	m5 := v.Reg[0]&0x08 != 0
	m1 := v.Reg[1]&0x10 != 0
	m2 := v.Reg[1]&0x08 != 0
	switch {
	case m1:
		return ModeText
	case m2:
		return ModeMulticolour
	case m5 && m4 && m3:
		return ModeGraphic7
	case m5 && m3:
		return ModeGraphic6
	case m5:
		return ModeGraphic5
	case m4 && m3:
		return ModeGraphic4
	case m4:
		return ModeGraphic3
	case m3:
		return ModeGraphic2
	}
	return ModeGraphic1
}

// Bitmap reports whether the mode is one of the V9938's packed-pixel modes,
// which the tile renderer knows nothing about.
func (v *VDP) Bitmap() bool {
	m := v.Mode()
	return m >= ModeGraphic4 && m <= ModeGraphic7
}

// Lines is how tall the display is: 192, or 212 when register 9 asks.
func (v *VDP) Lines() int {
	if v.Reg[9]&0x80 != 0 {
		return 212
	}
	return 192
}

// PageBase is where the visible bitmap starts, from register 2.
func (v *VDP) PageBase() int {
	switch v.Mode() {
	case ModeGraphic6, ModeGraphic7:
		return int(v.Reg[2]&0x20) << 11
	}
	return int(v.Reg[2]&0x60) << 10
}

// RegEvent is one register write, placed on the raster.
type RegEvent struct {
	Line     int
	Reg      byte
	Old, New byte
}

// splitReg says which registers are worth logging by line: the ones whose
// mid-frame writes change what a scanline shows -- which part of memory it
// comes from, and, through registers 0 and 1, which mode reads it. Space
// Manbow draws its status panel in SCREEN 5 over a SCREEN 4 playfield, so
// leaving the mode bits out of this list costs a whole band of the picture.
func splitReg(r byte) bool {
	switch r {
	case 0, 1, 2, 3, 4, 5, 6, 7, 8, 10, 11, 18, 23:
		return true
	}
	return false
}

// RegsAt reconstructs the register file as it stood when the raster reached
// the given line: the current registers with this frame's later writes undone,
// and its earlier ones applied. The log carries old values for exactly this.
func (v *VDP) RegsAt(line int) [64]byte {
	regs := v.Reg
	for i := len(v.SplitLog) - 1; i >= 0; i-- {
		e := v.SplitLog[i]
		if e.Line > line {
			regs[e.Reg] = e.Old
		}
	}
	return regs
}
