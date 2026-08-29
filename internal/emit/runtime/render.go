// Rasterising TMS9918A video memory into an image.
//
// There is no dependency on any graphics library here on purpose: rendering
// is a pure function of (VRAM, registers), so it can be tested headless and
// diffed against a reference without a graphics context. A window is just
// something that blits what this produces.
//
// The table bases are decoded from the registers the way the chip does rather
// than assumed, because cartridges reprogram them: King's Valley runs
// SCREEN 2 with R3=7Fh putting the colour table at 0000h and R4=07h the
// pattern generator at 2000h, the reverse of the usual arrangement.
//
// Graphics 2 only, which is what an MSX1 game uses. The text and multicolour
// modes are not drawn, and neither is anything the V9938 adds -- that is a
// milestone of its own and it is a large one.
package z80

import (
	"image"
	"image/color"
)

// Screen dimensions of the active display area.
const (
	ScreenWidth = 256
	// ScreenWidthWide is what the V9938's two wide modes put on a line.
	// SCREEN 6 and SCREEN 7 are 512 dots across, and a picture taken from
	// them 256 wide is the left half of the screen: Snatcher's menu drawn
	// that way is legible glyphs in the wrong places, which reads like a
	// blitter fault rather than a missing mode.
	ScreenWidthWide = 512
	ScreenHeight    = 192
	// ScreenHeightTall is what a V9938 shows when register 9 asks for 212.
	ScreenHeightTall = 212

	// The raster a TMS9918 actually draws, with the 256x192 image centred
	// in it and the rest painted in the backdrop colour.
	//
	// The image alone is already 4:3 and its pixels are already square, so
	// there is no aspect to correct -- what a television shows that this
	// does not is the border, which is not decoration: games set register 7
	// deliberately and some flash it.
	//
	// The renderer keeps producing the image alone, because every
	// comparison against a reference machine is of video memory rendered at
	// 256x192 and it would be a poor trade to complicate that. A harness
	// that wants the border draws it around the image. See BorderColour.
	FrameWidth  = 320
	FrameHeight = 240
	BorderLeft  = (FrameWidth - ScreenWidth) / 2
	BorderTop   = (FrameHeight - ScreenHeight) / 2
)

// Palette is the TMS9918A's fixed 16 colours. Entry 0 is transparent and is
// never drawn; it is present so indices line up.
var Palette = [16]color.RGBA{
	{0, 0, 0, 0},         // 0 transparent
	{0, 0, 0, 255},       // 1 black
	{62, 184, 73, 255},   // 2 medium green
	{116, 208, 125, 255}, // 3 light green
	{89, 85, 224, 255},   // 4 dark blue
	{128, 118, 241, 255}, // 5 light blue
	{185, 94, 81, 255},   // 6 dark red
	{101, 219, 239, 255}, // 7 cyan
	{219, 101, 89, 255},  // 8 medium red
	{255, 137, 125, 255}, // 9 light red
	{204, 195, 94, 255},  // 10 dark yellow
	{222, 208, 135, 255}, // 11 light yellow
	{58, 162, 65, 255},   // 12 dark green
	{183, 102, 181, 255}, // 13 magenta
	{204, 204, 204, 255}, // 14 grey
	{255, 255, 255, 255}, // 15 white
}

// Options controls behaviour that a faithful port and a comfortable one
// disagree about.
type RenderOptions struct {
	// SpriteLimit reproduces the TMS9918A's four-sprites-per-scanline rule.
	// The original flickers when more overlap and Konami leaned on it, so
	// this is authentic -- but it is also the thing modern players notice
	// first. Off by default.
	SpriteLimit bool
}

// Renderer holds the output buffer so frames do not allocate.
type Renderer struct {
	// tall is the buffer for a 212-line screen, wide and wideTall the
	// same two for a 512-dot mode, and cur the one in use. They are kept
	// apart rather than one buffer resized because every comparison in
	// this project is against an image of a fixed size, and a renderer
	// that reallocated would move them all.
	tall     *image.RGBA
	wide     *image.RGBA
	wideTall *image.RGBA
	cur      *image.RGBA
	lines    int
	width    int

	RenderOptions
	img *image.RGBA

	// perLine counts sprites already drawn on each scanline, for SpriteLimit.
	perLine [ScreenHeightTall]int
}

