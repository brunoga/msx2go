package z80

// The V9938's command engine.
//
// Registers 32 to 46 describe a rectangle and what to do with it, and writing
// register 46 starts it. An MSX2 game does most of its drawing this way -- it
// is the only way to move a screenful of pixels at any speed on a 3.58 MHz
// Z80 -- and a machine without one shows nothing at all. Space Manbow writes
// this block a hundred times in ten seconds and writes registers 0 to 7
// directly not once.
//
// The commands run to completion the moment they are started, rather than over
// the cycles the real chip takes. That is the same bargain the rest of this
// machine makes about time, and it shows up the same way: a game that waits
// for the command to finish sees it already finished, which is early but never
// wrong. See ReadStatus, which answers "idle and ready" for status register 2.

// A transfer between the processor and video memory runs over many writes or
// reads rather than all at once: the command sets it up, and each byte
// through register 44 -- or out through status register 7 -- moves it on.
const (
	xferNone = iota
	xferIn   // LMMC, HMMC: the processor is writing
	xferOut  // LMCM: the processor is reading
)

// startXfer sets up one of those transfers.
func (v *VDP) startXfer(dir int, whole bool, x, y, nx, ny, stepX, stepY int, op byte) {
	v.xfer, v.xferWhole = dir, whole
	v.xferX0, v.xferY = x, y
	v.xferX, v.xferI, v.xferJ = x, 0, 0
	v.xferNX, v.xferNY = nx, ny
	v.xferSX, v.xferSY = stepX, stepY
	v.xferOp = op
}

// xferStep moves a transfer on by one unit, and reports whether that was the
// last one.
func (v *VDP) xferStep() bool {
	v.xferI++
	v.xferX += v.xferSX
	if v.xferI < v.xferNX {
		return false
	}
	v.xferI, v.xferX = 0, v.xferX0
	v.xferJ++
	v.xferY += v.xferSY
	if v.xferJ < v.xferNY {
		return false
	}
	v.xfer = xferNone
	return true
}

// XferWrite is a byte the processor has written to register 44 while a
// transfer into video memory is running.
// XferActive reports whether a transfer into video memory is still asking
// for bytes.
func (v *VDP) XferActive() bool { return v.xfer == xferIn }

func (v *VDP) XferWrite(b byte) {
	if v.xferWhole {
		bpl := v.bytesPerLine()
		v.VRAM[v.phys(v.xferY*bpl+(v.xferX&(bpl-1)))] = b
	} else {
		v.setPixel(v.xferX, v.xferY, b, v.xferOp)
	}
	v.xferStep()
}

// firstXferByte hands the transfer the byte register 44 is already holding.
//
// The documented way to start a transfer into video memory is to put the
// first byte in register 44 and *then* write the command: the chip takes
// what the register holds as byte zero, and only asks for the next one
// through TR. A machine that waits for the next write instead is a byte
// behind for the whole rectangle -- every byte lands one position early,
// which is not noise but a picture shifted, and Snatcher loads its logo and
// its whole font strip through two of these.
func (v *VDP) firstXferByte() { v.XferWrite(v.Reg[cmdCLR]) }

// XferRead is the next byte of a transfer out of video memory.
func (v *VDP) XferRead() byte {
	var b byte
	if v.xferWhole {
		bpl := v.bytesPerLine()
		b = v.VRAM[v.phys(v.xferY*bpl+(v.xferX&(bpl-1)))]
	} else {
		b = v.getPixel(v.xferX, v.xferY)
	}
	v.xferStep()
	return b
}

// The command block, by register number.
const (
	cmdSX  = 32
	cmdSY  = 34
	cmdDX  = 36
	cmdDY  = 38
	cmdNX  = 40
	cmdNY  = 42
	cmdCLR = 44
	cmdARG = 45
	cmdCMD = 46
)

// Bits of the argument register: which way the rectangle grows, and which
// half of a 128K memory the source and destination live in.
const (
	argMXD = 0x01
	argMXS = 0x02
	argDIY = 0x04
	argDIX = 0x08
)

