package z80

// PSG is an AY-3-8910 register file. Sound synthesis lives in internal/audio;
// what matters here is capturing exactly which registers the driver writes,
// in order, so the port can be diffed against the original.
type PSG struct {
	Reg   [16]byte
	Latch byte

	// PortA is register 14, which on the MSX is an *input*: the joystick.
	// Reading it must not return whatever was last written, or the game
	// sees every direction held down at once. 0FFh is "nothing pressed".
	PortA byte

	// Writes records every register write in order when Log is true, which
	// is what the conformance tests compare.
	Writes []PSGWrite
	Log    bool
}

type PSGWrite struct {
	Frame uint64
	Reg   byte
	Val   byte
}

func (p *PSG) Write(reg, v byte) {
	if reg > 15 {
		return
	}
	p.Reg[reg] = v
	if p.Log {
		p.Writes = append(p.Writes, PSGWrite{Reg: reg, Val: v})
	}
}

// TakeWrites returns the writes recorded since the last call and clears the
// log. The game loop drains this once per frame and hands it to the
// synthesiser.
func (p *PSG) TakeWrites() []PSGWrite {
	w := p.Writes
	p.Writes = p.Writes[:0:0]
	return w
}

func (p *PSG) Read(reg byte) byte {
	switch {
	case reg == 14:
		return p.PortA
	case reg > 15:
		return 0xFF
	}
	return p.Reg[reg]
}
