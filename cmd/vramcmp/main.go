// Command vramcmp asks whether a machine showed the same picture as the
// reference, within a window of frames.
//
// The reference dump is one frame: 16K of VRAM and the eight VDP registers,
// as ref/ref.tcl writes it. The candidate is a span of such frames, as the
// generated harness's -vramspan writes. Both are rendered through the same
// rasteriser -- a pure function of VRAM and the registers -- and the check
// passes if any frame in the span produces the identical image.
//
// The window is the point. A translated machine has no cycle time, so its
// game runs at a legal pace the reference's cycle budget may not reach;
// scene changes land a few frames apart. What must agree is the picture,
// not the cycle it appeared on.
package main

import (
	"bytes"
	"fmt"
	"image/png"
	"os"

	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
)

const frameSize = 16384 + 8

func render(frame []byte) []byte {
	var reg [8]byte
	copy(reg[:], frame[16384:])
	img := z80.NewRenderer().Render(frame[:16384], reg[:])
	pix := make([]byte, len(img.Pix))
	copy(pix, img.Pix)
	return pix
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: vramcmp ref.vram span.bin [out.png]")
		os.Exit(2)
	}
	ref, err := os.ReadFile(os.Args[1])
	if err != nil {
		die(err)
	}
	if len(ref) != frameSize {
		die(fmt.Errorf("reference dump is %d bytes, want %d",
			len(ref), frameSize))
	}
	span, err := os.ReadFile(os.Args[2])
	if err != nil {
		die(err)
	}
	want := render(ref)
	n := len(span) / frameSize
	best, bestDiff := -1, 1<<30
	for i := 0; i < n; i++ {
		got := render(span[i*frameSize : (i+1)*frameSize])
		if bytes.Equal(got, want) {
			fmt.Printf("MATCH at span frame %d of %d\n", i+1, n)
			return
		}
		diff := 0
		for j := 0; j < len(got); j += 4 {
			if got[j] != want[j] || got[j+1] != want[j+1] ||
				got[j+2] != want[j+2] {
				diff++
			}
		}
		if diff < bestDiff {
			best, bestDiff = i, diff
		}
	}
	fmt.Printf("NO MATCH in %d frames; closest is span frame %d with %d "+
		"pixels differing\n", n, best+1, bestDiff)
	if len(os.Args) > 3 && best >= 0 {
		f, _ := os.Create(os.Args[3])
		img := z80.NewRenderer().Render(
			span[best*frameSize:best*frameSize+16384],
			regsOf(span[best*frameSize:]))
		png.Encode(f, img)
		f.Close()
		f, _ = os.Create(os.Args[3] + ".ref.png")
		png.Encode(f, z80.NewRenderer().Render(ref[:16384], regsOf(ref)))
		f.Close()
	}
	os.Exit(1)
}

// regsOf hands back a slice, because the renderer takes one now: a V9938 has
// forty-seven registers where this file's format carries eight.
func regsOf(b []byte) []byte { r := regsOfArr(b); return r[:] }

func regsOfArr(frame []byte) [8]byte {
	var reg [8]byte
	copy(reg[:], frame[16384:16392])
	return reg
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "vramcmp:", err)
	os.Exit(1)
}
