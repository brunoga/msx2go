package z80

import "testing"

// The split log is in time order and its line numbers wrap, so a frame that
// ran longer than one frame's worth of raster logs several passes down the
// screen. The replay has to notice, or it applies a later pass's writes at
// the wrong scanlines.
//
// Snatcher blanks the display partway down one pass and re-enables it during
// the next pass's blanking, which logs as a line *before* the one that
// blanked it. A replay that walks the log once, assuming the lines only
// climb, never reached the re-enable and painted every line below in the
// backdrop -- and at a transition the backdrop is white, so there was a white
// band across the bottom of the screen on every transition the game made.
func TestAWrappedSplitLogReplaysOnlyTheLastPass(t *testing.T) {
	var v VDP
	v.Reset()
	v.V9938 = true
	v.VRAM = make([]byte, 0x20000)
	// SCREEN 5-ish: a bitmap mode with the display on and a black backdrop.
	v.WriteReg(0, 0x02)
	v.WriteReg(1, 0x60) // display on
	v.WriteReg(7, 0x00) // backdrop is palette 0
	// Colour 0 opaque, so the only thing that can paint a line in the
	// backdrop is the display being blanked.
	v.WriteReg(8, 0x20)

	// The backdrop is white, so any line the display is blanked over comes
	// out white while the picture itself is black.
	v.WriteReg(7, 0x0F)
	v.WriteReg(16, 15) // point the palette port at entry 15
	v.WritePalette(0x77)
	v.WritePalette(0x07)

	// Two passes down the screen, as Snatcher's transitions log them. The
	// first blanks the display partway down; the second turns it back on
	// during its blanking, which logs as line -1 -- a line *before* the one
	// that blanked it, so the sequence goes 4, 100, -1 rather than climbing.
	v.SplitLog = []RegEvent{
		{Line: 4, Reg: 1, Old: 0x63, New: 0x23},   // display off
		{Line: 100, Reg: 8, Old: 0x20, New: 0x20}, // later in the same pass
		{Line: -1, Reg: 1, Old: 0x23, New: 0x63},  // on again, next pass
	}
	// The registers as the frame actually ended: the display is on.
	v.Reg[1] = 0x63
	v.Reg[7] = 0x0F

	img := NewRenderer().RenderVDP(&v)
	b := img.Bounds()
	// With only the final pass replayed the display is on for the whole
	// frame, so nothing is painted in the backdrop. The old replay left
	// everything from line 80 down blanked.
	blanked := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		c := img.RGBAAt(b.Min.X, y)
		same := true
		for x := b.Min.X + 1; x < b.Max.X; x++ {
			if img.RGBAAt(x, y) != c {
				same = false
				break
			}
		}
		// A backdrop-painted line is flat and white here.
		if same && c.R > 200 && c.G > 200 && c.B > 200 {
			blanked++
		}
	}
	if blanked > 0 {
		t.Errorf("%d lines came out painted in the backdrop; the replay "+
			"followed the log past its wrap and never saw the display come "+
			"back on", blanked)
	}
}

// The final pass of an overrunning frame is usually half-finished: the raster
// stopped wherever the frame ended. Replaying just that pass -- which was the
// first fix -- draws its own phantom, because everything above the line the
// pass begins at is painted in whatever state preceded it. For a blanked
// display that is a black strip from the top of the screen down to the line
// the raster had reached, which is what Snatcher's transition to its blue
// title screen showed.
//
// So a wrapped log is not replayed at all. The picture is drawn in the
// registers the frame ended with, the one state that was certainly real.
func TestAPartialFinalPassIsNotReplayedEither(t *testing.T) {
	var v VDP
	v.Reset()
	v.V9938 = true
	v.VRAM = make([]byte, 0x20000)
	v.WriteReg(0, 0x02)
	v.WriteReg(1, 0x63) // display on
	v.WriteReg(8, 0x20) // colour 0 opaque
	v.WriteReg(7, 0x0F)
	v.WriteReg(16, 15)
	v.WritePalette(0x77)
	v.WritePalette(0x07)

	// The shape Snatcher logs: a pass that blanks late, then a wrap into a
	// pass that only got as far as line 189 before the frame ended.
	v.SplitLog = []RegEvent{
		{Line: 148, Reg: 1, Old: 0x63, New: 0x23},
		{Line: 202, Reg: 1, Old: 0x23, New: 0x23},
		{Line: 188, Reg: 23, Old: 0x00, New: 0x00}, // wrap
		{Line: 189, Reg: 1, Old: 0x23, New: 0x63},
	}
	v.Reg[1] = 0x63 // the frame ended with the display on
	v.Reg[7] = 0x0F

	img := NewRenderer().RenderVDP(&v)
	b := img.Bounds()
	painted := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		c := img.RGBAAt(b.Min.X, y)
		same := true
		for x := b.Min.X + 1; x < b.Max.X; x++ {
			if img.RGBAAt(x, y) != c {
				same = false
				break
			}
		}
		if same && c.R > 200 && c.G > 200 && c.B > 200 {
			painted++
		}
	}
	if painted > 0 {
		t.Errorf("%d lines came out painted in the backdrop; the final pass "+
			"begins partway down and everything above it was drawn in the "+
			"state that preceded it", painted)
	}
}

// Register 1 bit 6 blanks the display, and a game that writes video memory
// with the display off drops it and puts it back a few lines later. Snatcher
// does that while the raster is in the picture -- the bit goes down at display
// line 8 and back up at 14 -- and a replay that followed it painted those
// lines in the backdrop: a black bar across the top of every screen it drew.
//
// The reference machine shows nothing there. Its picture on that screen is one
// flat blue from the first line to the last, measured row by row, while its
// own register log has the bit dropped at line 8. So the chip does not blank
// the lines a mid-frame write covers.
func TestAMidFrameBlankDoesNotPaintTheLinesItCovers(t *testing.T) {
	var v VDP
	v.Reset()
	v.V9938 = true
	v.VRAM = make([]byte, 0x20000)
	v.WriteReg(0, 0x02)
	v.WriteReg(1, 0x63) // display on for the frame
	v.WriteReg(8, 0x20) // colour 0 opaque, so the picture is not the backdrop
	v.WriteReg(7, 0x0F) // a backdrop that would show up loudly
	v.WriteReg(16, 15)
	v.WritePalette(0x77)
	v.WritePalette(0x07)

	// Down at line 7, back up at 16, the way the game writes video memory.
	v.SplitLog = []RegEvent{
		{Line: 7, Reg: 1, Old: 0x63, New: 0x23},
		{Line: 8, Reg: 8, Old: 0x20, New: 0x2A},
		{Line: 16, Reg: 1, Old: 0x23, New: 0x63},
		{Line: 17, Reg: 8, Old: 0x2A, New: 0x20},
	}

	img := NewRenderer().RenderVDP(&v)
	b := img.Bounds()
	painted := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		c := img.RGBAAt(b.Min.X, y)
		if c.R > 200 && c.G > 200 && c.B > 200 {
			painted++
		}
	}
	if painted > 0 {
		t.Errorf("%d lines were painted in the backdrop by a blank the "+
			"reference machine does not show", painted)
	}
}
