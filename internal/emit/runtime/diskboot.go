package z80

// Booting a disk.
//
// A disk game does not have an INIT the way a cartridge does. Disk BASIC
// boots, runs AUTOEXEC.BAS, and that program loads the machine code and jumps
// to it. King's Valley Plus is typical of the whole era:
//
//	AUTOEXEC.BAS  POKE-1,... : LOAD"kvalleyp.bas",R
//	KVALLEYP.BAS  1 CLS:KEY OFF:COLOR15,1,1:SCREEN2,2,0
//	              2 BLOAD"kvalleyp.001",S
//	              3 FOR I=0 TO 1500:NEXT I
//	             10 FOR T=0 TO 2000: VPOKE T,0: NEXT
//	             40 BLOAD"king.usr",R
//
// So booting a disk means running that much BASIC, and no more: load some
// files, set a screen mode, jump. This interprets the tokenised program
// directly -- there is no BASIC ROM here to run it, and there is not going to
// be one, since the whole point of msx2go is a machine with no ROMs in it.
//
// What is supported is the loader vocabulary and nothing beyond it. Anything
// else stops the boot with the statement named, because a loader that quietly
// skips a line it does not understand hands the game a machine that is not
// set up the way the program asked, and the failure surfaces later as
// something that looks like a translation bug.

import (
	"fmt"
	"strings"
)

// BootDisk boots a disk image: it runs the BASIC loader until that loader
// hands control to machine code, then leaves the machine ready to run from
// there, exactly as Boot leaves a cartridge.
//
// errTookOver says a BLOAD ,R ran a program that never returned: the machine
// is that program now, and there is no loader to go back to.
var errTookOver = fmt.Errorf("z80: the loaded program has taken over")

// start names the program to begin with, or is empty to work it out.
func (m *M) BootDisk(d *Disk, start string) error {
	m.InstallSystemBytes()
	m.IFF = false
	m.IM = 1
	m.SP = 0xF380
	if len(m.images) == 0 || m.images[0] != d {
		m.Disk = d
		m.AddDisk(d)
		m.Insert(0, len(m.images)-1)
	}
	m.syncDisk()

	if start == "" {
		start = m.DiskRun
	}
	if start == "" {
		var err error
		if start, err = d.bootProgram(); err != nil {
			return err
		}
	}
	b := &basic{m: m, d: d, vars: map[string]float64{}}
	for hops := 0; hops < 16; hops++ {
		prog, ok := d.Open(start)
		if !ok {
			return fmt.Errorf("z80: %s is not on this disk", start)
		}
		if len(prog) == 0 || prog[0] != 0xFF {
			return fmt.Errorf("z80: %s is not a tokenised BASIC program", start)
		}
		b.chain = ""
		if err := b.run(prog); err == errTookOver {
			// The program is already running -- runEntry's job is
			// done bar the classification. Give it the take-over
			// hook if it installed one, and the interrupts the
			// BIOS would enable.
			if m.Mem[hStke] != 0xC9 {
				m.run(hStke)
			}
			if !m.MainThread {
				m.IFF = true
			}
			return nil
		} else if err != nil {
			return err
		}
		if b.entry >= 0 {
			// From here a disk program is a cartridge that happens
			// to live in RAM: the same idle-loop and main-thread
			// machinery decides its shape.
			return m.runEntry(uint16(b.entry), "the loaded program")
		}
		if b.chain == "" {
			// The program may have started something all the same: a
			// BLOAD ,R that installed an interrupt hook and returned
			// is the handler shape, running from the hook while
			// BASIC idles underneath -- Breaker's game program does
			// exactly that. Only a program that left nothing behind
			// has finished without starting anything.
			if _, hooked := m.InterruptEntry(); hooked {
				m.IFF = true
				return nil
			}
			if m.Mem[hStke] != 0xC9 {
				m.run(hStke)
				m.IFF = true
				return nil
			}
			return fmt.Errorf("z80: %s finished without starting anything", start)
		}
		start = b.chain
	}
	return fmt.Errorf("z80: the disk's BASIC programs chain without end")
}

// basic is the loader interpreter. It holds no more state than the loaders
// need: numeric variables, a FOR stack, and where it was told to go next.
type basic struct {
	m    *M
	d    *Disk
	p    []byte // the tokenised program
	pc   int    // where in it
	vars map[string]float64
	fors []forFrame

	entry int    // where BLOAD ,R said to start machine code, or -1
	chain string // what LOAD ,R said to run next
}

