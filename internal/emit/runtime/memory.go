package z80

// The address space, and the cartridge paged into it.
//
// Mem is a flat 64K array and every translated instruction indexes it
// directly, which is the whole reason the translation is worth doing: a read
// is one bounds-checked load, not a page lookup. Paging therefore *copies* the
// selected bank into the window rather than indirecting through it. That
// sounds expensive and is not -- a bank is 8K and even a game switching a
// hundred times a frame moves less than a megabyte a second -- and it keeps
// the hot path a single array index.
//
// It also makes the model honest about one thing: after a switch, the bytes at
// 8123h really are the new bank's, including to anything that copied a pointer
// to them earlier. That is what the hardware does.

// memory carries the cartridge image and the paging over it.
type memory struct {
	rom    []byte
	mapper Mapper
	nbanks int
	// bank is the selected bank per page register.
	bank []int
	// rom8k marks each 8K page of the address space as cartridge, so a
	// write there is dropped rather than corrupting the image.
	rom8k [8]bool
}

// New returns a machine with the image mapped as the cartridge's own mapper
// says and RAM cleared.
//
// The mapper is the one msx2go recorded when it translated the code. Handing
// a different one, or a different image, is not a supported thing to do: the
// translation is only true of the bytes it was made from.
func New(rom []byte, mapper Mapper) *M {
	m := &M{}
	m.mem.rom = rom
	m.mem.mapper = mapper
	m.mem.nbanks = mapper.BankCount(len(rom))
	m.mem.bank = append([]int(nil), mapper.Initial...)
	for _, p := range mapper.Pages {
		for a := p[0]; a < p[1] && a < 0x10000; a += 0x2000 {
			m.mem.rom8k[a>>13] = true
		}
	}
	for i := range m.mem.bank {
		m.page(i)
	}
	for i := range m.Keys {
		m.Keys[i] = 0xFF // nothing pressed
	}
	m.Hz = 60
	m.CPUScale = 1
	m.VDP.Status2 = m.status2
	m.VDP.Cycles = func() uint64 { return m.Cyc }
	m.VDP.RegLine = m.rasterDisplayLine
	m.VDP.ReArm = m.rearmLine
	m.VDP.Reset()
	m.PSG.PortA = 0xFF // no joystick input
	// BIOS in slot 0, the cartridge in slot 1 across pages 1 and 2, RAM
	// in slot 3. Two bits a page, low page first.
	m.PrimarySlot = 0<<0 | 1<<2 | 1<<4 | 3<<6
	return m
}

// Banks reports the bank selected in each page register, which is what names
// a translated label together with the address.
func (m *M) Banks() []int { return m.mem.bank }

// Bank is the bank mapped at a logical address, or 0 where the cartridge is
// not mapped at all -- RAM, which the translation never runs from.
func (m *M) Bank(addr uint16) int {
	i := m.mem.mapper.PageOf(int(addr))
	if i < 0 || i >= len(m.mem.bank) {
		return 0
	}
	return m.mem.mapper.Mask(m.mem.bank[i], m.mem.nbanks)
}

// Offset is where a logical address is in the image right now.
//
// It is what names a translated instruction in a banked cartridge: an address
// alone does not, because which bytes 8123h holds depends on the paging. The
// dispatch keys on this.
//
// Anything outside the cartridge -- RAM, the BIOS -- has no offset, and gets
// one that no label can match, so reaching it is reported rather than mistaken
// for the first instruction in the image.
func (m *M) Offset(addr uint16) int {
	off := m.mem.mapper.Phys(m.mem.bank, int(addr), m.mem.nbanks)
	if off < 0 || off >= len(m.mem.rom) {
		return -1
	}
	return off
}

// page copies the bank currently selected for page register i into its window.
func (m *M) page(i int) {
	mp := m.mem.mapper
	if i < 0 || i >= len(mp.Pages) {
		return
	}
	start, end := mp.Pages[i][0], mp.Pages[i][1]
	if end > 0x10000 {
		end = 0x10000
	}
	off := mp.Mask(m.mem.bank[i], m.mem.nbanks) * mp.BankSize
	for a := start; a < end; a++ {
		p := off + (a - start)
		if p < len(m.mem.rom) {
			m.Mem[a] = m.mem.rom[p]
		} else {
			m.Mem[a] = 0xFF
		}
	}
}

// setBank selects a bank for one page register and re-pages it. Writing the
// bank that is already there is common -- drivers reload the register every
// frame -- so it is worth not copying 8K for nothing.
func (m *M) setBank(page, bank int) {
	if m.OnBank != nil {
		m.OnBank(page, bank)
	}
	if page < 0 || page >= len(m.mem.bank) || m.mem.bank[page] == bank {
		return
	}
	m.mem.bank[page] = bank
	m.page(page)
	// Bank 3Fh in the page the chip lives behind swaps ROM for registers.
	if m.mem.mapper.SCC && page == 2 {
		if on := bank == sccBank; on != m.SCC.Active {
			m.SCC.Active = on
			if on {
				for a := sccBase; a < sccEnd; a++ {
					m.Mem[a] = 0
				}
			} else {
				m.page(page)
			}
		}
	}
}

// --- memory ---------------------------------------------------------------
//
// rd and rd16 live in read_plain.go and read_check.go, one of which is built
// depending on the msxcheck tag, so that the pruning check costs a normal
// build nothing at all -- not even a branch on the hottest path there is.

func (m *M) wr(a uint16, v byte) {
	if m.MemTrace != nil {
		m.MemTrace(a, v)
	}
	// A bank register is selected by *writing to ROM*, so this comes first:
	// the write lands on the mapper and then, being ROM, goes nowhere.
	if p := m.mem.mapper.SwitchPage(int(a)); p >= 0 {
		m.setBank(p, int(v))
		return
	}
	// And where the mapper has put the sound chip instead of ROM, a write
	// there is a write to the chip. Mirrored into the address space as
	// well, because its registers read back.
	if m.SCC.Active && a >= sccBase && a < sccEnd {
		m.SCC.Write(a, v)
		m.Mem[a] = v
		return
	}
	if m.mem.rom8k[a>>13] {
		return // cartridge ROM: writes are dropped, as on the real machine
	}
	m.Mem[a] = v
	if m.ramAliased {
		m.mirror(a, v)
	}
	if m.WatchWrites != nil {
		m.WatchWrites(a, v)
	}
}

func (m *M) wr16(a uint16, v uint16) {
	m.wr(a, byte(v))
	m.wr(a+1, byte(v>>8))
}

func (m *M) push(v uint16) {
	m.SP -= 2
	m.wr16(m.SP, v)
}

func (m *M) pop() uint16 {
	v := m.rd16(m.SP)
	m.SP += 2
	return v
}
