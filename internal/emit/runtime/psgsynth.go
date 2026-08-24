// Synthesising the AY-3-8910 the cartridge drives.
//
// The sound driver is already running -- it is part of the translated code,
// and it writes chip registers exactly as it did on the MSX. What was missing
// is the chip those writes land on. Synth is that chip and nothing more: it
// takes register writes in and produces samples out, with no knowledge of the
// game and no dependency on whatever plays them.
//
// It is separate from the PSG in psg.go, which is the register file the
// translated code writes to. That one records; this one sounds.
package z80

import "math"

// MSX clock: the 3.579545 MHz colour-burst crystal divided by two.
const Clock = 1789772.5

// Internal tick rate. The published AY-3-8910 tone frequency is
// clock/(16*TP), and the counter *toggles* the output every TP ticks -- so a
// full square-wave period is 2*TP ticks and the ticks must run at clock/8,
// not clock/16. Getting this wrong puts every note exactly one octave out.
//
// Two independent anchors confirm it: TP=254 gives 440.4 Hz (A4), and the
// MSX note table's lowest entry, 0D5Dh = 3421, gives 32.70 Hz (C1) exactly.
const toneDivider = 8

// volumeTable is the AY-3-8910's 16-step logarithmic amplitude curve,
// normalised. The steps are roughly 3 dB apart, which is why a linear ramp
// sounds wrong.
var volumeTable = [16]float64{
	0.0000, 0.0137, 0.0205, 0.0291, 0.0423, 0.0618, 0.0847, 0.1369,
	0.1691, 0.2647, 0.3527, 0.4499, 0.5704, 0.6873, 0.8482, 1.0000,
}

// Synth is one AY-3-8910.
type Synth struct {
	reg [16]byte

	// Per-channel tone generators.
	toneCount [3]int
	toneState [3]bool

	// Noise generator: a 17-bit LFSR tapped at bits 0 and 3.
	noiseCount int
	noiseLFSR  uint32

	// Envelope generator.
	envCount   int
	envStep    int
	envAtt     bool
	envHolding bool

	sampleRate int
	acc        float64 // fractional chip ticks carried between samples

	// The chip's output is unipolar -- three channels summed, never
	// negative -- so it carries a large DC offset that would waste
	// headroom and thump the speakers. A one-pole high-pass removes it,
	// which is what the analogue output stage did.
	dcPrevIn  float64
	dcPrevOut float64

	// last holds the most recent averaged chip level, for the rare output
	// sample that spans no chip tick at all.
	last float64
}

func NewSynth(sampleRate int) *Synth {
	return &Synth{sampleRate: sampleRate, noiseLFSR: 1}
}

// Write sets a register, exactly as `out (a1h),a` would.
func (p *Synth) Write(reg, v byte) {
	if reg > 15 {
		return
	}
	p.reg[reg] = v
	if reg == 13 { // writing the shape restarts the envelope
		p.envStep = 0
		p.envCount = 0
		p.envHolding = false
		p.envAtt = v&0x04 != 0
	}
}

func (p *Synth) tonePeriod(ch int) int {
	n := int(p.reg[ch*2]) | int(p.reg[ch*2+1]&0x0F)<<8
	if n == 0 {
		return 1
	}
	return n
}

func (p *Synth) noisePeriod() int {
	if n := int(p.reg[6] & 0x1F); n != 0 {
		return n
	}
	return 1
}

func (p *Synth) envPeriod() int {
	if n := int(p.reg[11]) | int(p.reg[12])<<8; n != 0 {
		return n
	}
	return 1
}

// step advances the chip by one tone tick (clock/16).
func (p *Synth) step() {
	for ch := 0; ch < 3; ch++ {
		p.toneCount[ch]++
		if p.toneCount[ch] >= p.tonePeriod(ch) {
			p.toneCount[ch] = 0
			p.toneState[ch] = !p.toneState[ch]
		}
	}

	// Noise and envelope keep their published rates, so their thresholds
	// double along with the tick rate.
	p.noiseCount++
	if p.noiseCount >= 2*p.noisePeriod() {
		p.noiseCount = 0
		bit := (p.noiseLFSR ^ (p.noiseLFSR >> 3)) & 1
		p.noiseLFSR = (p.noiseLFSR >> 1) | (bit << 16)
	}

	// The envelope steps at clock/(256*EP).
	p.envCount++
	if p.envCount >= p.envPeriod()*32 {
		p.envCount = 0
		p.envAdvance()
	}
}