// pixelsPerByte and bytesPerLine describe how the bitmap modes pack pixels.
// Graphic 4 is two pixels to a byte over 128 bytes a line; Graphic 5 and 6
// four; Graphic 7 one over 256.
func (v *VDP) pixelsPerByte() int {
	switch v.Mode() {
	case ModeGraphic5:
		// SCREEN 6: two bits a pixel, four to the byte.
		return 4
	case ModeGraphic7:
		// SCREEN 8: the byte is the colour.
		return 1
	}
	// SCREEN 5 and SCREEN 7 both carry four bits a pixel. They differ in
	// how many bytes a line holds, not in how a byte is cut, and counting
	// SCREEN 7 as four pixels a byte put every coordinate the command
	// engine computed at half the address it wanted and read two-bit
	// pixels out of four-bit ones.
	return 2
}

func (v *VDP) bytesPerLine() int {
	switch v.Mode() {
	case ModeGraphic6, ModeGraphic7:
		return 256
	}
	return 128
}

// dotsPerLine is how wide the picture is in dots: 256 in Graphic 4 and 7, 512
// in the wide modes.
func (v *VDP) dotsPerLine() int { return v.bytesPerLine() * v.pixelsPerByte() }

// pixelAt is where a pixel lives: the byte holding it, and how far to shift a
// value to land on it.
//
// X wraps within the line. The coordinate registers are nine bits and the
// picture is only 256 dots wide in Graphic 4, so a copy that runs off the right
// edge comes back on the left -- it does not spill into the line below, which
// is what an unmasked address does and what turns a tidy blit into a smear one
// line down.
func (v *VDP) pixelAt(x, y int) (int, uint, byte) {
	ppb := v.pixelsPerByte()
	bpl := v.bytesPerLine()
	x &= v.dotsPerLine() - 1
	bits := uint(8 / ppb)
	addr := v.phys(y*bpl + x/ppb)
	// The leftmost pixel of a byte lives in its high bits.
	shift := uint(ppb-1-(x%ppb)) * bits
	return addr, shift, byte((1<<bits)-1) << shift
}

func (v *VDP) getPixel(x, y int) byte {
	a, sh, mask := v.pixelAt(x, y)
	return (v.VRAM[a] & mask) >> sh
}

func (v *VDP) setPixel(x, y int, c byte, op byte) {
	a, sh, mask := v.pixelAt(x, y)
	old := (v.VRAM[a] & mask) >> sh
	n := v.logic(old, c, op)
	v.VRAM[a] = v.VRAM[a]&^mask | (n<<sh)&mask
}

// logic is the operation the command register's low nibble names. The high bit
// of it means "leave the destination alone where the source is colour zero",
// which is how a sprite-shaped blit keeps its background.
func (v *VDP) logic(dst, src, op byte) byte {
	if op&0x08 != 0 && src == 0 {
		return dst
	}
	switch op & 0x07 {
	case 1:
		return dst & src
	case 2:
		return dst | src
	case 3:
		return dst ^ src
	case 4:
		return ^src
	}
	return src
}

// reg16 reads a pair of command registers as a number.
func (v *VDP) reg16(r int) int {
	return int(v.Reg[r]) | int(v.Reg[r+1])<<8
}

