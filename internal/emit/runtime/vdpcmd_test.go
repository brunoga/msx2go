package z80

import "testing"

// A transfer into video memory takes its first byte from register 44, which
// the program loaded *before* it wrote the command. A machine that waits for
// the next write instead is one byte behind for the whole rectangle, and the
// picture comes out shifted rather than noisy -- which is what Snatcher's
// options screen looked like.
//
// Both commands are checked because they are one engine: HMMC moves a whole
// byte a write and LMMC a single pixel, but the byte arrives the same way.
func TestTransferTakesItsFirstByteFromTheRegister(t *testing.T) {
	t.Run("HMMC", func(t *testing.T) {
		v := screen7()
		// Four bytes on one line at x=0, y=4. NX is in pixels, and
		// SCREEN 7 puts two in a byte, so eight of them are four bytes.
		setPair(v, cmdDX, 0)
		setPair(v, cmdDY, 4)
		setPair(v, cmdNX, 8)
		setPair(v, cmdNY, 1)

		want := []byte{0x12, 0x34, 0x56, 0x78}
		feed(t, v, 0xF0, want)

		for i, w := range want {
			if got := v.VRAM[v.phys(4*256+i)]; got != w {
				t.Errorf("byte %d = %02X, want %02X", i, got, w)
			}
		}
	})

	t.Run("LMMC", func(t *testing.T) {
		v := screen7()
		// Four pixels on one line at x=0, y=4: LMMC moves one a write.
		setPair(v, cmdDX, 0)
		setPair(v, cmdDY, 4)
		setPair(v, cmdNX, 4)
		setPair(v, cmdNY, 1)

		want := []byte{0x01, 0x02, 0x03, 0x04}
		feed(t, v, 0xB0, want)

		for i, w := range want {
			if got := v.getPixel(i, 4); got != w {
				t.Errorf("pixel %d = %X, want %X", i, got, w)
			}
		}
	})
}

// feed runs the documented start-up order: the first byte into register 44,
// then the command, then one write per remaining byte -- and insists the
// transfer asks for exactly as many as the rectangle holds.
func feed(t *testing.T, v *VDP, cmd byte, want []byte) {
	t.Helper()
	v.WriteReg(cmdCLR, want[0])
	v.WriteReg(cmdCMD, cmd)
	for _, b := range want[1:] {
		if !v.XferActive() {
			t.Fatalf("the transfer ended before byte %02X was sent", b)
		}
		v.WriteReg(cmdCLR, b)
	}
	if v.XferActive() {
		t.Fatal("the transfer still wants bytes after the last one was sent")
	}
}

// screen7 is a V9938 set up the way Snatcher has it: SCREEN 7, 256 bytes to
// the line, two pixels to the byte, over the whole 128K.
func screen7() *VDP {
	var v VDP
	v.Reset()
	v.VRAM = make([]byte, 0x20000)
	v.V9938 = true
	v.WriteReg(0, 0x0A)
	v.WriteReg(1, 0x60)
	if v.Mode() != ModeGraphic6 {
		panic("the test's own setup is not SCREEN 7")
	}
	return &v
}

// setPair writes one of the command engine's sixteen-bit coordinate pairs.
func setPair(v *VDP, r byte, n int) {
	v.WriteReg(r, byte(n))
	v.WriteReg(r+1, byte(n>>8))
}