func NewRenderer() *Renderer {
	return &Renderer{
		img:      image.NewRGBA(image.Rect(0, 0, ScreenWidth, ScreenHeight)),
		tall:     image.NewRGBA(image.Rect(0, 0, ScreenWidth, ScreenHeightTall)),
		wide:     image.NewRGBA(image.Rect(0, 0, ScreenWidthWide, ScreenHeight)),
		wideTall: image.NewRGBA(image.Rect(0, 0, ScreenWidthWide, ScreenHeightTall)),
	}
}

// Lines is how tall the last rendered image is.
func (r *Renderer) Lines() int {
	if r.lines == 0 {
		return ScreenHeight
	}
	return r.lines
}

// use points the renderer at the buffer for a screen of n lines. A V9938 asked
// for 212 shows 212, and drawing those into a 192-line buffer loses the bottom
// twenty rows -- in Space Manbow's first stage, the whole rock floor.
//
// The 192-line buffer is kept separate and returned unchanged for anything
// that asks for 192, because every MSX1 comparison in this project is of a
// 256x192 image and none of them should move.
func (r *Renderer) use(lines, dots int) {
	tall := lines > ScreenHeight
	r.lines, r.width = ScreenHeight, ScreenWidth
	if tall {
		r.lines = ScreenHeightTall
	}
	if dots > ScreenWidth {
		r.width = ScreenWidthWide
		r.cur = r.wide
		if tall {
			r.cur = r.wideTall
		}
		return
	}
	r.cur = r.img
	if tall {
		r.cur = r.tall
	}
}

// Width is how wide the last rendered image is: 512 in the modes that are
// 512 dots across, 256 in every other.
func (r *Renderer) Width() int {
	if r.width == 0 {
		return ScreenWidth
	}
	return r.width
}

// Layout describes where the VDP tables live, decoded from the registers.
type VDPLayout struct {
	Name, Pattern, Colour, SpritePattern, SpriteAttr int
	Backdrop                                         byte
	Sprites16                                        bool
	Magnified                                        bool
	DisplayOn                                        bool
}

// DecodeLayout reads the table bases out of the eight VDP registers, applying
// the Graphics 2 masking rules.
func DecodeLayout(reg []byte) VDPLayout {
	return VDPLayout{
		Name: int(reg[2]&0x0F) << 10,
		// In Graphics 2 only bit 2 of R4 and bit 7 of R3 select the base;
		// the remaining bits must be set and act as a mask, which this game
		// leaves fully open (R3=7Fh).
		Pattern:       int(reg[4]&0x04) << 11,
		Colour:        int(reg[3]&0x80) << 6,
		SpritePattern: int(reg[6]&0x07) << 11,
		SpriteAttr:    int(reg[5]&0x7F) << 7,
		Backdrop:      reg[7] & 0x0F,
		Sprites16:     reg[1]&0x02 != 0,
		Magnified:     reg[1]&0x01 != 0,
		DisplayOn:     reg[1]&0x40 != 0,
	}
}

// BorderColour is what register 7's low nibble paints the surround, and the
// backdrop the transparent colour shows through to. Colour 0 is transparent
// and there is nothing behind it, so it reads as black.
//
// This is the fixed-palette form, for a TMS9918. A V9938's sixteen colours are
// whatever the cartridge programmed them to be -- see VDP.BorderRGBA, which is
// what a harness should ask.
func BorderColour(reg []byte) color.RGBA {
	n := reg[7] & 0x0F
	if n == 0 {
		return Palette[1]
	}
	return Palette[n]
}

// RenderVDP draws one frame. On a V9938 the picture is built a scanline at a
// time from the registers in force when the raster reached that line, because
// the registers do not hold still for a frame: Space Manbow's status panel is
// a SCREEN 5 bitmap sitting above a SCREEN 4 playfield, the two swapped by a
// line interrupt twenty-nine lines down. Drawing a whole frame in the mode
// that happened to be set last shows one of the two bands as noise.
//
// An MSX1 machine never comes through here: its path is the one every King's
// Valley and Salamander comparison was verified against, and it stays alone.
func (r *Renderer) RenderVDP(v *VDP) *image.RGBA {
	if !v.V9938 {
		return r.Render(v.VRAM, v.Reg[:])
	}
	return r.scanlines(v)
}

