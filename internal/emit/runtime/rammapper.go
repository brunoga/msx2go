package z80

// The memory mapper: more RAM than fits in the address space.
//
// An MSX2 has 128K or more of RAM and only 64K of address space, so the RAM
// is cut into 16K segments and four registers say which segment is visible in
// which page. The registers are ports FCh to FFh, one per page.
//
// This machine keeps one flat 64K Mem, which is what every other part of it
// reads and what the translated code indexes directly. So a segment change is
// a copy: the page that is going away is put back in its segment's store, and
// the one arriving is copied in. That is what the cartridge mapper already
// does for ROM banks; this is the same idea for RAM, and the cost is paid
// only when a program actually switches.
//
// It is enabled only on a machine with no cartridge, which is a disk machine,
// because there the whole address space is RAM and nothing else can own a
// page. A cartridge's pages one and two are its own, and a mapper that copied
// RAM over them would be writing over the game.

// ramSegSize is a mapper segment: 16K, one page of the address space.
const ramSegSize = 0x4000

// ramSegments is how many segments this machine has: 256K, which is what a
// disk machine of this era is built out of once it has a mapper at all, and
// what the games that count them are counting up to. Snatcher wants nine
// before it will start.
const ramSegments = 16

// initRAMMapper gives the machine its segments, mapped the way an MSX2
// leaves them at power-on: the highest segment in page zero, counting down,
// so that page three -- where the work area lives -- is segment zero.
func (m *M) initRAMMapper() {
	if m.ramStore != nil {
		return
	}
	m.ramStore = make([][]byte, ramSegments)
	for i := range m.ramStore {
		m.ramStore[i] = make([]byte, ramSegSize)
	}
	for p := 0; p < 4; p++ {
		m.ramSeg[p] = 3 - p
	}
}

// ramMapperPort reports whether a port is one of the mapper's four, and
// which page it belongs to.
func ramMapperPort(port byte) (page int, ok bool) {
	if port < 0xFC {
		return 0, false
	}
	return int(port - 0xFC), true
}

// setRAMSegment puts a segment in a page, moving the bytes to match: the
// page's current contents go back to the segment they belong to, and the
// arriving segment's contents come out of its store.
func (m *M) setRAMSegment(page, seg int) {
	m.initRAMMapper()
	seg &= ramSegments - 1
	if m.ramSeg[page] == seg {
		return
	}
	at := page * ramSegSize
	copy(m.ramStore[m.ramSeg[page]], m.Mem[at:at+ramSegSize])
	copy(m.Mem[at:at+ramSegSize], m.ramStore[seg])
	m.ramSeg[page] = seg
}

// ramSegmentOf is what reading a mapper register gives: the segment in that
// page, with the bits the machine does not use left set, which is what the
// hardware's undriven lines read as and what a program counting segments
// measures.
func (m *M) ramSegmentOf(page int) byte {
	m.initRAMMapper()
	return byte(m.ramSeg[page]) | ^byte(ramSegments-1)
}

// hasRAMMapper says whether this machine answers the mapper ports. See the
// note above about cartridges.
func (m *M) hasRAMMapper() bool { return len(m.mem.rom) == 0 }