// Execute runs whatever writing register 46 asked for.
func (v *VDP) Execute(cmd byte) {

	op := cmd & 0x0F
	arg := v.Reg[cmdARG]
	sx, sy := v.reg16(cmdSX)&0x1FF, v.reg16(cmdSY)&0x3FF
	dx, dy := v.reg16(cmdDX)&0x1FF, v.reg16(cmdDY)&0x3FF
	// NX is nine bits and NY is ten. Taking a bit too many of either reads
	// whatever the cartridge left in the high byte as width, and a copy
	// four times too wide smears one part of the screen across the rest.
	nx, ny := v.reg16(cmdNX)&0x1FF, v.reg16(cmdNY)&0x3FF
	if nx == 0 {
		nx = 512
	}
	if ny == 0 {
		ny = 1024
	}
	stepX, stepY := 1, 1
	if arg&argDIX != 0 {
		stepX = -1
	}
	if arg&argDIY != 0 {
		stepY = -1
	}
	clr := v.Reg[cmdCLR]
	if v.OnCmd != nil {
		v.OnCmd(cmd, sx, sy, dx, dy, nx, ny, arg)
	}

	// The high-speed commands move whole bytes, but they are still given
	// their coordinates in dots like everything else -- so the byte range
	// is the dot range divided by however many dots a byte holds. Taking
	// the dots for bytes makes a full-width fill twice as wide as the line
	// it is filling, and it runs on into the next one; a screen wiped that
	// way comes out as noise a few thousand frames later.
	bpl := v.bytesPerLine()
	ppb := v.pixelsPerByte()
	byteAt := func(x, y int) int { return v.phys(y*bpl + (x & (bpl - 1))) }
	sxB, dxB, nxB := sx/ppb, dx/ppb, nx/ppb
	if nxB == 0 {
		nxB = 1
	}

	switch cmd >> 4 {
	case 0x8: // LMMV: fill a rectangle, a pixel at a time
		for j := 0; j < ny; j++ {
			for i := 0; i < nx; i++ {
				v.setPixel(dx+i*stepX, dy+j*stepY, clr, op)
			}
		}
	case 0x9: // LMMM: copy a rectangle, a pixel at a time
		for j := 0; j < ny; j++ {
			for i := 0; i < nx; i++ {
				c := v.getPixel(sx+i*stepX, sy+j*stepY)
				v.setPixel(dx+i*stepX, dy+j*stepY, c, op)
			}
		}
	case 0xC: // HMMV: fill whole bytes
		for j := 0; j < ny; j++ {
			for i := 0; i < nxB; i++ {
				v.VRAM[byteAt(dxB+i*stepX, dy+j*stepY)] = clr
			}
		}
	case 0xD: // HMMM: copy whole bytes
		for j := 0; j < ny; j++ {
			for i := 0; i < nxB; i++ {
				v.VRAM[byteAt(dxB+i*stepX, dy+j*stepY)] =
					v.VRAM[byteAt(sxB+i*stepX, sy+j*stepY)]
			}
		}
	case 0xE: // YMMM: copy whole bytes, moving only in Y
		for j := 0; j < ny; j++ {
			for i := 0; i < nxB; i++ {
				v.VRAM[byteAt(dxB+i*stepX, dy+j*stepY)] =
					v.VRAM[byteAt(dxB+i*stepX, sy+j*stepY)]
			}
		}
	case 0x5: // PSET
		v.setPixel(dx, dy, clr, op)
	case 0x7: // LINE
		v.line(dx, dy, nx, ny, clr, op, arg)
	case 0x4: // POINT: the colour read back through status register 7
		v.stat7 = v.getPixel(sx, sy)
		v.Reg[cmdCLR] = v.stat7

	case 0x0: // STOP: abandon whatever was running
		v.xfer = xferNone

	case 0x6: // SRCH: walk a line looking for a colour
		// The border-detect bit says whether it found one; status
		// registers 8 and 9 say where. A game uses this to find the
		// edge of a filled shape without reading every pixel over the
		// bus.
		want := clr
		equal := arg&argMXD == 0
		x, found := sx, false
		for x >= 0 && x < v.dotsPerLine() {
			if (v.getPixel(x, sy) == want) == equal {
				found = true
				break
			}
			x += stepX
		}
		v.stat8, v.stat9 = byte(x), byte(x>>8)&0x01
		v.borderFound = found

	case 0xA: // LMCM: hand a rectangle to the processor, a pixel at a time
		v.startXfer(xferOut, false, sx, sy, nx, ny, stepX, stepY, op)

	case 0xB: // LMMC: take a rectangle from the processor, a pixel at a time
		v.startXfer(xferIn, false, dx, dy, nx, ny, stepX, stepY, op)
		v.firstXferByte()

	case 0xF: // HMMC: take a rectangle from the processor, whole bytes
		v.startXfer(xferIn, true, dxB, dy, nxB, ny, stepX, stepY, op)
		v.firstXferByte()
	}
	if v.xfer != xferNone {
		// A transfer runs at the processor's pace, not the chip's:
		// it is finished when the last byte has been handed over, and
		// saying "busy" now would stall the very loop that feeds it.
		v.Reg[cmdCMD] = 0
		return
	}
	// The memory is changed all at once, but the chip is not finished:
	// the busy flag stands for as long as the transfer would have taken.
	// A game that polls it -- Space Manbow's intro polls it ten thousand
	// times -- otherwise runs its animation as fast as the host can copy
	// memory, which put the intro through in a third of its running time.
	v.busy(cmd>>4, nx, ny, nxB)

	// The command register reads back with the command bits cleared,
	// which is what a game polling *that* waits for.
	v.Reg[cmdCMD] = 0
}