// scanlines replays this frame's register writes down the raster, drawing
// every line in whatever mode, page, scroll and sprite table its own
// registers select.
// grb332 decodes screen 8's direct colour byte: three bits of green,
// three of red, two of blue.
func grb332(b byte) color.RGBA {
	return color.RGBA{
		R: byte(int(b>>2&7) * 255 / 7),
		G: byte(int(b>>5&7) * 255 / 7),
		B: byte(int(b&3) * 255 / 3),
		A: 255,
	}
}

func (r *Renderer) scanlines(v *VDP) *image.RGBA {
	saved := v.Reg
	defer func() { v.Reg = saved }()

	lines := v.Lines()
	r.use(lines, v.dotsPerLine())

	// The log is in time order and its line numbers wrap at 262, so a frame
	// that ran longer than one frame's worth of raster -- ordinary for a
	// game whose handler is its main loop -- logs several passes down the
	// screen and the numbers climb, wrap, and climb again. The replay below
	// walks the log once and assumes they only climb.
	//
	// A wrap therefore made it apply one pass's writes at another pass's
	// scanlines. Snatcher blanks the display partway down a pass and turns
	// it back on during the next pass's blanking, which logs as a line
	// *before* the one that blanked it; the replay never reached the
	// re-enable and painted every line below in the backdrop. At a
	// transition that is a white band across the bottom of the screen, on
	// every transition the game makes.
	//
	// There is no honest way to draw such a frame as one picture: what the
	// hardware showed while it ran was several, and the last of them is
	// usually half-finished, its raster having stopped partway down. So a
	// wrapped log is not replayed at all -- the picture is drawn in the
	// registers the frame ended with, which is the one state that was
	// certainly real. Replaying only the final pass was tried first and
	// draws a different phantom: the pass begins at the line the raster had
	// reached, so everything above it is painted in whatever state preceded
	// it, which for a blanked display is a black strip down to that line.
	//
	// A frame that did not overrun keeps the scanline replay exactly as it
	// was, which is every game in the battery: their logs never wrap.
	wrapped := false
	for i := 1; i < len(v.SplitLog); i++ {
		if v.SplitLog[i].Line < v.SplitLog[i-1].Line {
			wrapped = true
			break
		}
	}
	regs := v.Reg
	next := len(v.SplitLog)
	if !wrapped {
		regs = v.RegsAt(0)
		next = 0
	}
	displayOn := regs[1]&0x40 != 0
	for y := 0; y < r.lines; y++ {
		for next < len(v.SplitLog) && v.SplitLog[next].Line <= y {
			e := v.SplitLog[next]
			regs[e.Reg] = e.New
			next++
		}
		v.Reg = regs
		pal := v.Palette16()
		back := pal[regs[7]&0x0F]
		if v.Mode() == ModeGraphic7 {
			// The backdrop byte is its own colour on screen 8.
			back = grb332(regs[7])
		}
		// Register 1 bit 6 blanks the display, and it is read once for
		// the frame rather than followed down the raster.
		//
		// Following it was measured wrong. A game that writes video
		// memory with the display off drops the bit and puts it back a
		// few lines later -- Snatcher does it while the raster is in
		// the picture, at display line 8, and back at 14 -- and a
		// replay that honoured that painted those lines in the
		// backdrop: a black bar across the top of every screen it drew.
		// The reference machine shows nothing there. Its picture on
		// that screen is one flat blue from the first line to the last,
		// measured row by row, while its own register log has the bit
		// dropped at line 8. So the chip does not blank the lines a
		// mid-frame write covers, and neither does this.
		if y >= lines || !displayOn {
			for x := 0; x < r.width; x++ {
				r.set(x, y, back)
			}
			continue
		}
		// Register 8 bit 5 is TP. With it clear, colour 0 is
		// transparent and shows the backdrop; with it set, colour 0
		// is palette entry 0 like any other colour. Space Manbow sets
		// it, and its playfield's zeroes are a deep blue, not the
		// black the backdrop happens to be.
		zero := back
		if regs[8]&0x20 != 0 {
			zero = pal[0]
		}
		if v.Bitmap() {
			r.bitmapLine(v, y, pal, zero, back)
		} else {
			r.tileLine(v, y, pal, zero, back)
		}
		// Register 8 bit 1 turns the sprite plane off, which is how a
		// split screen keeps the playfield's sprites out of its panel.
		if regs[8]&0x02 == 0 {
			r.spriteLine(v, y, pal)
		}
	}
	return r.cur
}