// forFrame is one open FOR: where its body starts and how far it counts.
type forFrame struct {
	name     string
	to, step float64
	body     int
}

// line is one program line: its number and where its tokens begin.
type line struct {
	num, at int
}

func (b *basic) run(prog []byte) error {
	b.p, b.entry = prog, -1
	lines := b.index()
	if len(lines) == 0 {
		return fmt.Errorf("z80: the BASIC program has no lines")
	}
	b.pc = lines[0].at
	for steps := 0; steps < 10_000_000; steps++ {
		b.spaces()
		if b.pc >= len(b.p) || b.p[b.pc] == 0 {
			// End of a line: the next one follows its terminator,
			// after the four bytes of link and line number.
			nxt := b.pc + 5
			if b.pc+1 >= len(b.p) || nxt >= len(b.p) ||
				b.p[b.pc+1] == 0 && b.p[b.pc+2] == 0 {
				return nil
			}
			b.pc = nxt
			continue
		}
		if b.p[b.pc] == ':' {
			b.pc++
			continue
		}
		if err := b.statement(lines); err != nil {
			return err
		}
		if b.entry >= 0 || b.chain != "" {
			return nil
		}
	}
	return fmt.Errorf("z80: the BASIC loader is not finishing")
}

// index walks the line links so that GOTO can find a line by number.
// index lists the program's lines.
//
// Each line begins with a two-byte pointer to the next one, then its number,
// then the tokens, then a zero. The pointers are addresses in the machine
// where the program would live -- 8000h plus the offset in the file, for a
// program saved the ordinary way -- so following them lands exactly on each
// line.
//
// Hunting for the terminating zero instead does not work, because a tokenised
// constant can contain one: the line number in `RUN 10` is 0E 0A 00 and the
// byte in `POKE &HF6C3,&H90` is 0C 90 00. Breaker's loader has both on its
// first line, so the index went out of step immediately and the RUN that
// followed was told the program had no line 10. The zero-hunt is kept as a
// fallback for a program whose pointers do not look like a chain.
func (b *basic) index() []line {
	if out := b.indexByLinks(); out != nil {
		return out
	}
	return b.indexByZeros()
}

