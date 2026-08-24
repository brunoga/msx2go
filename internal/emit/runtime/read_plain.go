//go:build !msxcheck

package z80

// A read, in a build that trusts msx2go's pruning: one indexed load, which
// is the whole reason a static translation is worth making.
//
// See read_check.go for the build that does not trust it.

func (m *M) rd(a uint16) byte { return m.Mem[a] }

func (m *M) rd16(a uint16) uint16 {
	return uint16(m.Mem[a]) | uint16(m.Mem[a+1])<<8
}

// installHoles does nothing here; the map it would install is only consulted
// by the checking build.
func (m *M) installHoles(Info, []Block) {}