// cyclesPerAccess is what one of the command engine's reads or writes of
// video memory costs, in Z80 T-states, with the display on.
//
// Measured on the reference machine by timing its busy flag from the write
// of the command register to the fall of S#2 bit 0: a byte fill (HMMV) of
// 32,640 bytes takes 8.24 cycles a byte, a byte copy (HMMM) of 3,072 takes
// 16.16, and a pixel copy (LMMM) of 64 pixels takes 23.4 a pixel. Those are
// one, two and three memory accesses per unit -- a fill writes, a copy reads
// and writes, and a pixel copy reads the source byte, reads the destination
// and writes it back -- so all three land on the same 8.2 cycles an access.
const cyclesPerAccess = 8.2

// busy sets how long the command engine reports itself busy. The cost is
// the number of memory accesses the command needs: the logical commands
// work a pixel at a time and pay per pixel, the high-speed ones move whole
// bytes and pay per byte.
func (v *VDP) busy(kind byte, nx, ny, nxB int) {
	if v.Cycles == nil {
		return
	}
	var accesses float64
	switch kind {
	case 0x8: // LMMV: read the destination byte, write it back
		accesses = 2 * float64(nx) * float64(ny)
	case 0x9: // LMMM: and read the source as well
		accesses = 3 * float64(nx) * float64(ny)
	case 0xC: // HMMV: write the byte
		accesses = float64(nxB) * float64(ny)
	case 0xD, 0xE: // HMMM, YMMM: read it and write it
		accesses = 2 * float64(nxB) * float64(ny)
	case 0x5: // PSET
		accesses = 2
	case 0x7: // LINE: two accesses a pixel along the long axis
		accesses = 2 * float64(nx)
	case 0x4: // POINT
		accesses = 1
	default:
		return
	}
	v.busyUntil = v.Cycles() + uint64(accesses*cyclesPerAccess)
}

// Busy reports whether the command engine is still working: a command
// still costing its accesses, or a transfer through the processor that
// has not had all its bytes yet. Status register 2's low bit is this,
// and a program that starts a command waits on it before starting
// another.
func (v *VDP) Busy() bool {
	// A transfer through the processor counts: the command is running
	// until the last byte has been handed over. That is not pedantry
	// -- a program feeding one watches this bit to know when to stop,
	// and a chip that says "finished" straight away is told to stop
	// before it has sent a single byte, which draws nothing at all.
	if v.xfer != xferNone {
		return true
	}
	return v.Cycles != nil && v.Cycles() < v.busyUntil
}

// TransferReady reports whether the command engine wants the next byte,
// which is status register 2's top bit. A transfer here moves a byte the
// moment it is given one, so the answer is simply whether a transfer is
// running -- and a program feeding one waits on this bit between bytes,
// which is why a chip that never sets it is a program that never
// finishes its first blit.
func (v *VDP) TransferReady() bool { return v.xfer != xferNone }

// line draws the V9938's line, which is a Bresenham walk with the long axis
// chosen by the argument register's MAJ bit.
func (v *VDP) line(x, y, long, short int, clr, op, arg byte) {
	if long == 0 {
		return
	}
	stepX, stepY := 1, 1
	if arg&argDIX != 0 {
		stepX = -1
	}
	if arg&argDIY != 0 {
		stepY = -1
	}
	e := 0
	for i := 0; i <= long; i++ {
		v.setPixel(x, y, clr, op)
		e += short
		if e >= long {
			e -= long
			if arg&0x01 != 0 {
				x += stepX
			} else {
				y += stepY
			}
		}
		if arg&0x01 != 0 {
			y += stepY
		} else {
			x += stepX
		}
	}
}