// indexByLinks follows the chain, and reports nil if it does not hold
// together -- every pointer moving forward, inside the program, and landing
// where the previous line's terminator says it should.
func (b *basic) indexByLinks() []line {
	const base = 0x8000 // where a saved BASIC program is taken to start
	var out []line
	i := 1
	for i+4 <= len(b.p) {
		link := int(b.p[i]) | int(b.p[i+1])<<8
		if link == 0 {
			return out // the end of the chain, properly reached
		}
		next := link - base
		if next <= i+4 || next > len(b.p) {
			return nil // not a chain
		}
		if b.p[next-1] != 0 {
			return nil // the line before it did not end
		}
		out = append(out, line{int(b.p[i+2]) | int(b.p[i+3])<<8, i + 4})
		i = next
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (b *basic) indexByZeros() []line {
	var out []line
	i := 1
	for i+4 <= len(b.p) {
		if b.p[i] == 0 && b.p[i+1] == 0 {
			break
		}
		num := int(b.p[i+2]) | int(b.p[i+3])<<8
		out = append(out, line{num, i + 4})
		// Walk to this line's terminating zero.
		j := i + 4
		for j < len(b.p) && b.p[j] != 0 {
			if b.p[j] == '"' { // a string may hold a zero byte? no -- but it may hold ':'
				j++
				for j < len(b.p) && b.p[j] != '"' {
					j++
				}
			}
			j++
		}
		i = j + 1
	}
	return out
}

func (b *basic) spaces() {
	for b.pc < len(b.p) && b.p[b.pc] == ' ' {
		b.pc++
	}
}

func (b *basic) at() byte {
	if b.pc >= len(b.p) {
		return 0
	}
	return b.p[b.pc]
}

// eat consumes one byte if it is the one expected.
func (b *basic) eat(c byte) bool {
	b.spaces()
	if b.at() == c {
		b.pc++
		return true
	}
	return false
}

func (b *basic) statement(lines []line) error {
	b.spaces()
	c := b.at()
	switch c {
	case 0x8F, 0xE6: // REM, '
		for b.pc < len(b.p) && b.p[b.pc] != 0 {
			b.pc++
		}
		return nil
	case 0x81, 0x90: // END, STOP
		b.pc = len(b.p)
		return nil
	case 0x9F: // CLS
		b.pc++
		for i := 0; i < 0x300; i++ {
			b.m.VDP.VRAM[i] = 0
		}
		return nil
	case 0xCC: // KEY -- the function key display, which we do not draw
		b.pc++
		b.skipStatement()
		return nil
	case 0x91: // PRINT: a loader's messages, which nothing reads
		b.pc++
		b.skipStatement()
		return nil
	case 0xBD: // COLOR foreground, background, border
		b.pc++
		return b.color()
	case 0xC5: // SCREEN mode, sprite size, click
		b.pc++
		return b.screen()
	case 0xC6: // VPOKE address, value
		b.pc++
		a, err := b.expr()
		if err != nil {
			return err
		}
		if !b.eat(',') {
			return fmt.Errorf("z80: VPOKE wants two arguments")
		}
		v, err := b.expr()
		if err != nil {
			return err
		}
		b.m.VDP.VRAM[b.m.VDP.phys(int(a))] = byte(int(v))
		return nil
	case 0x98: // POKE address, value
		b.pc++
		a, err := b.expr()
		if err != nil {
			return err
		}
		if !b.eat(',') {
			return fmt.Errorf("z80: POKE wants two arguments")
		}
		v, err := b.expr()
		if err != nil {
			return err
		}
		b.m.Mem[uint16(int(a))] = byte(int(v))
		return nil
	case 0xCF: // BLOAD "file" [,S] [,R]
		b.pc++
		return b.bload()
	case 0xB5: // LOAD "file",R
		b.pc++
		return b.load()
	case 0x8A: // RUN, which has three forms and not one
		b.pc++
		b.spaces()
		switch {
		case b.at() == '"':
			// RUN "file": load it and run it.
			return b.load()
		case b.at() == 0 || b.at() == ':':
			// Bare RUN: start again at the first line.
			if len(lines) > 0 {
				b.pc = lines[0].at
			}
			return nil
		default:
			// RUN <line>: carry on from there. Breaker's loader
			// pokes a byte and then does exactly this, and a
			// machine that only knew the filename form read the
			// line number as a missing quote and gave up on the
			// disk.
			return b.goto_(lines)
		}
	case 0xD2: // SET, of which only SET PAGE matters to a loader
		b.pc++
		b.spaces()
		var word strings.Builder
		for c := b.at(); c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'; c = b.at() {
			word.WriteByte(c)
			b.pc++
		}
		if !strings.EqualFold(word.String(), "PAGE") {
			return fmt.Errorf("z80: SET %s is not something booting a "+
				"disk implements", word.String())
		}
		// SET PAGE display, active: the second number is the page the
		// next BLOAD ,S lands in. Breaker's loader uses it to put one
		// compressed picture in page 1 and another in page 3, and a
		// machine that ignored it loaded every picture over the same
		// page 0 -- the decompressor then read zeroes for ever, which
		// is where the title screen went.
		vals, err := b.args(2)
		if err != nil {
			return err
		}
		if vals[0].ok {
			b.m.Mem[dpPage] = byte(int(vals[0].v))
			// The display page goes into register 2, the way the
			// BIOS puts it there for the four-page screens.
			if b.m.Mem[scrMod] >= 5 && b.m.Mem[scrMod] < 7 {
				b.m.VDP.WriteReg(2, byte(int(vals[0].v))<<5|0x1F)
			}
		}
		if vals[1].ok {
			b.m.Mem[acPage] = byte(int(vals[1].v))
		}
		return nil
	case 0x82: // FOR
		b.pc++
		return b.forStmt()
	case 0x83: // NEXT
		b.pc++
		return b.nextStmt()
	case 0x89: // GOTO
		b.pc++
		return b.goto_(lines)
	case 0x88: // LET
		b.pc++
		return b.assign()
	}
	if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
		return b.assign()
	}
	return fmt.Errorf("z80: the disk's BASIC loader uses %s, which booting a "+
		"disk does not implement", tokenName(c, b.after()))
}

// after is the byte following the current one, for naming two-byte tokens.
func (b *basic) after() byte {
	if b.pc+1 < len(b.p) {
		return b.p[b.pc+1]
	}
	return 0
}

// skipStatement runs to the next ':' or the end of the line.
func (b *basic) skipStatement() {
	for b.pc < len(b.p) && b.p[b.pc] != 0 && b.p[b.pc] != ':' {
		if b.p[b.pc] == '"' {
			b.pc++
			for b.pc < len(b.p) && b.p[b.pc] != '"' {
				b.pc++
			}
		}
		b.pc++
	}
}

