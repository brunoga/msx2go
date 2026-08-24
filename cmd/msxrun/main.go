// msxrun runs a cartridge in the interpreter and reports what it did.
//
// Two uses. As a check, -digest prints a per-frame hash of video memory, and
// the translated build of the same cartridge has to print the same hashes --
// that is the proof that the interpreter and the translation agree, and it is
// the only reason to trust what the other use produces.
//
// As discovery, -sites writes every address the cartridge actually executed,
// with the banks in force. A static trace cannot follow `jp (hl)`; running the
// game does not have to.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc32"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brunoga/msx2go/internal/dis"
	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
)

var frameMark func()

var resetCnt func()

var snsRows [16]int

func main() {
	rom := flag.String("rom", "", "cartridge image")
	frames := flag.Int("frames", 600, "frames to run")
	quota := flag.Int("quota", 400000, "instruction budget per frame")
	mapper := flag.String("mapper", "", "override the detected mapper")
	base := flag.Int("base", 0x4000, "where the cartridge is mapped")
	sites := flag.String("sites", "", "write every executed address here")
	digest := flag.Int("digest", 0, "print a digest every this many frames")
	renderDump := flag.String("renderdump", "", "render a VRAM dump (with 32 "+
		"registers appended) to -png and stop")
	vramdump := flag.String("vramdump", "", "write video memory here")
	ramdump := flag.String("ramdump", "", "write RAM from C000h out here")
	memdump := flag.String("memdump", "", "write the whole 64K address space here")
	shot := flag.String("png", "", "write the last frame here")
	tape := flag.String("tape", "", "keyboard tape to play back")
	trail := flag.Int("trail", 0, "print the first N instructions executed")
	bankwatch := flag.Int("bankwatch", -1, "report every bank switch of this "+
		"page, with the code that made it")
	bankFrom := flag.Int("bankfrom", 0, "only report bank switches from this "+
		"frame on")
	biostrace := flag.Bool("biostrace", false, "print the registers at the "+
		"first call to each VDP BIOS entry point, which is how a call "+
		"site says which routine it believes it is calling")
	guard := flag.Int("guard", -1, "report writes to this address with the "+
		"cycles spent in the frame so far, which is how a handler's cost "+
		"can be checked against a reference machine's clock")
	guardFrom := flag.Int("guardfrom", 0, "start reporting -guard at this frame")
	icount := flag.Int("icount", 0, "print instructions executed, every N frames")
	splitdbg := flag.Bool("splitdbg", false, "also render each split state whole")
	reglogN := flag.Int("reglogn", 3, "how many frames -reglog covers")
	runBas := flag.String("run", "", "the BASIC program a floppy starts "+
		"with, for a disk that has no AUTOEXEC.BAS")
	dsk := flag.String("dsk", "", "boot this disk image instead of a cartridge")
	dskList := flag.Bool("dsklist", false, "list the disk's files and stop")
	dosLog := flag.Bool("doslog", false, "report every disk function call")
	pokes := flag.String("poke", "", "frame:addr:val[,frame:addr:val...] -- "+
		"write a byte at the top of a frame, addr and val in hex")
	mwatch := flag.String("mwatch", "", "lo:hi in hex -- report every write "+
		"to this memory range, with the code that made it")
	mwatchFrom := flag.Int("mwatchfrom", 0, "frame to start -mwatch at")
	enterRAM := flag.Bool("enterram", false, "report the first instruction "+
		"executed in RAM, and what ran just before it")
	ixcensus := flag.Bool("ixcensus", false, "count EXTROM calls by the "+
		"routine in IX")
	lastpcs := flag.Bool("lastpcs", false, "print the last 64 interpreted "+
		"instruction addresses at exit, oldest first")
	irqLog := flag.Int("irqlog", 0, "report what the line interrupt does, for "+
		"three frames from here")
	irqLogN := flag.Int("irqlogn", 3, "how many frames -irqlog covers")
	irqCount := flag.Bool("irqcount", false, "with -irqlog, count the events "+
		"rather than printing each one")
	btape := flag.String("btape", "", "replay a button tape, one z80.Buttons "+
		"byte a frame, driving the joystick *and* the keyboard exactly "+
		"as the window does -- which -tape and -monkey do not")
	dskSave := flag.String("dsksave", "", "write the disk image back here if "+
		"the program changed it")
	wav := flag.String("wav", "", "write the synthesised sound here")
	wavFrom := flag.Int("wavfrom", 0, "frame to start recording sound at")
	reglog := flag.Int("reglog", 0, "log every VDP register write with its "+
		"raster line, from this frame on")
	stuck := flag.Bool("stuck", false, "name the pc a runaway frame is spinning at")
	isrtrace := flag.Int("isrtrace", 0, "print every ISR pc in this frame")
	finfo := flag.Int("finfo", 0, "print frame state for 8 frames from here")
	rst38 := flag.Bool("rst38", false, "report who executes rst 38h")
	pchist := flag.Int("pchist", 0, "histogram the PCs executed from this frame on")
	pcof := flag.String("pcof", "", "with -pchist, report the count for these "+
		"comma-separated hex addresses rather than the busiest ones")
	pctop := flag.Int("pctop", 12, "how many addresses -pchist reports")
	pcuntil := flag.Int("pcuntil", 0, "with -pchist, stop counting at this frame")
	stat := flag.Bool("stat", false, "count status-register reads by number")
	vwrites := flag.Int("vwrites", 0, "count data-port VRAM writes for 50 "+
		"frames from here, by page")
	cmds := flag.Bool("cmds", false, "report which VDP commands are used")
	cmdFrom := flag.Int("cmdfrom", -1, "log every command from this frame on")
	cmdWin := flag.Int("cmdwin", 20, "how many frames of commands to log")
	cyccmp := flag.Bool("cyccmp", false, "report opcodes the two cycle models "+
		"disagree about")
	cycles := flag.Bool("cycles", false, "print the T-states counted at the end")
	modeReport := flag.Bool("mode", false, "report the screen mode at the end")
	regs := flag.Bool("regs", false, "report which VDP registers the cartridge "+
		"writes, by the number it actually names")
	border7 := flag.Bool("border7", false, "report the border colour register")
	vbyte := flag.Int("vbyte", -1, "report every frame in which this VRAM "+
		"byte changes, however it was written")
	probe := flag.String("probe", "", "addr[,addr] -- print the registers and "+
		"the byte at (DE) each time one of these executes")
	probeN := flag.Int("probeN", 30, "how many probe hits to print")
	probeFrom := flag.Int("probefrom", 0, "ignore probe hits before this frame")
	vwatch := flag.String("vwatch", "", "lo:hi -- report which code writes "+
		"this VRAM range, with the banks in force")
	vcrc := flag.String("vcrc", "", "write a video-memory digest every -vevery "+
		"frames here, in the form ref/vframes.tcl writes")
	vevery := flag.Int("vevery", 20, "how often -vcrc writes")
	refcrc := flag.String("refcrc", "", "write one work-RAM CRC per frame here, "+
		"in the form ref/ref.tcl writes, so the two can be diffed")
	snapshot := flag.String("snapshot", "", "write a snapshot here at -snapat")
	snapAt := flag.Int("snapat", 0, "frame to snapshot at")
	resume := flag.String("resume", "", "start from this snapshot instead of "+
		"booting")
	hold := flag.String("hold", "", "hold this MSX key down from -holdfrom on: "+
		"f1-f5, ret, esc, space, stop, or a letter or digit")
	holdFrom := flag.Int("holdfrom", 0, "frame to start holding -hold")
	mainThread := flag.Bool("mainthread", false, "boot as a main-thread "+
		"cartridge, the way a generated module does when its shape says so")
	hz := flag.Int("hz", 60, "vertical frequency, 50 or 60")
	cpu := flag.Float64("cpu", 1, "processor speed as a multiple of a stock MSX")
	monkey := flag.Int64("monkey", 0, "play the game with this seed instead of "+
		"watching the demo; 0 is off")
	flag.Parse()

	// Rendering someone else's video memory, to tell a renderer that is
	// wrong from a machine that has put the wrong thing in memory.
	if *renderDump != "" {
		b, err := os.ReadFile(*renderDump)
		check(err)
		var v z80.VDP
		v.Reset()
		// The dump is video memory, then 32 registers, and optionally
		// the 16 palette entries as the chip stores them: two bytes
		// each, red and blue then green.
		extra := 32
		if len(b)%0x20000 == 64 || len(b) == 0x20000+64 {
			extra = 64
		}
		n := len(b) - extra
		if n > 0x20000 {
			n = 0x20000
		}
		if n > 0x4000 {
			v.VRAM = make([]byte, 0x20000)
			v.V9938 = true
		}
		copy(v.VRAM, b[:n])
		for r := 0; r < 32 && n+r < len(b); r++ {
			v.WriteReg(byte(r), b[n+r])
		}
		if extra == 64 {
			for i := 0; i < 16; i++ {
				v.WriteReg(16, byte(i))
				v.WritePalette(b[n+32+i*2])
				v.WritePalette(b[n+32+i*2+1])
			}
		}
		img := z80.NewRenderer().RenderVDP(&v)
		f, err := os.Create(*shot)
		check(err)
		check(png.Encode(f, img))
		check(f.Close())
		fmt.Fprintf(os.Stderr, "msxrun: rendered %s -> %s (%v, %d lines, "+
			"page %05Xh, R23=%02X)\n", *renderDump, *shot, v.Mode(),
			v.Lines(), v.PageBase(), v.Reg[23])
		return
	}

	if *rom == "" && *dsk == "" {
		fmt.Fprintln(os.Stderr, "msxrun: -rom or -dsk is required")
		os.Exit(2)
	}
	var m *z80.M
	var data []byte
	var err error
	if *dsk != "" {
		var img []byte
		img, err = os.ReadFile(*dsk)
		check(err)
		var d *z80.Disk
		d, err = z80.NewDisk(img)
		check(err)
		if *dskList {
			for _, f := range d.Files() {
				fmt.Printf("%-14s %7d\n", f.Name, f.Size)
			}
			return
		}
		// A disk machine is all RAM: no cartridge, no mapper.
		m = z80.New(nil, z80.Mapper{})
		m.Disk = d
		if *dosLog {
			m.DOSTrace = func(fn byte, de uint16) {
				name := ""
				if fn != 0x1A && fn != 0x2F && fn != 0x30 {
					name = " " + m.FCBName(de)
				}
				fmt.Fprintf(os.Stderr, "dos f%d fn=%02X de=%04X%s\n",
					m.Frames(), fn, de, name)
			}
		}
	} else {
		data, err = os.ReadFile(*rom)
		check(err)
		name := *mapper
		if name == "" {
			name = dis.DetectMapper(data)
		}
		var mp z80.Mapper
		mp, err = z80.NamedMapper(name, *base, len(data))
		check(err)
		m = z80.New(data, mp)
	}
	m.CPUScale = *cpu
	m.Hz = *hz
	m.DiskRun = *runBas
	m.MainThread = *mainThread
	// Every address executed, and the banks that were in force -- two
	// cartridge addresses can share one Z80 address, so the bank is part
	// of the answer, not decoration.
	seen := map[string]bool{}
	if *sites != "" {
		m.Observe = func(pc uint16, banks []int) {
			if pc < 0x4000 {
				return // BIOS, which is shims here and not translated
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%04X ", pc)
			for i, x := range banks {
				if i > 0 {
					b.WriteByte(',')
				}
				fmt.Fprintf(&b, "%d", x)
			}
			seen[b.String()] = true
		}
	}

	if *trail > 0 {
		n := 0
		prev := m.Observe
		m.Observe = func(pc uint16, banks []int) {
			if prev != nil {
				prev(pc, banks)
			}
			if n < *trail {
				fmt.Fprintf(os.Stderr, "%5d %04X %v  a=%02X hl=%04X sp=%04X\n",
					n, pc, banks, m.A, m.HL(), m.SP)
			}
			n++
		}
	}

	// The reference machine writes one CRC per interrupt over the game's
	// work RAM, skipping the stack page: what is on it depends on who
	// called the hook, which is C-BIOS there and a sentinel here, and
	// neither is the game. Producing the same number here turns "the
	// picture is wrong" into "frame N is the first that differs".
	var vcrcOut *os.File
	if *vcrc != "" {
		vcrcOut, err = os.Create(*vcrc)
		check(err)
		defer vcrcOut.Close()
	}

	var crcOut *os.File
	if *refcrc != "" {
		crcOut, err = os.Create(*refcrc)
		check(err)
		defer crcOut.Close()
	}

	// Who wrote this part of the screen? The interpreter knows the PC of
	// every instruction, so a VRAM write can be attributed to the code that
	// made it -- which is the question when a picture comes out half drawn.
	if *vwatch != "" {
		var lo, hi int
		if _, err := fmt.Sscanf(*vwatch, "%x:%x", &lo, &hi); err != nil {
			check(err)
		}
		pc := uint16(0)
		prev := m.Observe
		m.Observe = func(a uint16, banks []int) {
			pc = a
			if prev != nil {
				prev(a, banks)
			}
		}
		writers := map[string]int{}
		shown := 0
		m.VDP.OnWrite = func(addr uint16, b byte) {
			if int(addr) < lo || int(addr) >= hi {
				return
			}
			writers[siteOf(pc, m.Banks())]++
			if shown < *probeN {
				shown++
				de := m.DE()
				fmt.Fprintf(os.Stderr,
					"  f%d write vram %04X = %02X  from %s de=%04X (de)=%02X\n",
					m.Frames(), addr, b, siteOf(pc, m.Banks()), de, m.Mem[de])
			}
		}
		defer func() {
			if len(writers) == 0 {
				fmt.Fprintf(os.Stderr, "msxrun: nothing wrote %04X-%04X\n", lo, hi)
				return
			}
			keysOf := make([]string, 0, len(writers))
			for k := range writers {
				keysOf = append(keysOf, k)
			}
			sort.Strings(keysOf)
			for _, k := range keysOf {
				fmt.Fprintf(os.Stderr, "msxrun: %s wrote %04X-%04X %d time(s)\n",
					k, lo, hi, writers[k])
			}
		}()
	}

	if *probe != "" {
		want := map[uint16]bool{}
		for _, f := range strings.Split(*probe, ",") {
			var a int
			fmt.Sscanf(f, "%x", &a)
			want[uint16(a)] = true
		}
		hits := 0
		prev := m.Observe
		m.Observe = func(a uint16, banks []int) {
			if prev != nil {
				prev(a, banks)
			}
			if want[a] && hits < *probeN && m.Frames() >= *probeFrom {
				hits++
				de := m.DE()
				fmt.Fprintf(os.Stderr,
					"f%d probe %04X banks %v de=%04X hl=%04X bc=%04X a=%02X ret=%04X\n",
					m.Frames(), a, banks, de, m.HL(), m.BC(), m.A,
					uint16(m.Mem[m.SP])|uint16(m.Mem[m.SP+1])<<8)
			}
		}
	}

	if *biostrace {
		nCall := 0
		defer func() {
			for r, n := range snsRows {
				if n > 0 {
					fmt.Fprintf(os.Stderr, "SNSMAT row %d asked %d times\n", r, n)
				}
			}
		}()
		prevBios0 := m.BiosTrace
		m.BiosTrace = func(addr uint16) {
			if prevBios0 != nil {
				prevBios0(addr)
			}
			if addr == 0x0141 {
				snsRows[m.A&0x0F]++
				return
			}
			// Every entry, not a window of them. Filtering to
			// 40h..62h made this look like a machine that calls
			// three routines and nothing else, and a comparison
			// against real hardware read as "we never call that"
			// when the truth was "this never printed it".
			if m.Frames() < *guardFrom {
				return
			}
			nCall++
			if *probeN > 0 && nCall > *probeN {
				return
			}
			fmt.Fprintf(os.Stderr,
				"f%d bios %04X hl=%04X de=%04X bc=%04X a=%02X ret=%04X banks %v\n",
				m.Frames(), addr, m.HL(), m.DE(), m.BC(), m.A,
				uint16(m.Mem[m.SP])|uint16(m.Mem[m.SP+1])<<8, m.Banks())
		}
	}

	if *bankwatch >= 0 {
		pc := uint16(0)
		prev := m.Observe
		m.Observe = func(a uint16, banks []int) {
			pc = a
			if prev != nil {
				prev(a, banks)
			}
		}
		m.OnBank = func(page, bank int) {
			if page != *bankwatch || m.Frames() < *bankFrom {
				return
			}
			fmt.Fprintf(os.Stderr, "f%d page %d <- bank %d  by %s\n",
				m.Frames(), page, bank, siteOf(pc, m.Banks()))
		}
	}

	if *guard >= 0 {
		var atFrame uint64
		prevF := m.WatchWrites
		m.WatchWrites = func(a uint16, v byte) {
			if prevF != nil {
				prevF(a, v)
			}
			if int(a) == *guard && m.Frames() >= *guardFrom &&
				m.Frames() < *guardFrom+40 {
				fmt.Fprintf(os.Stderr,
					"f%d guard <- %d at %d cycles into the frame\n",
					m.Frames(), v, m.Cyc-atFrame)
			}
		}
		mark := func(f int) { atFrame = m.Cyc }
		_ = mark
		frameMark = func() { atFrame = m.Cyc }
	}

	if *regs {
		seen := map[byte]int{}
		vals := map[byte]byte{}
		m.VDP.OnReg = func(r, v byte) { seen[r]++; vals[r] = v }
		defer func() {
			ks := make([]int, 0, len(seen))
			for k := range seen {
				ks = append(ks, int(k))
			}
			sort.Ints(ks)
			for _, k := range ks {
				fmt.Fprintf(os.Stderr, "  R%-2d written %6d times, last %02X\n",
					k, seen[byte(k)], vals[byte(k)])
			}
		}()
	}

	if *pchist > 0 {
		counts := map[uint16]int{}
		prev := m.Observe
		m.Observe = func(pc uint16, banks []int) {
			if prev != nil {
				prev(pc, banks)
			}
			if m.Frames() >= *pchist &&
				(*pcuntil == 0 || m.Frames() < *pcuntil) {
				counts[pc]++
			}
		}
		defer func() {
			type kv struct {
				pc uint16
				n  int
			}
			var all []kv
			for k, v := range counts {
				all = append(all, kv{k, v})
			}
			if *pcof != "" {
				for _, name := range strings.Split(*pcof, ",") {
					var a uint16
					if _, err := fmt.Sscanf(strings.TrimSpace(name), "%x", &a); err != nil {
						continue
					}
					fmt.Fprintf(os.Stderr, "  pc %04X x%d\n", a, counts[a])
				}
				return
			}
			sort.Slice(all, func(i, j int) bool { return all[i].n > all[j].n })
			for i := 0; i < *pctop && i < len(all); i++ {
				fmt.Fprintf(os.Stderr, "  pc %04X x%d\n", all[i].pc, all[i].n)
			}
		}()
	}
	if *stat {
		counts := map[byte]int{}
		m.VDP.OnStatus = func(n byte) { counts[n]++ }
		defer func() {
			ks := make([]int, 0, len(counts))
			for k := range counts {
				ks = append(ks, int(k))
			}
			sort.Ints(ks)
			for _, k := range ks {
				fmt.Fprintf(os.Stderr, "  S#%d read %d times\n", k, counts[byte(k)])
			}
		}()
	}
	if *vwrites > 0 {
		byPage := map[int]int{}
		n := 0
		m.VDP.OnWrite = func(addr uint16, b byte) {
			if m.Frames() < *vwrites || m.Frames() > *vwrites+50 {
				return
			}
			n++
			byPage[m.VDP.At()>>15]++
			if n <= 24 {
				fmt.Fprintf(os.Stderr, "    write %05X = %02X\n",
					m.VDP.At(), b)
			}
		}
		defer func() {
			fmt.Fprintf(os.Stderr, "  %d writes through the data port in "+
				"frames %d-%d\n", n, *vwrites, *vwrites+50)
			for p := 0; p < 4; p++ {
				if byPage[p] > 0 {
					fmt.Fprintf(os.Stderr, "    page %d: %d\n", p, byPage[p])
				}
			}
		}()
	}
	if *cmds {
		n := map[byte]int{}
		big := 0
		m.VDP.OnCmd = func(c byte, sx, sy, dx, dy, nx, ny int, arg byte) {
			n[c>>4]++
			if m.Frames() >= *cmdFrom && m.Frames() <= *cmdFrom+*cmdWin && big < 100000 {
				big++
				fmt.Fprintf(os.Stderr,
					"  f%d cmd %02X: src %d,%d dst %d,%d size %dx%d arg %02X\n",
					m.Frames(), c, sx, sy, dx, dy, nx, ny, arg)
			}
		}
		defer func() {
			names := map[byte]string{0: "STOP", 4: "POINT", 5: "PSET",
				6: "SRCH", 7: "LINE", 8: "LMMV", 9: "LMMM", 0xA: "LMCM",
				0xB: "LMMC", 0xC: "HMMV", 0xD: "HMMM", 0xE: "YMMM",
				0xF: "HMMC"}
			ks := make([]int, 0, len(n))
			for k := range n {
				ks = append(ks, int(k))
			}
			sort.Ints(ks)
			for _, k := range ks {
				fmt.Fprintf(os.Stderr, "  command %X %-5s x%d\n",
					k, names[byte(k)], n[byte(k)])
			}
		}()
	}
	if *cycles {
		defer func() { fmt.Printf("cycles %d\n", m.Cyc) }()
	}
	// Which instructions the two cycle models disagree about. They have to
	// agree instruction by instruction or the two machines keep different
	// clocks, so this checks every instruction that actually runs.
	if *cyccmp {
		type mism struct {
			pc        uint16
			got, want uint32
			n         int
		}
		bad := map[string]*mism{}
		var prevPC uint16
		var prevCyc uint64
		first := true
		prev := m.Observe
		m.Observe = func(pc uint16, banks []int) {
			if prev != nil {
				prev(pc, banks)
			}
			if !first {
				got := uint32(m.Cyc - prevCyc)
				if ins, ok := dis.Decode(romView{m}, prevPC); ok {
					want := z80.CycleCost(ins.Op, ins.Sub)
					if got != want {
						k := fmt.Sprintf("%02X %02X", ins.Op, ins.Sub)
						if bad[k] == nil {
							bad[k] = &mism{prevPC, got, want, 0}
						}
						bad[k].n++
					}
				}
			}
			first = false
			prevPC, prevCyc = pc, m.Cyc
		}
		defer func() {
			ks := make([]string, 0, len(bad))
			for k := range bad {
				ks = append(ks, k)
			}
			sort.Strings(ks)
			for _, k := range ks {
				b := bad[k]
				fmt.Fprintf(os.Stderr,
					"  opcode %s at %04Xh x%d: interpreter %d, emitter %d\n",
					k, b.pc, b.n, b.got, b.want)
			}
		}()
	}
	if *modeReport {
		n := 0
		m.VDP.OnPal = func(b byte) {
			if n < 40 {
				fmt.Fprintf(os.Stderr, "9A <- %02X\n", b)
			}
			n++
		}
		defer func() {
			fmt.Fprintf(os.Stderr, "mode %v, %d lines, page base %05Xh, "+
				"V9938=%v, VRAM %dK\n", m.VDP.Mode(), m.VDP.Lines(),
				m.VDP.PageBase(), m.VDP.V9938, len(m.VDP.VRAM)/1024)
			fmt.Fprintf(os.Stderr, "R19=%02X (line interrupt at line %d), "+
				"IE1=%v IE0=%v\n", m.VDP.Reg[19], m.VDP.Reg[19],
				m.VDP.Reg[0]&0x10 != 0, m.VDP.Reg[1]&0x20 != 0)
			fmt.Fprintf(os.Stderr, "R0=%02X R1=%02X R2=%02X R3=%02X R4=%02X R9=%02X R10=%02X "+
				"R5=%02X R6=%02X R11=%02X R23=%02X\n",
				m.VDP.Reg[0], m.VDP.Reg[1], m.VDP.Reg[2], m.VDP.Reg[3],
				m.VDP.Reg[4], m.VDP.Reg[9], m.VDP.Reg[10],
				m.VDP.Reg[5], m.VDP.Reg[6], m.VDP.Reg[11], m.VDP.Reg[23])
			pal := m.VDP.Palette16()
			fmt.Fprintf(os.Stderr, "palette:")
			for i, c := range pal {
				fmt.Fprintf(os.Stderr, " %d:%02X%02X%02X", i, c.R, c.G, c.B)
			}
			fmt.Fprintln(os.Stderr)
		}()
	}

	if *reglog > 0 {
		m.VDP.OnReg = func(r, v byte) {
			if m.Frames() >= *reglog && m.Frames() < *reglog+*reglogN {
				perLine := m.FrameCycles() / 262
				line := (m.Cyc - m.FrameOrigin()) / perLine
				stack := ""
				for i := 0; i < 8; i++ {
					a := m.SP + uint16(i*2)
					stack += fmt.Sprintf(" %04X",
						uint16(m.Mem[a])|uint16(m.Mem[a+1])<<8)
				}
				fmt.Fprintf(os.Stderr, "f%d line %3d: R%d <- %02X "+
					"(pc=%04X sp=%04X stack:%s banks %v)\n",
					m.Frames(), line, r, v, m.PC, m.SP, stack,
					m.Banks())
			}
		}
	}
	if *mwatch != "" {
		var lo, hi int
		if _, err := fmt.Sscanf(*mwatch, "%x:%x", &lo, &hi); err != nil {
			check(fmt.Errorf("-mwatch wants lo:hi in hex"))
		}
		m.MemTrace = func(a uint16, v byte) {
			if int(a) < lo || int(a) > hi ||
				m.Frames() < *mwatchFrom {
				return
			}
			fmt.Fprintf(os.Stderr, "mem f%d %04X <- %02X (pc=%04X banks %v)\n",
				m.Frames(), a, v, m.PC, m.Banks())
		}
	}
	if *enterRAM {
		var prev [32]uint16
		var n int
		done := false
		p0 := m.Observe
		m.Observe = func(pc uint16, banks []int) {
			if p0 != nil {
				p0(pc, banks)
			}
			if !done && pc >= 0xBC00 && pc < 0xFD00 {
				done = true
				out := ""
				for i := 1; i <= 32; i++ {
					out += fmt.Sprintf(" %04X", prev[(n+i)&31])
				}
				fmt.Fprintf(os.Stderr, "first stray instruction %04X, preceded by%s\n", pc, out)
			}
			n = (n + 1) & 31
			prev[n] = pc
		}
	}
	if *ixcensus {
		counts := map[uint16]int{}
		pb := m.BiosTrace
		m.BiosTrace = func(addr uint16) {
			if pb != nil {
				pb(addr)
			}
			if addr == 0x015C || addr == 0x015F {
				counts[m.IX]++
			}
		}
		defer func() {
			type kv struct {
				ix uint16
				n  int
			}
			var all []kv
			for k, v := range counts {
				all = append(all, kv{k, v})
			}
			sort.Slice(all, func(i, j int) bool { return all[i].n > all[j].n })
			for _, e := range all {
				fmt.Fprintf(os.Stderr, "  ix=%04X x%d\n", e.ix, e.n)
			}
		}()
	}
	if *lastpcs {
		var ring [1024]uint16
		var rn int
		prev := m.Observe
		m.Observe = func(pc uint16, banks []int) {
			if prev != nil {
				prev(pc, banks)
			}
			ring[rn&1023] = pc
			rn++
		}
		defer func() {
			fmt.Fprintf(os.Stderr, "last instructions (oldest first):\n")
			start := rn - 1024
			if start < 0 {
				start = 0
			}
			for i := start; i < rn; i++ {
				fmt.Fprintf(os.Stderr, " %04X", ring[i&1023])
				if (i-start)%16 == 15 {
					fmt.Fprintln(os.Stderr)
				}
			}
			fmt.Fprintln(os.Stderr)
		}()
	}
	if *irqLog > 0 {
		tally := map[string]int{}
		m.IRQTrace = func(what string, line int) {
			if m.Frames() < *irqLog || m.Frames() >= *irqLog+*irqLogN {
				return
			}
			if *irqCount {
				if i := strings.IndexByte(what, ' '); i > 0 &&
					strings.HasPrefix(what, "arm for") {
					what = "arm"
				}
				tally[what]++
				return
			}
			fmt.Fprintf(os.Stderr, "irq f%d %-10s line %d\n",
				m.Frames(), what, line)
		}
		if *irqCount {
			defer func() {
				ks := make([]string, 0, len(tally))
				for k := range tally {
					ks = append(ks, k)
				}
				sort.Strings(ks)
				for _, k := range ks {
					fmt.Fprintf(os.Stderr, "  %-32s %d\n", k, tally[k])
				}
			}()
		}
	}
	if *stuck {
		var cnt uint64
		printed := false
		prev2 := m.Observe
		m.Observe = func(pc uint16, banks []int) {
			if prev2 != nil {
				prev2(pc, banks)
			}
			cnt++
			if cnt == 3_000_000 && !printed {
				printed = true
				fmt.Fprintf(os.Stderr, "STUCK f%d pc=%04X sp=%04X banks %v "+
					"IFF=%v FH=%v F=%v\n", m.Frames(), pc, m.SP, m.Banks(),
					m.IFF, m.VDP.FHPending(), m.VDP.FPending())
			}
		}
		resetCnt = func() { cnt = 0; printed = false }
	}
	if *isrtrace > 0 {
		on := false
		n := 0
		prev := m.Observe
		m.Observe = func(pc uint16, banks []int) {
			if prev != nil {
				prev(pc, banks)
			}
			if m.Frames() == *isrtrace && m.SP < 0xF0F0 {
				on = true
			}
			if on && n < 260 {
				n++
				fmt.Fprintf(os.Stderr, " %04X", pc)
				if n%16 == 0 {
					fmt.Fprintln(os.Stderr)
				}
			}
		}
	}
	if *rst38 {
		type ev struct{ pc, sp uint16 }
		var ring [4096]ev
		ri := 0
		done := false
		prev := m.Observe
		m.Observe = func(pc uint16, banks []int) {
			if prev != nil {
				prev(pc, banks)
			}
			ring[ri] = ev{pc, m.SP}
			ri = (ri + 1) % len(ring)
			if pc < 0x4158 && pc >= 0x4000 && m.Frames() > 700 && !done {
				done = true
				fmt.Fprintf(os.Stderr, "  main thread at %04X, f%d; last 160:\n   ", pc, m.Frames())
				for k := len(ring) - 160; k < len(ring); k++ {
					e := ring[(ri+k)%len(ring)]
					fmt.Fprintf(os.Stderr, " %04X/%04X", e.pc, e.sp)
				}
				fmt.Fprintln(os.Stderr)
			}
		}
		prevBios1 := m.BiosTrace
		m.BiosTrace = func(addr uint16) {
			if prevBios1 != nil {
				prevBios1(addr)
			}
			if addr == 0x38 {
				fmt.Fprintf(os.Stderr, "rst38 f%d pc=%04X sp=%04X banks %v "+
					"ret=%04X\n", m.Frames(), m.PC, m.SP, m.Banks(),
					uint16(m.Mem[m.SP])|uint16(m.Mem[m.SP+1])<<8)
				fmt.Fprintf(os.Stderr, "  trail (pc/sp):")
				for k := 0; k < len(ring); k++ {
					e := ring[(ri+k)%len(ring)]
					fmt.Fprintf(os.Stderr, " %04X/%04X", e.pc, e.sp)
				}
				fmt.Fprintln(os.Stderr)
			}
		}
	}

	lastExec, lastCyc := uint64(0), uint64(0)
	derailed := false
	lastVByte := byte(0)
	// The cartridge's identity, so a snapshot cannot be restored into a
	// different game.
	sum := sha1.Sum(data)
	id := fmt.Sprintf("%x", sum)

	type poke struct {
		frame int
		addr  uint16
		val   byte
	}
	var pokeList []poke
	if *pokes != "" {
		for _, p := range strings.Split(*pokes, ",") {
			var f2, a, v int
			if _, err := fmt.Sscanf(p, "%d:%x:%x", &f2, &a, &v); err != nil {
				check(fmt.Errorf("bad -poke %q: %v", p, err))
			}
			pokeList = append(pokeList, poke{f2, uint16(a), byte(v)})
		}
	}
	keys := loadTape(*tape)
	var buttons []byte
	if *btape != "" {
		var err error
		buttons, err = os.ReadFile(*btape)
		check(err)
	}
	mk := newMonkey(*monkey)
	defer func() {
		e, _ := m.InterruptEntry()
		fmt.Fprintf(os.Stderr, "msxrun: %d mid-init interrupt(s), hook at %04Xh\n",
			m.BootIRQs(), e)
	}()
	if *resume != "" {
		rf, err := os.Open(*resume)
		check(err)
		check(m.LoadState(rf, id))
		rf.Close()
		fmt.Fprintf(os.Stderr, "msxrun: resumed from %s at frame %d\n",
			*resume, m.Frames())
	}
	// The sound the harness would have played, written to a file so it can
	// be compared against a recording of the reference machine.
	const wavRate = 44100
	var synth *z80.Synth
	var pcm []int16
	if *wav != "" {
		synth = z80.NewSynth(wavRate)
		m.PSG.Log = true
		m.SCC.SetSampleRate(wavRate)
	}
	err = m.InterpretRun(uint16(*base), *frames, *quota, func(f int) {
		if synth != nil {
			for _, x := range m.PSG.TakeWrites() {
				synth.Write(x.Reg, x.Val)
			}
			if f >= *wavFrom {
				buf := make([]int16, wavRate/60*2)
				synth.Synthesize(buf)
				m.SCC.Synthesize(buf)
				pcm = append(pcm, buf...)
			}
		}
		if resetCnt != nil {
			resetCnt()
		}
		if m.PC < 0x4000 && !derailed {
			derailed = true
			fmt.Fprintf(os.Stderr, "DERAIL f%d pc=%04X sp=%04X banks %v\n",
				f, m.PC, m.SP, m.Banks())
		}
		if *finfo > 0 && f >= *finfo && f < *finfo+8 {
			fmt.Fprintf(os.Stderr,
				"f%d IFF=%v FH=%v F=%v C914=%02X pc=%04X sp=%04X\n",
				f, m.IFF, m.VDP.FHPending(), m.VDP.FPending(),
				m.Mem[0xC914], m.PC, m.SP)
		}
		if frameMark != nil {
			frameMark()
		}
		for _, pk := range pokeList {
			if pk.frame == f {
				m.Mem[pk.addr] = pk.val
				fmt.Fprintf(os.Stderr, "poke f%d %04X <- %02X\n",
					f, pk.addr, pk.val)
			}
		}
		if f < len(buttons) {
			// What the window does: the same press reaches a game
			// through the joystick port and through the keyboard
			// matrix, and a game is entitled to read either.
			b := z80.Buttons(buttons[f])
			m.SetJoystick(b)
			m.SetInput(b)
		} else if f < len(keys) {
			for r := range keys[f] {
				m.SetKeyRow(r, keys[f][r])
			}
		} else if mk != nil {
			m.SetInput(mk.next(f))
		}
		if *snapshot != "" && f+1 == *snapAt {
			f2, err := os.Create(*snapshot)
			check(err)
			check(m.SaveState(f2, id))
			check(f2.Close())
			fmt.Fprintf(os.Stderr, "msxrun: snapshot at frame %d -> %s\n",
				f+1, *snapshot)
		}
		if *hold != "" && f >= *holdFrom {
			if k, ok := namedKey(*hold); ok {
				m.PressKey(k)
			}
		}
		if *border7 && (f+1)%600 == 0 {
			fmt.Fprintf(os.Stderr, "frame %d: R7=%02X border colour %d\n",
				f+1, m.VDP.Reg[7], m.VDP.Reg[7]&0x0F)
		}
		if *vbyte >= 0 {
			if v := m.VDP.VRAM[*vbyte&0x3FFF]; v != lastVByte {
				fmt.Fprintf(os.Stderr, "vram %04X: %02X -> %02X at frame %d\n",
					*vbyte, lastVByte, v, f+1)
				lastVByte = v
			}
		}
		if *icount > 0 && (f+1)%*icount == 0 {
			di := m.Executed - lastExec
			dc := m.Cyc - lastCyc
			cpi := 0.0
			if di > 0 {
				cpi = float64(dc) / float64(di)
			}
			fmt.Fprintf(os.Stderr,
				"frame %5d: %6d instr, %8d cycles (%.1f/instr) = %.2f frames of budget\n",
				f+1, di, dc, cpi, float64(dc)/float64(m.FrameCycles()))
		}
		lastExec, lastCyc = m.Executed, m.Cyc
		if vcrcOut != nil && (f+1)%*vevery == 0 {
			// The name table and the registers: what is on screen.
			// See ref/vframes.tcl for why not all of video memory.
			base := int(m.VDP.Reg[2]&0x0F) << 10
			c := crc32.NewIEEE()
			c.Write(m.VDP.VRAM[base : base+768])
			// The first eight only: that is what a TMS9918 has and
			// what ref/vframes.tcl writes on the other side.
			c.Write(m.VDP.Reg[:8])
			fmt.Fprintf(vcrcOut, "%d %08x\n", f+1, c.Sum32())
		}
		if crcOut != nil {
			c := crc32.NewIEEE()
			c.Write(m.Mem[0xC000:0xE23A])
			c.Write(m.Mem[0xE23B:0xF000])
			c.Write(m.Mem[0xF0F1:0xF0F7])
			fmt.Fprintf(crcOut, "%d %08x\n", f+1, c.Sum32())
		}
		if *digest > 0 && (f+1)%*digest == 0 {
			fmt.Printf("%d vram %s ram %s psg %s\n", f+1,
				h(m.VDP.VRAM[:]), h(m.Mem[0xC000:]), h(m.PSG.Reg[:]))
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "msxrun:", err)
	}

	if *sites != "" {
		out := make([]string, 0, len(seen))
		for s := range seen {
			out = append(out, s)
		}
		sort.Strings(out)
		f, err := os.Create(*sites)
		check(err)
		w := bufio.NewWriter(f)
		fmt.Fprintln(w, "# Every address the cartridge executed, with the "+
			"banks in force. Written by msxrun.")
		fmt.Fprintln(w, "# Observation, not inference: whatever ran is code, "+
			"including what a static trace cannot follow.")
		for _, s := range out {
			fmt.Fprintln(w, s)
		}
		check(w.Flush())
		check(f.Close())
		fmt.Fprintf(os.Stderr, "msxrun: %d frames, %d distinct addresses executed -> %s\n",
			m.Frames(), len(out), *sites)
	}
	if *vramdump != "" {
		// The same shape ref/ dumps: memory, 32 registers, then the
		// palette as the chip stores it, so the two can be rendered
		// through the same path and compared.
		out := append([]byte(nil), m.VDP.VRAM...)
		out = append(out, m.VDP.Reg[:32]...)
		for _, c := range m.VDP.Palette16() {
			out = append(out, c.R/36<<4|c.B/36, c.G/36)
		}
		check(os.WriteFile(*vramdump, out, 0o644))
	}
	if m.Disk != nil && *dskSave != "" {
		check(m.DOSFlushAll())
		if m.Disk.Dirty() {
			check(os.WriteFile(*dskSave, m.Disk.Image(), 0o644))
			fmt.Fprintf(os.Stderr, "msxrun: the disk changed -> %s\n", *dskSave)
		} else {
			fmt.Fprintln(os.Stderr, "msxrun: the disk was not written to")
		}
	}
	if *wav != "" {
		writeWAV(*wav, pcm, wavRate)
		fmt.Fprintf(os.Stderr, "msxrun: %d samples of sound -> %s\n",
			len(pcm)/2, *wav)
	}
	if *ramdump != "" {
		check(os.WriteFile(*ramdump, m.Mem[0xC000:], 0o644))
	}
	if *memdump != "" {
		check(os.WriteFile(*memdump, m.Mem[:], 0o644))
	}
	if *splitdbg && *shot != "" {
		// The frame's two register states, each rendered over the whole
		// screen: which one is garbled says whether the split placement
		// or the content is wrong.
		for _, e := range m.VDP.SplitLog {
			fmt.Fprintf(os.Stderr, "  line %3d: R%d %02X -> %02X\n",
				e.Line, e.Reg, e.Old, e.New)
		}
		base := m.VDP.RegsAt(0)
		end := m.VDP.Reg
		m.VDP.SplitLog = m.VDP.SplitLog[:0]
		m.VDP.Reg = base
		fa, _ := os.Create(*shot + ".A.png")
		png.Encode(fa, z80.NewRenderer().RenderVDP(&m.VDP))
		fa.Close()
		m.VDP.Reg = end
		fb, _ := os.Create(*shot + ".B.png")
		png.Encode(fb, z80.NewRenderer().RenderVDP(&m.VDP))
		fb.Close()
	}
	if *shot != "" {
		fmt.Fprintf(os.Stderr, "msxrun: %d split event(s) in the last frame\n",
			len(m.VDP.SplitLog))
		img := z80.NewRenderer().RenderVDP(&m.VDP)
		os.MkdirAll(filepath.Dir(*shot), 0o755)
		f, err := os.Create(*shot)
		check(err)
		check(png.Encode(f, img))
		check(f.Close())
	}
}

// h hashes one region, in the same three parts kvdigest prints, so that the
// interpreter's answer can be diffed against the translated build's directly.
// That diff is the proof the interpreter is worth trusting.
func h(b []byte) string { s := sha1.Sum(b); return fmt.Sprintf("%x", s)[:12] }

// A monkey at the controls.
//
// Attract mode is only the code a cartridge runs when nobody is playing, and
// it saturates: on Salamander it stops finding anything new after a few
// thousand frames. Everything past the title screen has to be reached by
// pressing buttons, so this presses them -- start often enough to get through
// menus and continues, a direction held for a while at a time, and fire
// always, which is what a shooter wants.
//
// It is deterministic. A run that finds a crash has to be repeatable, and a
// seed is cheaper to write down than a tape.
type monkey struct {
	st  uint64
	dir z80.Buttons
}

func newMonkey(seed int64) *monkey {
	if seed == 0 {
		return nil
	}
	return &monkey{st: uint64(seed)*6364136223846793005 + 1442695040888963407}
}

func (k *monkey) rand() uint64 {
	k.st = k.st*6364136223846793005 + 1442695040888963407
	return k.st >> 33
}

func (k *monkey) next(f int) z80.Buttons {
	var b z80.Buttons
	// A direction, held for about a third of a second so that it moves
	// somewhere rather than jittering in place.
	if f%20 == 0 {
		k.dir = z80.Buttons(1 << (k.rand() % 4))
	}
	b |= k.dir
	// Fire, always -- and start, often, since the same button does both.
	b |= z80.TriggerA
	if f%180 < 4 {
		b |= z80.TriggerB
	}
	return b
}

// siteOf is an address and its paging, the pair that names cartridge code.
func siteOf(pc uint16, banks []int) string {
	b := make([]string, 0, len(banks))
	for _, n := range banks {
		b = append(b, fmt.Sprintf("%d", n))
	}
	return fmt.Sprintf("%04X [%s]", pc, strings.Join(b, ","))
}

// namedKey turns a key's name into its place in the MSX matrix.
func namedKey(s string) (z80.MSXKey, bool) {
	switch strings.ToLower(s) {
	case "f1":
		return z80.KeyF1, true
	case "f2":
		return z80.KeyF2, true
	case "f3":
		return z80.KeyF3, true
	case "f4":
		return z80.KeyF4, true
	case "f5":
		return z80.KeyF5, true
	case "ret", "return":
		return z80.KeyReturn, true
	case "esc":
		return z80.KeyEsc, true
	case "space":
		return z80.KeySpace, true
	case "stop":
		return z80.KeyStop, true
	case "select":
		return z80.KeySelect, true
	case "shift":
		return z80.KeyShift, true
	}
	if len(s) == 1 {
		if k, ok := z80.LetterKey(s[0]); ok {
			return k, true
		}
		if k, ok := z80.DigitKey(s[0]); ok {
			return k, true
		}
	}
	return z80.MSXKey{}, false
}

func loadTape(path string) [][12]byte {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	check(err)
	var out [][12]byte
	for i := 0; i+12 <= len(b); i += 12 {
		var row [12]byte
		copy(row[:], b[i:i+12])
		out = append(out, row)
	}
	return out
}

// romView lets the decoder read the machine's address space.
type romView struct{ m *z80.M }

func (romView) Readable(uint16, int) bool { return true }

func (r romView) Byte(a uint16) byte { return r.m.Mem[a] }
func (r romView) Word(a uint16) uint16 {
	return uint16(r.m.Mem[a]) | uint16(r.m.Mem[a+1])<<8
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "msxrun:", err)
		os.Exit(1)
	}
}

// writeWAV writes stereo signed 16-bit samples as a RIFF file.
func writeWAV(path string, pcm []int16, rate int) {
	var b bytes.Buffer
	data := len(pcm) * 2
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+data))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(2)) // stereo
	binary.Write(&b, binary.LittleEndian, uint32(rate))
	binary.Write(&b, binary.LittleEndian, uint32(rate*4))
	binary.Write(&b, binary.LittleEndian, uint16(4))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(data))
	binary.Write(&b, binary.LittleEndian, pcm)
	check(os.WriteFile(path, b.Bytes(), 0o644))
}
