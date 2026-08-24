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
//
// A segment can be in two pages at once, and the hardware makes nothing of
// it: both windows are the same RAM. Snatcher leans on it -- the disk
// operating system's way of putting code in page one is to select a segment
// in page *two*, load into it there, and then select the same segment in
// page one to run it. Two things follow for a machine built on one flat
// memory. The arriving segment's bytes must come from wherever it is live
// -- another page's window, if it is in one -- because its store is stale
// for as long as it is mapped. And while a segment is in two windows, a
// write through either must land in both, which is aliasedPages' job: the
// write path mirrors while the table says so. See wr.
//
// Nothing is pushed back out when a page *leaves* a shared segment: the
// other window has been kept current by the mirroring, and overwriting it
// here would roll it back. The first cut of this did exactly that, and the
// boot-time RAM count -- which briefly puts the work area's own segment in
// page one, stack and all -- was un-run every time page one moved on.
func (m *M) setRAMSegment(page, seg int) {
	m.initRAMMapper()
	seg &= ramSegments - 1
	old := m.ramSeg[page]
	if old == seg {
		return
	}
	at := page * ramSegSize
	copy(m.ramStore[old], m.Mem[at:at+ramSegSize])
	src := m.ramStore[seg][:ramSegSize]
	for p := 0; p < 4; p++ {
		if p != page && m.ramSeg[p] == seg {
			src = m.Mem[p*ramSegSize : (p+1)*ramSegSize]
			break
		}
	}
	copy(m.Mem[at:at+ramSegSize], src)
	m.ramSeg[page] = seg
	m.ramAliased = false
	for p := 0; p < 4; p++ {
		m.ramAlias[p] = -1
		for q := 0; q < 4; q++ {
			if q != p && m.ramSeg[q] == m.ramSeg[p] {
				m.ramAlias[p] = q
				m.ramAliased = true
				break
			}
		}
	}
}

// mirror repeats a write into the other window of a doubly-mapped segment.
// The caller has checked ramAliased, which is false on every machine that
// has not mapped one segment into two pages.
func (m *M) mirror(a uint16, v byte) {
	if p := m.ramAlias[a>>14]; p >= 0 {
		m.Mem[uint16(p)<<14|a&0x3FFF] = v
	}
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