// color sets the three BIOS colour bytes and register 7, as CHGCLR does.
func (b *basic) color() error {
	vals, err := b.args(3)
	if err != nil {
		return err
	}
	for i, addr := range []uint16{ForClr, BakClr, BdrClr} {
		if vals[i].ok {
			b.m.Mem[addr] = byte(int(vals[i].v)) & 0x0F
		}
	}
	b.m.VDP.WriteReg(7, b.m.Mem[ForClr]&0x0F<<4|b.m.Mem[BdrClr]&0x0F)
	return nil
}

// screenTables is what the BIOS points the VDP at for each of the tile modes
// it can set from BASIC: name, colour, pattern, sprite attributes, sprite
// patterns. A loader that says SCREEN 2 and then fills video memory is
// relying on exactly these addresses.
var screenTables = map[int]struct{ name, colour, pat, sprAttr, sprPat int }{
	0: {0x0000, 0, 0x0800, 0, 0},
	1: {0x1800, 0x2000, 0x0000, 0x1B00, 0x3800},
	2: {0x1800, 0x2000, 0x0000, 0x1B00, 0x3800},
	3: {0x0800, 0, 0x0000, 0x1B00, 0x3800},
}

// screen sets a screen mode the way the BIOS does.
func (b *basic) screen() error {
	vals, err := b.args(3)
	if err != nil {
		return err
	}
	if !vals[0].ok {
		return nil
	}
	mode := int(vals[0].v)
	t, known := screenTables[mode]
	if !known {
		return fmt.Errorf("z80: this loader asks for SCREEN %d, which "+
			"booting a disk does not set up", mode)
	}
	b.m.Mem[ScrMod] = byte(mode)
	r0, r1 := byte(0x00), byte(0xE0)
	switch mode {
	case 0:
		r0, r1 = 0x00, 0xF0
	case 1:
		r0, r1 = 0x00, 0xE0
	case 2:
		r0, r1 = 0x02, 0xE0
	case 3:
		r0, r1 = 0x00, 0xE8
	}
	// The second argument is the sprite size: 0 and 1 are eight by eight,
	// 2 and 3 sixteen, and the odd ones are magnified.
	if vals[1].ok {
		switch int(vals[1].v) {
		case 1:
			r1 |= 0x01
		case 2:
			r1 |= 0x02
		case 3:
			r1 |= 0x03
		}
	}
	b.m.VDP.WriteReg(0, r0)
	b.m.VDP.WriteReg(1, r1)
	b.m.VDP.WriteReg(2, byte(t.name/0x400))
	if mode == 2 {
		b.m.VDP.WriteReg(3, 0xFF)
		b.m.VDP.WriteReg(4, 0x03)
	} else {
		b.m.VDP.WriteReg(3, byte(t.colour/0x40))
		b.m.VDP.WriteReg(4, byte(t.pat/0x800))
	}
	b.m.VDP.WriteReg(5, byte(t.sprAttr/0x80))
	b.m.VDP.WriteReg(6, byte(t.sprPat/0x800))
	b.m.VDP.WriteReg(7, b.m.Mem[ForClr]&0x0F<<4|b.m.Mem[BdrClr]&0x0F)
	return nil
}