// tileLine draws one scanline of a tile screen as a V9938 shows it: table
// bases over the whole 128K, the cartridge's own palette, and register 23's
// vertical scroll, which applies to tile screens exactly as it does to
// bitmaps and which Space Manbow uses to scroll SCREEN 4.
//
// zero is what colour 0 shows as -- the backdrop, or palette entry 0 when
// register 8's transparency bit says so. See scanlines.
func (r *Renderer) tileLine(v *VDP, y int, pal [16]color.RGBA, zero, edge color.RGBA) {
	back := zero
	regs := &v.Reg
	mask := len(v.VRAM) - 1
	name := int(regs[2]&0x7F) << 10
	patBase := int(regs[4]&0x3C) << 11
	colBase := int(regs[10]&0x07)<<14 | int(regs[3]&0x80)<<6
	sy := (y + int(regs[23])) & 0xFF
	row, fy := sy/8, sy%8
	third := (row / 8) & 3
	var lineBuf [ScreenWidthWide]color.RGBA
	for col := 0; col < 32; col++ {
		ch := int(v.VRAM[(name+(row&31)*32+col)&mask])
		idx := third*0x800 + ch*8 + fy
		bits := v.VRAM[(patBase+idx)&mask]
		attr := v.VRAM[(colBase+idx)&mask]
		fg, bg := pal[attr>>4], pal[attr&0x0F]
		if attr>>4 == 0 {
			fg = back
		}
		if attr&0x0F == 0 {
			bg = back
		}
		for x := 0; x < 8; x++ {
			c := bg
			if bits&(0x80>>uint(x)) != 0 {
				c = fg
			}
			lineBuf[col*8+x] = c
		}
	}
	r.adjusted(y, lineBuf[:], int(regs[18]&0x0F^8)-8, edge)
}

// bitmapLine draws one scanline of a bitmap screen, four or eight bits to the
// pixel depending on the mode those registers name. zero is what colour 0
// shows as; see scanlines.
func (r *Renderer) bitmapLine(v *VDP, y int, pal [16]color.RGBA, zero, edge color.RGBA) {
	back := zero
	base := v.PageBase()
	bpl := v.bytesPerLine()
	ppb := v.pixelsPerByte()
	bits := uint(8 / ppb)
	nib := byte(1<<bits) - 1
	// Register 23 slides the display down the page and wraps at 256
	// lines. It is how a V9938 game scrolls without moving any pixels,
	// so ignoring it does not show a stationary picture -- it shows
	// whatever else happens to be in memory.
	sy := (y + int(v.Reg[23])) & 0xFF
	var lineBuf [ScreenWidthWide]color.RGBA
	if v.Mode() == ModeGraphic7 {
		// Screen 8 has no palette: the byte is the colour, three bits
		// of green, three of red, two of blue, scaled the way the
		// chip's DAC steps.
		for x := 0; x < r.width; x++ {
			lineBuf[x] = grb332(v.VRAM[v.phys(base+sy*bpl+x)])
		}
		r.adjusted(y, lineBuf[:], int(v.Reg[18]&0x0F^8)-8, edge)
		return
	}
	for x := 0; x < r.width; x++ {
		c := back
		a := v.phys(base + sy*bpl + x/ppb)
		sh := uint(ppb-1-(x%ppb)) * bits
		if n := (v.VRAM[a] >> sh) & nib; n != 0 {
			c = pal[n&0x0F]
		}
		lineBuf[x] = c
	}
	r.adjusted(y, lineBuf[:], int(v.Reg[18]&0x0F^8)-8, edge)
}