// envAdvance implements the four shape bits: hold, alternate, attack,
// continue.
func (p *Synth) envAdvance() {
	if p.envHolding {
		return
	}
	p.envStep++
	if p.envStep <= 15 {
		return
	}
	p.envStep = 0
	shape := p.reg[13]
	cont := shape&0x08 != 0
	alt := shape&0x02 != 0
	hold := shape&0x01 != 0

	if !cont {
		// Without continue the envelope runs once and sits at silence.
		p.envHolding = true
		p.envAtt = false
		p.envStep = 0
		return
	}
	if alt {
		p.envAtt = !p.envAtt
	}
	if hold {
		p.envHolding = true
		if alt {
			p.envStep = 0
		} else {
			p.envStep = 15
		}
	}
}

func (p *Synth) envVolume() int {
	v := p.envStep
	if !p.envAtt {
		v = 15 - v
	}
	if p.envHolding {
		shape := p.reg[13]
		if shape&0x08 == 0 {
			return 0
		}
	}
	return v
}

// sample mixes the three channels at the current chip state.
func (p *Synth) sample() float64 {
	mixer := p.reg[7]
	var out float64
	for ch := 0; ch < 3; ch++ {
		toneOn := mixer&(1<<uint(ch)) == 0
		noiseOn := mixer&(1<<uint(ch+3)) == 0

		// Both disabled means a constant level, which the driver uses to
		// play envelope-shaped percussion.
		lvl := true
		if toneOn {
			lvl = p.toneState[ch]
		}
		if noiseOn {
			lvl = lvl && p.noiseLFSR&1 != 0
		}
		if !toneOn && !noiseOn {
			lvl = true
		}
		if !lvl {
			continue
		}

		v := p.reg[8+ch]
		amp := 0.0
		if v&0x10 != 0 {
			amp = volumeTable[p.envVolume()]
		} else {
			amp = volumeTable[v&0x0F]
		}
		out += amp
	}
	return out / 3
}

// dcBlock is a one-pole high-pass at a few Hz: it removes the offset without
// touching anything in the audible band.
func (p *Synth) dcBlock(x float64) float64 {
	const r = 0.999
	y := x - p.dcPrevIn + r*p.dcPrevOut
	p.dcPrevIn, p.dcPrevOut = x, y
	return y
}

// Synthesize fills buf with signed 16-bit stereo samples (the format Ebiten's
// audio package expects) and advances the chip.
//
// The chip changes state at clock/16 = 111.9 kHz, about 2.5 times per output
// sample at 44.1 kHz. Point-sampling that would quantise every square-wave
// edge to the output grid, which detunes high notes and aliases their
// harmonics down into the audible band. Averaging the chip's output over the
// ticks that fall inside each sample is a box filter at the chip's own
// resolution, and costs nothing extra.
func (p *Synth) Synthesize(buf []int16) {
	ticksPerSample := Clock / toneDivider / float64(p.sampleRate)
	for i := 0; i+1 < len(buf); i += 2 {
		p.acc += ticksPerSample
		sum, n := 0.0, 0
		for p.acc >= 1 {
			p.step()
			sum += p.sample()
			n++
			p.acc--
		}
		if n > 0 {
			p.last = sum / float64(n)
		}
		v := int16(math.Round(clamp(p.dcBlock(p.last)) * 20000))
		buf[i] = v
		buf[i+1] = v
	}
}

func clamp(v float64) float64 {
	if v > 1 {
		return 1
	}
	if v < -1 {
		return -1
	}
	return v
}

// ToneFrequency reports the frequency a channel is currently producing, which
// is what makes the note tables testable.
func (p *Synth) ToneFrequency(ch int) float64 {
	return Clock / (16 * float64(p.tonePeriod(ch)))
}