// bload loads a binary. Its first seven bytes say where it goes: FEh, then
// the start, end and execution addresses. ",S" sends it to video memory
// instead of main memory, and ",R" says to run it when it lands.
func (b *basic) bload() error {
	name, err := b.str()
	if err != nil {
		return err
	}
	toVRAM, run := false, false
	for b.eat(',') {
		b.spaces()
		switch c := b.at(); {
		case c == 'S' || c == 's':
			toVRAM = true
			b.pc++
		case c == 'R' || c == 'r':
			run = true
			b.pc++
		default:
			// An offset, which only matters for tape.
			if _, err := b.expr(); err != nil {
				return err
			}
		}
	}
	data, ok := b.d.Open(name)
	if !ok {
		return fmt.Errorf("z80: BLOAD %q, which is not on this disk", name)
	}
	if len(data) < 7 || data[0] != 0xFE {
		return fmt.Errorf("z80: %s has no BLOAD header", name)
	}
	start := int(data[1]) | int(data[2])<<8
	end := int(data[3]) | int(data[4])<<8
	exec := int(data[5]) | int(data[6])<<8
	body := data[7:]
	if n := end - start + 1; n > 0 && n < len(body) {
		body = body[:n]
	}
	if toVRAM {
		// Through the same page arithmetic every video call uses.
		base := b.m.vramBase()
		for i, v := range body {
			b.m.VDP.VRAM[b.m.VDP.phys(base+start+i)] = v
		}
	} else {
		for i, v := range body {
			b.m.Mem[uint16(start+i)] = v
		}
		// Remember where code landed: the union of every BLOAD into
		// RAM is the region a disk's translation is made from and
		// checked against. See Info.TransSHA1.
		if b.m.loadHi == 0 || start < int(b.m.loadLo) {
			b.m.loadLo = uint16(start)
		}
		if end := start + len(body) - 1; end > int(b.m.loadHi) {
			b.m.loadHi = uint16(end)
		}
	}
	if run {
		if exec == 0 {
			exec = start
		}
		// BASIC runs it here and now, and carries on with the next
		// line when it returns. Breaker's title program is exactly
		// that: BLOADed mid-loader with ,R, it sets the screen up and
		// hands straight back, and the lines after it load the
		// pictures. Deferring every ,R to the end of the program ran
		// the wrong one, last, on a machine set up by nobody.
		m := b.m
		m.booting = true
		m.VDP.BootVblank = true
		sp0 := m.SP
		m.run(uint16(exec))
		for halts := 0; m.halted && !m.idle && !m.MainThread; halts++ {
			if halts >= 8 {
				m.MainThread = true
				break
			}
			m.halted = false
			m.bootInterrupt()
			m.Interpret(0, maxInterpSteps)
			if m.bootStop {
				m.bootStop = false
				break
			}
		}
		m.booting = false
		m.VDP.BootVblank = false
		// A clean return is the sentinel popped by a ret with the
		// stack back where it started. The address alone is not
		// enough: the sentinel is FFFFh, and a program that runs away
		// through empty memory *walks* to FFFFh executing nops, stack
		// anywhere at all.
		if m.PC == sentinel && m.SP == sp0 &&
			!m.idle && !m.halted && !m.MainThread {
			return nil // it returned; the loader continues
		}
		// It never came back: this program owns the machine now.
		b.entry = exec
		return errTookOver
	}
	return nil
}

// load chains to another BASIC program, which is how a loader splits itself
// across files. Without ",R" it would only be read in, and a loader that does
// that and stops is not one we can carry on from.
func (b *basic) load() error {
	name, err := b.str()
	if err != nil {
		return err
	}
	for b.eat(',') {
		b.spaces()
		if c := b.at(); c == 'R' || c == 'r' {
			b.pc++
		}
	}
	b.chain = name
	return nil
}

func (b *basic) forStmt() error {
	name, err := b.varName()
	if err != nil {
		return err
	}
	if !b.eat(0xEF) { // =
		return fmt.Errorf("z80: FOR without =")
	}
	from, err := b.expr()
	if err != nil {
		return err
	}
	if !b.eat(0xD9) { // TO
		return fmt.Errorf("z80: FOR without TO")
	}
	to, err := b.expr()
	if err != nil {
		return err
	}
	step := 1.0
	if b.eat(0xDC) { // STEP
		if step, err = b.expr(); err != nil {
			return err
		}
	}
	b.vars[name] = from
	b.fors = append(b.fors, forFrame{name: name, to: to, step: step, body: b.pc})
	return nil
}

func (b *basic) nextStmt() error {
	// The variable after NEXT is optional and, when several loops are
	// closed at once, comma-separated.
	b.spaces()
	if c := b.at(); c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
		if _, err := b.varName(); err != nil {
			return err
		}
	}
	if len(b.fors) == 0 {
		return fmt.Errorf("z80: NEXT without FOR")
	}
	f := &b.fors[len(b.fors)-1]
	b.vars[f.name] += f.step
	v := b.vars[f.name]
	if f.step > 0 && v <= f.to || f.step < 0 && v >= f.to {
		b.pc = f.body
		return nil
	}
	b.fors = b.fors[:len(b.fors)-1]
	return nil
}

func (b *basic) goto_(lines []line) error {
	n, err := b.expr()
	if err != nil {
		return err
	}
	for _, l := range lines {
		if l.num == int(n) {
			b.pc = l.at
			return nil
		}
	}
	return fmt.Errorf("z80: GOTO %d, which the program has no line for", int(n))
}

func (b *basic) assign() error {
	name, err := b.varName()
	if err != nil {
		return err
	}
	if !b.eat(0xEF) { // =
		return fmt.Errorf("z80: %s is not a statement this understands", name)
	}
	v, err := b.expr()
	if err != nil {
		return err
	}
	b.vars[name] = v
	return nil
}