// adjusted lays a finished scanline into the picture through register 18's
// low nibble, which slides the display horizontally by a signed count of
// pixels. The V9938 has no X scroll register, and this adjust, cycled a
// couple of pixels a frame with a column redraw every eighth, is how a
// Manbow scrolls smoothly. What the slide exposes at the edge is the
// backdrop, which is not the same colour as a transparent pixel once
// register 8 says colour 0 is opaque.
func (r *Renderer) adjusted(y int, lineBuf []color.RGBA, adj int, edge color.RGBA) {
	for x := 0; x < r.width; x++ {
		sx := x + adj
		if sx < 0 || sx >= r.width {
			r.set(x, y, edge)
		} else {
			r.set(x, y, lineBuf[sx])
		}
	}
}

// spriteLine draws the sprites covering one scanline, from the attribute
// table in force at that line. Space Manbow aims register 5 at a different
// table for its panel than for its playfield and changes it again mid-field,
// so a frame drawn from whichever table the register held last scatters one
// band's sprites through the others.
//
// The attribute table's base is register 5 with its low *two* bits dropped,
// over register 11 -- and in sprite mode 2 the colour table sits in the 512
// bytes below it. Masking one bit too many lands the attributes on the colour
// table, where every Y reads as zero and nothing is drawn at all: no ship, no
// shots, no enemies. Measured against the reference machine's own memory,
// where R5=EFh puts the attributes at F600h and the colours at F400h.
// spritesPerLine is what the V9938 draws on any one scanline in sprite mode
// 2. Beyond it the chip draws nothing and reports the overflow in status
// register 0.
const spritesPerLine = 8

// graphic7SpritePalette is the fixed palette screen 8's sprites use: the
// bitmap's byte is its own colour there, so the chip gives sprites this
// table instead -- dim primaries, an orange, then bright primaries. The
// V9938 carries it as GRB nibbles; checked against the reference
// renderer on the demo's sprites: the paddle is cyan, colour 13, and
// the first cut of this table had red and green swapped, which painted
// it magenta.
var graphic7SpritePalette = [16][3]byte{ // R, G, B, 0-7
	{0, 0, 0}, {0, 0, 2}, {3, 0, 0}, {3, 0, 2},
	{0, 3, 0}, {0, 3, 2}, {3, 3, 0}, {3, 3, 2},
	{7, 4, 2}, {0, 0, 7}, {7, 0, 0}, {7, 0, 7},
	{0, 7, 0}, {0, 7, 7}, {7, 7, 0}, {7, 7, 7},
}

