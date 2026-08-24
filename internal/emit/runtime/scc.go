package z80

// The Konami SCC: five channels of 32-sample wavetable, on the cartridge.
//
// Konami put a sound chip in the mapper. When the page-2 bank register is
// given bank 3Fh, the window at 9800h-9FFFh stops being ROM and becomes the
// chip's registers -- which is why a cartridge that has one is detected by the
// same signature that detects its mapper, and why the two cannot be separated.
//
// Each channel plays a 32-byte signed waveform at a rate the twelve-bit period
// sets: the chip walks the table once every 32*(period+1) ticks of the 3.58 MHz
// clock. Channels four and five share one waveform, which is a real limitation
// of the hardware and not a shortcut here.
//
// The registers are readable, so they are also mirrored into the address space
// the way a bank is -- copied rather than indirected, for the same reason.

// sccChannels is how many the chip has.
const sccChannels = 5

// SCC is the chip's state and its synthesiser.
type SCC struct {
	// Wave is the per-channel table. Four and five share table four.
	Wave [4][32]int8
	// Freq is the twelve-bit period per channel, Vol the four-bit volume,
	// and Enable a bit per channel.
	Freq   [sccChannels]int
	Vol    [sccChannels]byte
	Enable byte

	// Active says the mapper has the registers visible rather than ROM.
	Active bool

	pos        [sccChannels]int
	acc        [sccChannels]float64
	last       [sccChannels]float64
	sampleRate int
}

// sccClock is the chip's own clock: the whole 3.579545 MHz colour-burst
// crystal, not the halved one the PSG counts. Its tone is
// clock/(32*(period+1)), and taking the PSG's constant put every SCC voice
// an octave down.
//
// Measured, not assumed. The reference machine's SCC period registers were
// logged against a recording of its sound, and each note's predicted
// frequency compared against the magnitude of its own neighbourhood in the
// spectrum: over ten notes the 3.58 MHz prediction sits 9.5 times above the
// surrounding noise -- individual notes 32, 27 and 16 times above -- while
// the 1.79 MHz prediction sits at 1.4, which is the noise.
const sccClock = 3579545.0

// sccBase and sccEnd are the window the registers appear in.
const (
	sccBase = 0x9800
	sccEnd  = 0xA000
	// sccBank is the bank value that makes them appear.
	sccBank = 0x3F
)

// waveOf is the table a channel plays. Four and five share one.
func (s *SCC) waveOf(ch int) *[32]int8 {
	if ch >= 4 {
		return &s.Wave[3]
	}
	return &s.Wave[ch]
}

// Write applies a write to the register window.
func (s *SCC) Write(addr uint16, v byte) {
	off := int(addr) - sccBase
	switch {
	case off < 0x80: // the four waveform tables
		s.Wave[off>>5][off&31] = int8(v)
	case off < 0x8A: // five twelve-bit periods, low byte first
		ch := (off - 0x80) >> 1
		if (off-0x80)&1 == 0 {
			s.Freq[ch] = s.Freq[ch]&0xF00 | int(v)
		} else {
			s.Freq[ch] = s.Freq[ch]&0x0FF | int(v&0x0F)<<8
		}
		// A period change restarts the walk through the table, which is
		// what makes a sweep sound like a sweep rather than a stutter.
		s.pos[ch], s.acc[ch] = 0, 0
	case off < 0x8F: // five volumes
		s.Vol[off-0x8A] = v & 0x0F
	case off == 0x8F:
		s.Enable = v & 0x1F
	}
}

// SetSampleRate tells the synthesiser what it is producing.
func (s *SCC) SetSampleRate(r int) { s.sampleRate = r }

// Synthesize adds the chip's five channels into a stereo buffer.
//
// It adds rather than fills, because the PSG is playing at the same time
// through the same speaker and the cartridge expects to be heard over it.
func (s *SCC) Synthesize(buf []int16) {
	if s.sampleRate == 0 {
		return
	}
	// The chip walks a channel's 32-sample table once every
	// 32*(period+1) clocks, so one sample of the table lasts (period+1)
	// clocks.
	for i := 0; i+1 < len(buf); i += 2 {
		sum := 0.0
		for ch := 0; ch < sccChannels; ch++ {
			if s.Enable&(1<<uint(ch)) == 0 || s.Freq[ch] == 0 {
				continue
			}
			step := sccClock / float64(s.Freq[ch]+1) / float64(s.sampleRate)
			s.acc[ch] += step
			// Every table sample the chip would have played inside
			// this output sample, averaged -- which is what the
			// analogue output stage did to them. A high note steps
			// the table faster than the output rate, and taking
			// whichever sample the walk happened to land on folds
			// everything above half that rate back into the audible
			// band as hiss. The PSG has always averaged; the SCC
			// did not.
			acc, n := 0.0, 0
			for s.acc[ch] >= 1 {
				s.pos[ch] = (s.pos[ch] + 1) & 31
				acc += float64(s.waveOf(ch)[s.pos[ch]])
				n++
				s.acc[ch]--
			}
			if n > 0 {
				s.last[ch] = acc / float64(n)
			}
			sum += s.last[ch] * float64(s.Vol[ch])
		}
		// Five channels of +-128 at volume 15 is +-9600; scaling by
		// two keeps it in the same range as the PSG without clipping.
		v := int(sum * 2)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		buf[i] = addClamped(buf[i], int16(v))
		buf[i+1] = buf[i]
	}
}

func addClamped(a, b int16) int16 {
	v := int(a) + int(b)
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}