// optVal is one argument of a statement whose arguments may be left out, the
// way COLOR and SCREEN allow.
type optVal struct {
	v  float64
	ok bool
}

// args reads up to n comma-separated arguments, any of which may be empty.
func (b *basic) args(n int) ([]optVal, error) {
	out := make([]optVal, n)
	for i := 0; i < n; i++ {
		b.spaces()
		if c := b.at(); c != ',' && c != 0 && c != ':' {
			v, err := b.expr()
			if err != nil {
				return nil, err
			}
			out[i] = optVal{v, true}
		}
		if !b.eat(',') {
			break
		}
	}
	return out, nil
}

// str reads a quoted string.
func (b *basic) str() (string, error) {
	b.spaces()
	if !b.eat('"') {
		// Say what it saw, not just that it was unhappy: the byte, the
		// token's name if it has one, and the few bytes around it.
		// A loader that stops here needs the statement identified
		// before anything can be done about it.
		return "", fmt.Errorf("z80: this loader names a file some way "+
			"other than with a quoted string: at offset %d it has "+
			"%02Xh (%s), context % 02X",
			b.pc, b.at(), tokenName(b.at(), b.peek(1)), b.around())
	}
	var sb strings.Builder
	for b.pc < len(b.p) && b.p[b.pc] != '"' && b.p[b.pc] != 0 {
		sb.WriteByte(b.p[b.pc])
		b.pc++
	}
	b.eat('"')
	return sb.String(), nil
}

// peek is the byte n along from where the interpreter is.
func (b *basic) peek(n int) byte {
	if b.pc+n >= len(b.p) {
		return 0
	}
	return b.p[b.pc+n]
}

// around is the bytes either side of where the interpreter is, for an error
// message that can be acted on.
func (b *basic) around() []byte {
	lo, hi := b.pc-6, b.pc+10
	if lo < 0 {
		lo = 0
	}
	if hi > len(b.p) {
		hi = len(b.p)
	}
	return b.p[lo:hi]
}

// varName reads a variable's name, with any type suffix.
func (b *basic) varName() (string, error) {
	b.spaces()
	c := b.at()
	if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z') {
		return "", fmt.Errorf("z80: a variable name was expected")
	}
	var sb strings.Builder
	for b.pc < len(b.p) {
		c := b.p[b.pc]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			sb.WriteByte(c)
			b.pc++
			continue
		}
		if c == '%' || c == '!' || c == '#' || c == '$' {
			b.pc++
		}
		break
	}
	return strings.ToUpper(sb.String()), nil
}

func (b *basic) expr() (float64, error) { return b.orExpr() }

func (b *basic) orExpr() (float64, error) {
	v, err := b.andExpr()
	for err == nil && b.eat(0xF7) { // OR
		var r float64
		r, err = b.andExpr()
		v = float64(int(v) | int(r))
	}
	return v, err
}

func (b *basic) andExpr() (float64, error) {
	v, err := b.cmpExpr()
	for err == nil && b.eat(0xF6) { // AND
		var r float64
		r, err = b.cmpExpr()
		v = float64(int(v) & int(r))
	}
	return v, err
}

func (b *basic) cmpExpr() (float64, error) {
	v, err := b.addExpr()
	if err != nil {
		return v, err
	}
	b.spaces()
	// The relational operators are separate tokens, and BASIC writes the
	// compound ones as two: >= is > then =.
	var gt, lt, eq bool
	for {
		switch b.at() {
		case 0xEE:
			gt = true
		case 0xF0:
			lt = true
		case 0xEF:
			eq = true
		default:
			if !gt && !lt && !eq {
				return v, nil
			}
			r, err := b.addExpr()
			if err != nil {
				return v, err
			}
			// BASIC's true is -1.
			if gt && v > r || lt && v < r || eq && v == r {
				return -1, nil
			}
			return 0, nil
		}
		b.pc++
	}
}

func (b *basic) addExpr() (float64, error) {
	v, err := b.mulExpr()
	for err == nil {
		switch {
		case b.eat(0xF1): // +
			var r float64
			r, err = b.mulExpr()
			v += r
		case b.eat(0xF2): // -
			var r float64
			r, err = b.mulExpr()
			v -= r
		default:
			return v, err
		}
	}
	return v, err
}

