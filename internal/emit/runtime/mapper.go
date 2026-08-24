package z80

import "fmt"

// A megaROM is bigger than the 32K the Z80 can see through a cartridge slot,
// so it is cut into banks that the program maps into fixed logical pages by
// writing to magic addresses. A logical address therefore means nothing on its
// own: 8123h is a different byte depending on which bank is in the 8000h page.
//
// Mapper is the shape of one such scheme, and it is data rather than code so
// that msx2go can write the cartridge's own mapper into the generated source
// as a literal. The tracer and the running machine then page with the very
// same implementation, which is the only way to be sure the addresses the
// translation was made from are the addresses it will see.

// Switch is a write range that selects a bank, and which page register it
// lands on.
type Switch struct{ Lo, Hi, Page int }

// Mapper describes a cartridge mapper.
type Mapper struct {
	// Name is the mapper's usual name: "none", "konami4", "konami-scc",
	// "ascii8", "ascii16".
	Name string
	// BankSize is how big one bank is.
	BankSize int
	// Pages are the logical [start, end) windows, one per bank register.
	Pages [][2]int
	// Initial is the bank in each page at power-on.
	Initial []int
	// Switches are the write ranges that select a bank.
	Switches []Switch
	// SCC says bank 3Fh in a page exposes the sound chip rather than ROM.
	SCC bool
}

// Flat is a plain, non-banked image: one page, one bank, no switching.
func Flat(base, size int) Mapper {
	return Mapper{
		Name: "none", BankSize: size,
		Pages: [][2]int{{base, base + size}}, Initial: []int{0},
	}
}

// konamiPages is the four 8K windows the Konami and ASCII-8 schemes use.
var konamiPages = [][2]int{
	{0x4000, 0x6000}, {0x6000, 0x8000}, {0x8000, 0xA000}, {0xA000, 0xC000},
}

// Mappers are the known schemes by name.
//
// Konami4 fixes the 4000h page to bank 0 and selects the other three by
// writing anywhere in the page itself. Konami-SCC makes all four switchable
// through narrow windows and puts the sound chip at 9800h when bank 3Fh is in
// the 8000h page. Both ASCII schemes keep every register inside 6000h-7FFFh,
// which is itself readable ROM.
var Mappers = map[string]Mapper{
	"konami4": {
		Name: "konami4", BankSize: 0x2000, Pages: konamiPages,
		Initial: []int{0, 1, 2, 3},
		Switches: []Switch{
			{0x6000, 0x8000, 1}, {0x8000, 0xA000, 2}, {0xA000, 0xC000, 3},
		},
	},
	"konami-scc": {
		Name: "konami-scc", BankSize: 0x2000, Pages: konamiPages,
		Initial: []int{0, 1, 2, 3},
		Switches: []Switch{
			{0x5000, 0x5800, 0}, {0x7000, 0x7800, 1},
			{0x9000, 0x9800, 2}, {0xB000, 0xB800, 3},
		},
		SCC: true,
	},
	"ascii8": {
		Name: "ascii8", BankSize: 0x2000, Pages: konamiPages,
		Initial: []int{0, 0, 0, 0},
		Switches: []Switch{
			{0x6000, 0x6800, 0}, {0x6800, 0x7000, 1},
			{0x7000, 0x7800, 2}, {0x7800, 0x8000, 3},
		},
	},
	"ascii16": {
		Name: "ascii16", BankSize: 0x4000,
		Pages:   [][2]int{{0x4000, 0x8000}, {0x8000, 0xC000}},
		Initial: []int{0, 0},
		Switches: []Switch{
			{0x6000, 0x6800, 0}, {0x7000, 0x7800, 1},
		},
	},
}

// NamedMapper is Mappers by name, with "none"/"flat"/"" giving a flat image.
func NamedMapper(name string, base, size int) (Mapper, error) {
	switch name {
	case "", "none", "flat":
		return Flat(base, size), nil
	}
	if m, ok := Mappers[name]; ok {
		return m, nil
	}
	return Mapper{}, fmt.Errorf("z80: unknown mapper %q", name)
}

// BankCount is how many banks an image of this size holds.
func (m Mapper) BankCount(size int) int {
	if m.BankSize == 0 {
		return 0
	}
	return size / m.BankSize
}

// Mask wraps a bank register the way the hardware does, which is by dropping
// the bits above the image's size rather than by refusing the write.
func (m Mapper) Mask(bank, nbanks int) int {
	if nbanks == 0 {
		return 0
	}
	if nbanks&(nbanks-1) == 0 {
		return bank & (nbanks - 1)
	}
	return ((bank % nbanks) + nbanks) % nbanks
}

// PageOf is which bank register covers a logical address, or -1.
func (m Mapper) PageOf(addr int) int {
	for i, p := range m.Pages {
		if addr >= p[0] && addr < p[1] {
			return i
		}
	}
	return -1
}

// Phys is the offset in the image for a logical address under a given bank
// state, or -1 where nothing is mapped.
func (m Mapper) Phys(banks []int, addr, nbanks int) int {
	i := m.PageOf(addr)
	if i < 0 || i >= len(banks) {
		return -1
	}
	return m.Mask(banks[i], nbanks)*m.BankSize + (addr - m.Pages[i][0])
}

// SwitchPage is which bank register a write to addr selects, or -1.
func (m Mapper) SwitchPage(addr int) int {
	for _, s := range m.Switches {
		if addr >= s.Lo && addr < s.Hi {
			return s.Page
		}
	}
	return -1
}
