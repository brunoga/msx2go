package dis

// Mapper detection.
//
// A megaROM does not say which scheme it uses; you find out by looking at what
// the code writes to. Each scheme has its own set of register addresses, and
// `ld (nnnn),a` landing on one of them is the strong signal. Addresses shared
// by several schemes score for all of them, so what comes out is a ranking to
// confirm rather than an oracle -- which is why the tool prints the whole
// ranking and takes -mapper to override it.

// Rank is one candidate scheme and how much the image looks like it.
type Rank struct {
	Name  string
	Score int
}

// signatures are the bank-register addresses of each scheme.
var signatures = map[string][]uint16{
	"konami4":    {0x6000, 0x8000, 0xA000},
	"konami-scc": {0x5000, 0x7000, 0x9000, 0xB000},
	"ascii8":     {0x6000, 0x6800, 0x7000, 0x7800},
	"ascii16":    {0x6000, 0x7000, 0x77FF},
}

// DetectMapper is the best guess for an image, or "none" for anything that
// fits in the 32K a cartridge slot shows without any paging at all.
func DetectMapper(data []byte) string {
	r := RankMappers(data)
	if len(r) == 0 {
		return "none"
	}
	return r[0].Name
}

// RankMappers scores every scheme, best first.
func RankMappers(data []byte) []Rank {
	if len(data) <= 0x8000 {
		return []Rank{{"none", 1}}
	}
	score := map[string]int{}
	for name := range signatures {
		score[name] = 0
	}
	hit := func(addr uint16) {
		for name, regs := range signatures {
			for _, r := range regs {
				if r == addr {
					score[name]++
				}
			}
		}
	}
	for i := 0; i+2 < len(data); i++ {
		switch data[i] {
		case 0x32: // ld (nnnn),a -- the strong signal
			hit(uint16(data[i+1]) | uint16(data[i+2])<<8)
		case 0x01, 0x11, 0x21: // ld bc/de/hl,nnnn -- weaker, and common
			hit(uint16(data[i+1]) | uint16(data[i+2])<<8)
		}
	}
	out := make([]Rank, 0, len(score))
	for name, s := range score {
		out = append(out, Rank{name, s})
	}
	// Sorted by score, then name, so the answer does not depend on map
	// iteration order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && less(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	if out[0].Score == 0 {
		return append([]Rank{{"none", 0}}, out...)
	}
	return out
}

func less(a, b Rank) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Name < b.Name
}