func (b *basic) mulExpr() (float64, error) {
	v, err := b.unary()
	for err == nil {
		switch {
		case b.eat(0xF3): // *
			var r float64
			r, err = b.unary()
			v *= r
		case b.eat(0xF4): // /
			var r float64
			r, err = b.unary()
			if r != 0 {
				v /= r
			}
		default:
			return v, err
		}
	}
	return v, err
}

func (b *basic) unary() (float64, error) {
	b.spaces()
	if b.eat(0xF2) { // unary minus
		v, err := b.unary()
		return -v, err
	}
	if b.eat(0xE0) { // NOT, which in BASIC is the ones' complement
		v, err := b.unary()
		return float64(^int16(int(v))), err
	}
	return b.primary()
}

func (b *basic) primary() (float64, error) {
	b.spaces()
	c := b.at()
	switch {
	case c == '(':
		b.pc++
		v, err := b.expr()
		if err != nil {
			return 0, err
		}
		b.eat(')')
		return v, nil
	case c == 0xFF: // a function: the second byte says which
		b.pc++
		fn := b.at()
		b.pc++
		if !b.eat('(') {
			return 0, fmt.Errorf("z80: %s without its argument",
				tokenName(0xFF, fn))
		}
		v, err := b.expr()
		if err != nil {
			return 0, err
		}
		b.eat(')')
		switch fn {
		case 0x96: // PEEK
			return float64(b.m.Mem[uint16(int(v))]), nil
		case 0x97: // VPEEK
			return float64(b.m.VDP.VRAM[b.m.VDP.phys(int(v))]), nil
		case 0x84: // INT
			return float64(int(v)), nil
		case 0x85: // ABS
			if v < 0 {
				return -v, nil
			}
			return v, nil
		}
		return 0, fmt.Errorf("z80: this loader calls %s, which booting a "+
			"disk does not implement", tokenName(0xFF, fn))
	case c >= '0' && c <= '9': // a digit stored as its character
		// The tokeniser leaves digits as plain text in some spots --
		// Breaker's SET PAGE 0,1 stores the 0 as 30h and the 1 as the
		// token 12h, in the same statement.
		v := 0.0
		for b.pc < len(b.p) && b.p[b.pc] >= '0' && b.p[b.pc] <= '9' {
			v = v*10 + float64(b.p[b.pc]-'0')
			b.pc++
		}
		return v, nil
	case c >= 0x11 && c <= 0x1A: // the constants zero to nine
		b.pc++
		return float64(c - 0x11), nil
	case c == 0x0F: // a byte
		b.pc++
		v := float64(b.at())
		b.pc++
		return v, nil
	case c == 0x0E: // a line number, as GOTO and RUN write one
		b.pc++
		v := uint16(b.p[b.pc]) | uint16(b.p[b.pc+1])<<8
		b.pc += 2
		return float64(v), nil
	case c == 0x1C: // a sixteen-bit integer
		b.pc++
		v := int16(uint16(b.p[b.pc]) | uint16(b.p[b.pc+1])<<8)
		b.pc += 2
		return float64(v), nil
	case c == 0x0B || c == 0x0C: // octal and hexadecimal
		b.pc++
		v := uint16(b.p[b.pc]) | uint16(b.p[b.pc+1])<<8
		b.pc += 2
		return float64(v), nil
	case c == 0x1D: // single precision
		b.pc++
		v := msxFloat(b.p[b.pc : b.pc+4])
		b.pc += 4
		return v, nil
	case c == 0x1F: // double precision
		b.pc++
		v := msxFloat(b.p[b.pc : b.pc+8])
		b.pc += 8
		return v, nil
	case c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z':
		name, err := b.varName()
		if err != nil {
			return 0, err
		}
		return b.vars[name], nil
	}
	return 0, fmt.Errorf("z80: %s is not something this can evaluate",
		tokenName(c, b.after()))
}

// msxFloat decodes MSX BASIC's decimal floating point: an exponent byte whose
// low seven bits are the power of ten in excess-64 and whose top bit is the
// sign, then two decimal digits per byte, most significant first.
func msxFloat(b []byte) float64 {
	if len(b) == 0 || b[0] == 0 {
		return 0
	}
	exp := int(b[0]&0x7F) - 64
	mant := 0.0
	scale := 0.1
	for _, d := range b[1:] {
		mant += float64(d>>4) * scale
		scale /= 10
		mant += float64(d&0x0F) * scale
		scale /= 10
	}
	v := mant
	for ; exp > 0; exp-- {
		v *= 10
	}
	for ; exp < 0; exp++ {
		v /= 10
	}
	if b[0]&0x80 != 0 {
		v = -v
	}
	return v
}