func (r *Renderer) spriteLine(v *VDP, y int, pal [16]color.RGBA) {
	if v.Mode() == ModeGraphic7 {
		for i, c := range graphic7SpritePalette {
			pal[i] = color.RGBA{
				R: byte(int(c[0]) * 255 / 7),
				G: byte(int(c[1]) * 255 / 7),
				B: byte(int(c[2]) * 255 / 7),
				A: 255,
			}
		}
	}
	attr := int(v.Reg[5]&0xFC)<<7 | int(v.Reg[11]&0x03)<<15
	patt := int(v.Reg[6]&0x3F) << 11
	colTab := attr - 512
	dot := r.width / ScreenWidth
	size, scale := 8, 1
	if v.Reg[1]&0x02 != 0 {
		size = 16
	}
	if v.Reg[1]&0x01 != 0 {
		scale = 2
	}
	// The end-of-list marker is the line just past the display.
	stop := byte(216)
	if v.Lines() == 192 {
		stop = 208
	}
	// The scroll registers move the whole display, sprites with it: the
	// vertical scroll shifts a sprite up the screen exactly as it shifts
	// the pattern under it, and register 18's adjust slides it sideways
	// with the rest of the signal.
	//
	// Measured against the reference machine's own memory and screenshot.
	// Space Manbow parks every sprite it is not using at Y=32 with the
	// scroll at 28, which puts them on display line 5 -- under the status
	// panel, where the same handler has turned the sprite plane off. Drawn
	// without the shift they land on line 33, in the open playfield, which
	// is a row of debris across the top of the field that the reference
	// does not have.
	scroll := int(v.Reg[23])
	adjust := int(v.Reg[18]&0x0F^8) - 8
	var list [32]struct{ i, dy int }
	n := 0
	for i := 0; i < 32; i++ {
		sy := int(v.VRAM[v.phys(attr+i*4)])
		if byte(sy) == stop {
			break
		}
		top := (sy + 1 - scroll) & 0xFF
		if top > v.Lines() {
			// Above the top edge rather than below the bottom one:
			// a sprite there shows the rows that reach the screen.
			top -= 256
		}
		if dy := y - top; dy >= 0 && dy < size*scale {
			list[n].i, list[n].dy = i, dy
			n++
			// The chip draws eight sprites to a line and no
			// more. The ninth and beyond are not drawn at all --
			// the fifth-sprite flag goes up instead -- and a game
			// that puts more than eight on a line is written
			// around that: it rotates which of them holds an
			// early slot, so the ones that vanish change from
			// frame to frame and the eye fills in the rest.
			//
			// Drawing all of them instead shows sprites the
			// hardware hides. Space Manbow keeps thirty-two
			// sprites live and lands twenty-three of them on some
			// lines, in two attribute tables it alternates
			// between, with the same object at a different slot
			// in each -- so the extras we drew changed slot,
			// colour and priority every time the tables flipped.
			// That is the flashing, and the shots and enemies
			// that seemed to jump.
			if n == spritesPerLine {
				break
			}
		}
	}
	// Entry order is priority order, so the line is drawn back to front.
	for k := n - 1; k >= 0; k-- {
		i, dy := list[k].i, list[k].dy
		line := dy / scale
		cbyte := v.VRAM[v.phys(colTab+i*16+(line&0x0F))]
		col := cbyte & 0x0F
		if col == 0 {
			continue
		}
		// Early clock, per line: bit 7 shifts that line 32 pixels
		// left. It is a property of the line, not the sprite --
		// subtracting it once per sprite moves it up to 512 pixels off
		// the screen.
		x0 := int(v.VRAM[v.phys(attr+i*4+1)]) - adjust
		if cbyte&0x80 != 0 {
			x0 -= 32
		}
		pat := int(v.VRAM[v.phys(attr+i*4+2)])
		if size == 16 {
			pat &= 0xFC
		}
		for dx := 0; dx < size*scale; dx++ {
			cx := dx / scale
			byteAt := patt + pat*8 + line
			if size == 16 && cx >= 8 {
				byteAt += 16
			}
			if v.VRAM[v.phys(byteAt)]&(0x80>>uint(cx%8)) == 0 {
				continue
			}
			// The sprite plane stays on a 256-dot grid in the
			// wide modes: the chip places sprites by the same
			// coordinates whatever the bitmap's resolution, and
			// each sprite dot covers two screen dots.
			for i := 0; i < dot; i++ {
				if px := (x0+dx)*dot + i; px >= 0 && px < r.width {
					r.set(px, y, pal[col])
				}
			}
		}
	}
}

// Render draws one frame from VRAM and returns the buffer. The buffer is
// reused between calls.
func (r *Renderer) Render(vram []byte, reg []byte) *image.RGBA {
	// The MSX1 path is always 192 lines and 256 dots, unchanged.
	r.use(ScreenHeight, ScreenWidth)
	l := DecodeLayout(reg)

	backdrop := Palette[l.Backdrop]
	if l.Backdrop == 0 {
		backdrop = Palette[1]
	}
	for i := 0; i < len(r.cur.Pix); i += 4 {
		r.cur.Pix[i+0] = backdrop.R
		r.cur.Pix[i+1] = backdrop.G
		r.cur.Pix[i+2] = backdrop.B
		r.cur.Pix[i+3] = 255
	}
	if !l.DisplayOn {
		return r.cur
	}

	r.drawTiles(vram, l, backdrop)
	r.drawSprites(vram, l)
	return r.cur
}

