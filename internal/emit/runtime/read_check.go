//go:build msxcheck

package z80

import (
	"fmt"
	"sort"
)

// A read, in a build that is trying to catch msx2go's pruning out.
//
// Keeping only the bytes no translated instruction covers is a hypothesis:
// that nothing reads a byte the tracer proved is code. It holds for every
// cartridge looked at so far and it is not provable in general -- a checksum
// over the program, a table packed between two routines, and it is false.
//
// So rather than argue about it, this build makes the hypothesis falsifiable.
// Every read is mapped back through the mapper to an offset in the original
// image and checked against the holes the pruning left. Reading one panics
// naming the address, the offset and the run it fell in, which is a bug report
// rather than a game that is quietly wrong.
//
// It is slow. That is the point of it being a separate build: run the tape
// battery under it once and the hypothesis stops being likely and starts being
// tested over every path those tapes cover.

// hole is one run of bytes the pruning left out.
type hole struct{ lo, hi int }

// installHoles works out what was pruned: everything inside the image that no
// block covers.
func (m *M) installHoles(info Info, blocks []Block) {
	covered := make([]bool, info.Size)
	for _, b := range blocks {
		for i := b.Off; i < b.Off+len(b.Data) && i < info.Size; i++ {
			covered[i] = true
		}
	}
	var holes []hole
	for i := 0; i < info.Size; {
		if covered[i] {
			i++
			continue
		}
		j := i
		for j < info.Size && !covered[j] {
			j++
		}
		holes = append(holes, hole{i, j})
		i = j
	}
	m.holes = holes
	m.holesOn = len(holes) > 0
}

// rd reads a byte, and reports a read from a pruned run.
func (m *M) rd(a uint16) byte {
	if m.holesOn {
		m.checkHole(a)
	}
	return m.Mem[a]
}

func (m *M) rd16(a uint16) uint16 {
	return uint16(m.rd(a)) | uint16(m.rd(a+1))<<8
}

// checkHole maps a logical address back to an offset in the original image and
// panics if that offset was pruned away.
func (m *M) checkHole(a uint16) {
	off := m.mem.mapper.Phys(m.mem.bank, int(a), m.mem.nbanks)
	if off < 0 {
		return // not the cartridge: RAM, and none of this applies
	}
	i := sort.Search(len(m.holes), func(k int) bool {
		return m.holes[k].hi > off
	})
	if i >= len(m.holes) || off < m.holes[i].lo {
		return
	}
	panic(fmt.Sprintf(
		"z80: read of %04Xh (image offset %05Xh) falls in a pruned run "+
			"%05Xh-%05Xh. msx2go kept only the bytes no translated "+
			"instruction covers, and this cartridge reads one that is "+
			"covered. Regenerate with -data whole.",
		a, off, m.holes[i].lo, m.holes[i].hi))
}