// tokens and functions name what a program asked for, so that a loader this
// does not implement says which statement stopped it rather than a byte.
var tokens = map[byte]string{
	0x81: "END", 0x82: "FOR", 0x83: "NEXT", 0x84: "DATA", 0x85: "INPUT",
	0x86: "DIM", 0x87: "READ", 0x88: "LET", 0x89: "GOTO", 0x8A: "RUN",
	0x8B: "IF", 0x8C: "RESTORE", 0x8D: "GOSUB", 0x8E: "RETURN", 0x8F: "REM",
	0x90: "STOP", 0x91: "PRINT", 0x92: "CLEAR", 0x93: "LIST", 0x94: "NEW",
	0x95: "ON", 0x96: "WAIT", 0x97: "DEF", 0x98: "POKE", 0x99: "CONT",
	0x9C: "OUT", 0x9F: "CLS", 0xA0: "WIDTH", 0xA1: "ELSE", 0xA4: "SWAP",
	0xA5: "ERASE", 0xA6: "ERROR", 0xA7: "RESUME", 0xAB: "DEFSTR",
	0xAC: "DEFINT", 0xAD: "DEFSNG", 0xAE: "DEFDBL", 0xAF: "LINE",
	0xB0: "OPEN", 0xB1: "FIELD", 0xB2: "GET", 0xB3: "PUT", 0xB4: "CLOSE",
	0xB5: "LOAD", 0xB6: "MERGE", 0xB7: "FILES", 0xB8: "LSET", 0xB9: "RSET",
	0xBA: "SAVE", 0xBC: "CIRCLE", 0xBD: "COLOR", 0xBE: "DRAW", 0xBF: "PAINT",
	0xC0: "BEEP", 0xC1: "PLAY", 0xC2: "PSET", 0xC3: "PRESET", 0xC4: "SOUND",
	0xC5: "SCREEN", 0xC6: "VPOKE", 0xC7: "SPRITE", 0xC8: "VDP", 0xC9: "BASE",
	0xCA: "CALL", 0xCB: "TIME", 0xCC: "KEY", 0xCE: "MOTOR", 0xCF: "BLOAD",
	0xD0: "BSAVE", 0xD3: "NAME", 0xD4: "KILL", 0xD6: "COPY", 0xD8: "LOCATE",
	0xD9: "TO", 0xDA: "THEN", 0xDC: "STEP", 0xDD: "USR", 0xDE: "FN",
	0xE0: "NOT", 0xE4: "USING", 0xE6: "'", 0xE7: "VARPTR", 0xEB: "OFF",
	0xEC: "INKEY$", 0xED: "POINT",
}

var functions = map[byte]string{
	0x80: "LEFT$", 0x81: "RIGHT$", 0x82: "MID$", 0x83: "SGN", 0x84: "INT",
	0x85: "ABS", 0x86: "SQR", 0x87: "RND", 0x88: "SIN", 0x89: "LOG",
	0x8A: "EXP", 0x8B: "COS", 0x8C: "TAN", 0x8D: "ATN", 0x8E: "FRE",
	0x8F: "INP", 0x90: "POS", 0x91: "LEN", 0x92: "STR$", 0x93: "VAL",
	0x94: "ASC", 0x95: "CHR$", 0x96: "PEEK", 0x97: "VPEEK", 0x98: "SPACE$",
	0x99: "OCT$", 0x9A: "HEX$", 0x9C: "BIN$", 0xA1: "STICK", 0xA2: "STRIG",
	0xA5: "DSKF", 0xAA: "EOF", 0xAB: "LOC", 0xAC: "LOF",
}

func tokenName(c, next byte) string {
	if c == 0xFF {
		if n, ok := functions[next]; ok {
			return n
		}
		return fmt.Sprintf("function FF%02Xh", next)
	}
	if n, ok := tokens[c]; ok {
		return n
	}
	if c >= 32 && c < 127 {
		return fmt.Sprintf("%q", string(c))
	}
	return fmt.Sprintf("token %02Xh", c)
}

// LoadedRange is the span of RAM the disk's loader filled with code: the
// union of every BLOAD that was not aimed at video memory. It is what a
// disk's translation reads and what the runtime hashes to know that
// translation still tells the truth.
func (m *M) LoadedRange() (lo, hi uint16, ok bool) {
	return m.loadLo, m.loadHi, m.loadHi != 0
}