// drawTiles renders the 32x24 name table. Graphics 2 splits the screen into
// three vertical thirds, each with its own 2 KB of patterns and colours, which
// is what gives the mode its 768 independent tiles.
func (r *Renderer) drawTiles(vram []byte, l VDPLayout, backdrop color.RGBA) {
	for row := 0; row < 24; row++ {
		third := (row / 8) * 0x800
		for col := 0; col < 32; col++ {
			ch := int(vram[(l.Name+row*32+col)&0x3FFF])
			pat := l.Pattern + third + ch*8
			col8 := l.Colour + third + ch*8
			for y := 0; y < 8; y++ {
				bits := vram[(pat+y)&0x3FFF]
				attr := vram[(col8+y)&0x3FFF]
				fg, bg := Palette[attr>>4], Palette[attr&0x0F]
				if attr>>4 == 0 {
					fg = backdrop
				}
				if attr&0x0F == 0 {
					bg = backdrop
				}
				py := row*8 + y
				for x := 0; x < 8; x++ {
					c := bg
					if bits&(0x80>>uint(x)) != 0 {
						c = fg
					}
					r.set(col*8+x, py, c)
				}
			}
		}
	}
}

// drawSprites renders the sprite attribute table. Entry order is priority
// order: sprite 0 wins, so the list is drawn back to front.
func (r *Renderer) drawSprites(vram []byte, l VDPLayout) {
	size := 8
	if l.Sprites16 {
		size = 16
	}
	scale := 1
	if l.Magnified {
		scale = 2
	}

	type spr struct {
		x, y int
		pat  int
		col  byte
	}
	var list []spr

	for i := 0; i < 32; i++ {
		a := l.SpriteAttr + i*4
		y := int(vram[(a+0)&0x3FFF])
		if y == 0xD0 { // end-of-list marker
			break
		}
		// The stored Y is one less than the top line, and values above the
		// screen wrap so a sprite can enter from the top.
		y++
		if y > 0xE0 {
			y -= 256
		}
		x := int(vram[(a+1)&0x3FFF])
		pat := int(vram[(a+2)&0x3FFF])
		c := vram[(a+3)&0x3FFF]
		if c&0x80 != 0 { // early clock: shift 32 pixels left
			x -= 32
		}
		if l.Sprites16 {
			pat &= 0xFC
		}
		list = append(list, spr{x, y, pat, c & 0x0F})
	}

	if r.SpriteLimit {
		for i := range r.perLine {
			r.perLine[i] = 0
		}
		kept := list[:0]
		for _, s := range list {
			over := false
			for dy := 0; dy < size*scale; dy++ {
				py := s.y + dy
				if py < 0 || py >= r.lines {
					continue
				}
				if r.perLine[py] >= 4 {
					over = true
					break
				}
			}
			if over {
				continue
			}
			for dy := 0; dy < size*scale; dy++ {
				if py := s.y + dy; py >= 0 && py < r.lines {
					r.perLine[py]++
				}
			}
			kept = append(kept, s)
		}
		list = kept
	}

	// Back to front, so lower-numbered sprites end up on top.
	for i := len(list) - 1; i >= 0; i-- {
		s := list[i]
		if s.col == 0 {
			continue // transparent
		}
		c := Palette[s.col]
		base := l.SpritePattern + s.pat*8
		for dy := 0; dy < size; dy++ {
			for dx := 0; dx < size; dx++ {
				// A 16x16 sprite is four 8x8 patterns in column order:
				// top-left, bottom-left, top-right, bottom-right.
				var bits byte
				if size == 16 {
					quad := (dx / 8) * 2
					bits = vram[(base+quad*8+dy%8+(dy/8)*8)&0x3FFF]
				} else {
					bits = vram[(base+dy)&0x3FFF]
				}
				if bits&(0x80>>uint(dx%8)) == 0 {
					continue
				}
				for sy := 0; sy < scale; sy++ {
					for sx := 0; sx < scale; sx++ {
						r.set(s.x+dx*scale+sx, s.y+dy*scale+sy, c)
					}
				}
			}
		}
	}
}

func (r *Renderer) set(x, y int, c color.RGBA) {
	if r.cur == nil {
		r.use(ScreenHeight, ScreenWidth)
	}
	if x < 0 || x >= r.width || y < 0 || y >= r.lines {
		return
	}
	i := y*r.cur.Stride + x*4
	r.cur.Pix[i+0] = c.R
	r.cur.Pix[i+1] = c.G
	r.cur.Pix[i+2] = c.B
	r.cur.Pix[i+3] = 255
}
