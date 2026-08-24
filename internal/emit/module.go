package emit

import (
	"bytes"
	"crypto/sha1"
	"embed"
	"encoding/hex"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brunoga/msx2go/internal/dis"
	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
)

// The runtime is not a template. It is ordinary Go, compiled and tested as
// part of msx2go, and copied out verbatim -- one source of truth, and it is
// one the compiler and the test suite already check.
//
//go:embed runtime/*.go
var runtimeFS embed.FS

// runtimeSkip are the files that stay behind: the two stand-ins that let the
// runtime compile inside msx2go where there is no cartridge, and the tests.
var runtimeSkip = map[string]bool{
	"run_stub.go":  true, // the generated rom_gen.go supplies Run
	"data_stub.go": true, // the generated data_gen.go supplies dataBlocks
	"data_test.go": true,
}

// Module is everything needed to write a generated module out.
type Module struct {
	// Dir is where it goes, Info what the cartridge is.
	Dir  string
	Info z80.Info
	// ModPath is the go.mod module path.
	ModPath string
	// ROM is the image, Starts the tracer's instruction boundaries, and
	// Insns those instructions by image offset -- which is what says
	// which bytes are code and so which are not data.
	//
	// Logical is the address each offset was decoded at and SiteBanks the
	// paging it was decoded under, both of which matter only for a banked
	// cartridge: there the label is the offset, and an instruction at the
	// end of a bank reads its operands out of the next page.
	ROM    []byte
	Starts []uint16
	// Executed are image offsets the cartridge was seen to run, from a
	// sweep. When it is non-empty, pruning believes it rather than the
	// tracer: see blocks.
	Executed  map[int]bool
	Insns     map[int]dis.Insn
	Logical   map[int]uint16
	SiteBanks map[int][]int
	// Base is where the cartridge is mapped.
	Base uint16
	// Names, where given, name the data blocks: a region's name is used
	// for any block that starts inside it.
	Names []NamedRange
	// Whole keeps the entire image rather than pruning to the data.
	Whole bool
	// TransROM and TransBase, when set, are the translation's view of
	// the code: a snapshot of the RAM a disk's loader filled, taken the
	// moment the loader finished. The data blocks still come from ROM --
	// the floppy -- and the runtime checks the loaded RAM against
	// TransSHA1 before trusting the translation. Cartridges leave these
	// empty and translate the image itself.
	TransROM  []byte
	TransBase uint16
	// MinPruneRun is the shortest run of translated bytes worth pruning,
	// and it is off by default.
	//
	// It exists for the case where code and data genuinely overlap: a lone
	// C9h inside a table that something jumps to as a `ret` is a byte of
	// the table *and* an instruction, and pruning it leaves FFh where the
	// table expected C9h. Keeping short runs takes that whole class out of
	// play for a few dozen bytes.
	//
	// It is off because on the one cartridge that can be checked it is not
	// needed -- every apparent overlap in King's Valley turned out to be
	// the tracer walking into data, and fixing that removed them all. A
	// rule that guards against nothing observable is a fudge, so it waits
	// for a cartridge where the msxcheck build says otherwise. Zero means
	// the default.
	MinPruneRun int
}

// DefaultMinPruneRun is one: prune every run. See Module.MinPruneRun.
const DefaultMinPruneRun = 1

// NamedRange is a span of the image somebody has a name for.
type NamedRange struct {
	Start, End int
	Name       string
}

// Write generates the module.
func (m Module) Write() (Report, error) {
	var rep Report
	if err := os.MkdirAll(filepath.Join(m.Dir, "z80"), 0o755); err != nil {
		return rep, err
	}

	trom, tbase := m.ROM, m.Base
	if m.TransROM != nil {
		trom, tbase = m.TransROM, m.TransBase
	}
	run := Run{
		Package: "z80",
		Source:  m.Info.Name + ".rom",
		View:    dis.Rom{Data: trom, Base: tbase},
		Starts:  m.Starts,
		Mapper:  m.Info.Mapper,
		Base:    tbase,
		ROM:     trom,
		// A generated cartridge is booted by running its own INIT, so
		// the idle loop it ends in has to hand control back.
		IdleOnSelfJump: true,
		// A generated cartridge is what discovery runs against, so it
		// carries the hook that records where it goes.
		TraceTransfers: true,
		CountCycles:    true,
		// And an untranslated address gives up on the frame rather
		// than falling into the next label, which is dead code unless
		// the discovery build is what is being made.
		RecoverOnNoLabel: true,
		// And a dynamic jump into the BIOS is a tail call into it,
		// which is what the static forms already do.
		BIOSTailCall: true,
	}
	if m.Info.Mapper.Name != "none" && m.TransROM == nil {
		run.Sites = m.sites()
		run.Starts = nil
	}
	gen, bad, err := run.Generate()
	if err != nil {
		return rep, err
	}
	rep.Unsupported = bad
	rep.Instructions = len(m.Insns)
	if err := m.put("z80/rom_gen.go", gen); err != nil {
		return rep, err
	}

	blocks := m.blocks()
	sortBlocks(blocks)
	rep.PointerLoads = m.PointerLoads()
	rep.Blocks = len(blocks)
	for _, b := range blocks {
		rep.DataBytes += len(b.Data)
	}
	packed := z80.Pack(blocks)
	sum := sha1.Sum(packed)
	m.Info.SHA1 = hex.EncodeToString(sum[:])
	m.Info.Fill = 0xFF

	if err := m.put("NOTICE", []byte(moduleNotice)); err != nil {
		return rep, err
	}
	meta, err := m.meta()
	if err != nil {
		return rep, err
	}
	if err := m.put("z80/rom_meta.go", meta); err != nil {
		return rep, err
	}
	data, err := m.data(blocks)
	if err != nil {
		return rep, err
	}
	if err := m.put("z80/data_gen.go", data); err != nil {
		return rep, err
	}

	names, err := runtimeFS.ReadDir("runtime")
	if err != nil {
		return rep, err
	}
	for _, f := range names {
		if runtimeSkip[f.Name()] || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		b, err := runtimeFS.ReadFile("runtime/" + f.Name())
		if err != nil {
			return rep, err
		}
		if err := m.put("z80/"+f.Name(), b); err != nil {
			return rep, err
		}
		rep.RuntimeFiles++
	}

	// The windowed harness wants Ebiten; the headless one wants nothing.
	// Declaring it here rather than leaving `go mod tidy` to discover it
	// means the version is the one this was written against.
	if err := m.put("go.mod", []byte(fmt.Sprintf(
		"module %s\n\ngo 1.26.6\n\n"+
			"require github.com/hajimehoshi/ebiten/v2 v2.9.10\n",
		m.ModPath))); err != nil {
		return rep, err
	}
	return rep, m.mainProgram()
}

// Report is what generating produced, for the person who asked for it.
type Report struct {
	Instructions int
	Blocks       int
	DataBytes    int
	RuntimeFiles int
	Unsupported  []error
	// PointerLoads are addresses of translated code that something loads
	// a pointer to. See Module.PointerLoads.
	PointerLoads []uint16
}

func (m Module) put(rel string, b []byte) error {
	p := filepath.Join(m.Dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// sites pairs every translated instruction with the address it was decoded
// at, which is what a banked cartridge's labels are made of.
func (m Module) sites() []Site {
	out := make([]Site, 0, len(m.Insns))
	for off, ins := range m.Insns {
		addr, ok := m.Logical[off]
		if !ok {
			addr = ins.Addr
		}
		banks := m.SiteBanks[off]
		if banks == nil {
			banks = m.Info.Mapper.Initial
		}
		out = append(out, Site{Off: off, Addr: addr, Ins: ins,
			Banks: banks})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Off < out[j].Off })
	return out
}

// blocks prunes the image to the bytes the cartridge does not need as data.
//
// The rule is that a byte the cartridge *executes* cannot also be needed as
// data, because the translation carries its meaning. Which bytes those are is
// the whole question, and there are two answers of very different strength.
//
// The tracer's answer is every byte it decoded an instruction over. That is
// inference, and on a megaROM it over-reaches: a walk that ran off into a
// table decodes the table as instructions, pruning throws it away, and the
// game reads FF FF where a pointer should be. Salamander does exactly this
// with the parameter table at BB6F in bank 15, and the symptom is a frozen
// screen two thousand frames later, nowhere near the cause.
//
// A sweep's answer is every byte an instruction that actually ran covered.
// That is observation, and it cannot over-reach -- what executed, executed. So
// when a sweep has been done, its answer is the one used, and an instruction
// the tracer translated but never saw run is kept as data as well as being
// translated. Paying for those bytes twice is the cheap side of this trade.
func (m Module) blocks() []z80.Block {
	if m.Whole || m.Info.MainThread {
		// A main-thread game keeps everything whether or not -whole was
		// asked for: its main thread runs interpreted, an interpreter
		// executes from memory, and pruning removes exactly the bytes
		// the translation covers -- which for the main thread is the
		// code it is about to execute.
		return []z80.Block{{Name: "image", Off: 0, Data: m.ROM}}
	}
	covered := make([]bool, len(m.ROM))
	for off, ins := range m.Insns {
		if len(m.Executed) > 0 && !m.Executed[off] {
			continue
		}
		for i := off; i < off+ins.Len && i < len(covered); i++ {
			covered[i] = true
		}
	}
	// Runs of translated bytes too short to be a routine are kept: see
	// Module.MinPruneRun.
	min := m.MinPruneRun
	if min == 0 {
		min = DefaultMinPruneRun
	}
	for i := 0; i < len(covered); {
		if !covered[i] {
			i++
			continue
		}
		j := i
		for j < len(covered) && covered[j] {
			j++
		}
		if j-i < min {
			for k := i; k < j; k++ {
				covered[k] = false
			}
		}
		i = j
	}
	// And then un-cover the code bytes something reads as data.
	//
	// King's Valley builds a two-byte routine in RAM -- `pop hl / ret` --
	// by copying the opcode out of its own code at 404Ch. That is exactly
	// the case pruning gets wrong, and it is not guesswork to fix: an
	// absolute load names its address and the opcode says how wide it is,
	// so those bytes can be kept exactly. What cannot be pinned down is a
	// pointer loaded into a register and walked, and Module.Write reports
	// those rather than pretending.
	for _, r := range m.exactReads() {
		for i := r.off; i < r.off+r.n && i < len(covered); i++ {
			covered[i] = false
		}
	}
	var out []z80.Block
	for i := 0; i < len(covered); {
		if covered[i] {
			i++
			continue
		}
		j := i
		for j < len(covered) && !covered[j] {
			j++
		}
		out = append(out, z80.Block{
			Name: m.nameFor(i), Off: i, Data: m.ROM[i:j],
		})
		i = j
	}
	return out
}

// read is a byte range some instruction loads from an absolute address.
type read struct{ off, n int }

// exactReads are the absolute loads whose width the opcode fixes: `ld a,(nn)`
// reads one byte, `ld hl,(nn)` and the ED-prefixed pair loads read two.
func (m Module) exactReads() []read {
	var out []read
	for _, ins := range m.Insns {
		var n int
		switch {
		case ins.Op == 0x3A: // ld a,(nn)
			n = 1
		case ins.Op == 0x2A: // ld hl,(nn)
			n = 2
		case ins.Op == 0xED && (ins.Sub == 0x4B || ins.Sub == 0x5B ||
			ins.Sub == 0x6B || ins.Sub == 0x7B): // ld rr,(nn)
			n = 2
		default:
			continue
		}
		if len(ins.Refs) == 0 {
			continue
		}
		off := m.offsetOf(ins.Refs[0])
		if off >= 0 {
			out = append(out, read{off, n})
		}
	}
	return out
}

// PointerLoads are the `ld rr,nnnn` whose target is a byte the translation
// covers. Their width is not knowable, so they are reported rather than
// guessed at: if the cartridge really walks one of them, the msxcheck build
// says so at the address it happens.
func (m Module) PointerLoads() []uint16 {
	covered := make([]bool, len(m.ROM))
	for off, ins := range m.Insns {
		for i := off; i < off+ins.Len && i < len(covered); i++ {
			covered[i] = true
		}
	}
	seen := map[uint16]bool{}
	var out []uint16
	for _, ins := range m.Insns {
		if ins.Op != 0x01 && ins.Op != 0x11 && ins.Op != 0x21 {
			continue
		}
		if len(ins.Refs) == 0 {
			continue
		}
		off := m.offsetOf(ins.Refs[0])
		if off < 0 || !covered[off] || seen[ins.Refs[0]] {
			continue
		}
		seen[ins.Refs[0]] = true
		out = append(out, ins.Refs[0])
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// offsetOf turns a logical address into an image offset under the initial
// paging, or -1 if it is not in the cartridge at all.
func (m Module) offsetOf(addr uint16) int {
	off := m.Info.Mapper.Phys(m.Info.Mapper.Initial, int(addr),
		m.Info.Mapper.BankCount(len(m.ROM)))
	if off < 0 || off >= len(m.ROM) {
		return -1
	}
	return off
}

// nameFor is what to call the block starting at an offset.
func (m Module) nameFor(off int) string {
	addr := int(m.Base) + off
	for _, r := range m.Names {
		if addr >= r.Start && addr < r.End && r.Name != "" {
			return r.Name
		}
	}
	return fmt.Sprintf("data%04X", addr)
}

// meta writes what the cartridge is, including its mapper as a literal, so
// that the running machine pages exactly as the tracer did.
func (m Module) meta() ([]byte, error) {
	var b bytes.Buffer
	f := func(s string, a ...any) { fmt.Fprintf(&b, s+"\n", a...) }
	f("// Code generated by msx2go. DO NOT EDIT.")
	f("")
	f("package z80")
	f("")
	f("// Cartridge is what msx2go recorded about the image this code was")
	f("// translated from. The translation is only true of those bytes, which")
	f("// is why the SHA-1 is here and is checked.")
	f("var Cartridge = Info{")
	f("\tName:    %q,", m.Info.Name)
	f("\tMachine: %q,", m.Info.Machine)
	f("\tSize:    %d,", m.Info.Size)
	f("\tFill:    0x%02X,", m.Info.Fill)
	if m.Info.MainThread {
		f("\tMainThread: true,")
	}
	if m.Info.Floppy {
		f("\tFloppy: true,")
	}
	if m.Info.Run != "" {
		f("\tRun: %q,", m.Info.Run)
	}
	if len(m.Info.DiskSizes) > 1 {
		f("\tDiskSizes: []int{%s},", ints(m.Info.DiskSizes))
	}
	if m.Info.TransSHA1 != "" {
		f("\tTransBase: 0x%04X,", m.Info.TransBase)
		f("\tTransSize: %d,", m.Info.TransSize)
		f("\tTransSHA1: %q,", m.Info.TransSHA1)
	}
	f("\tSHA1:    %q,", m.Info.SHA1)
	f("\tMapper: Mapper{")
	f("\t\tName:     %q,", m.Info.Mapper.Name)
	f("\t\tBankSize: 0x%X,", m.Info.Mapper.BankSize)
	f("\t\tPages: [][2]int{")
	for _, p := range m.Info.Mapper.Pages {
		f("\t\t\t{0x%04X, 0x%04X},", p[0], p[1])
	}
	f("\t\t},")
	f("\t\tInitial: []int{%s},", ints(m.Info.Mapper.Initial))
	if len(m.Info.Mapper.Switches) > 0 {
		f("\t\tSwitches: []Switch{")
		for _, s := range m.Info.Mapper.Switches {
			f("\t\t\t{0x%04X, 0x%04X, %d},", s.Lo, s.Hi, s.Page)
		}
		f("\t\t},")
	}
	if m.Info.Mapper.SCC {
		f("\t\tSCC: true,")
	}
	f("\t},")
	f("}")
	f("")
	f("// Base is where the cartridge is mapped, which is where Boot looks")
	f("// for the header.")
	f("const Base = 0x%04X", m.Base)
	return format.Source(b.Bytes())
}

// data writes the kept runs as named Go slices.
//
// They are strings rather than byte-slice literals because a quarter of a
// megabyte of `0x12, 0x34,` takes the compiler a very long time and a string
// constant takes it none.
func (m Module) data(blocks []z80.Block) ([]byte, error) {
	var b bytes.Buffer
	f := func(s string, a ...any) { fmt.Fprintf(&b, s+"\n", a...) }
	f("// Code generated by msx2go. DO NOT EDIT.")
	f("")
	f("//go:build msxdata")
	f("")
	f("package z80")
	f("")
	f("// The cartridge's data: every run of bytes no translated instruction")
	f("// covers. The code that reads them is in rom_gen.go; these are what it")
	f("// reads. Built only with the msxdata tag -- without it the same blocks")
	f("// are looked for on disk. See data.go.")
	f("var dataBlocks = []Block{")
	for i, blk := range blocks {
		f("\t{Name: %q, Off: 0x%05X, Data: []byte(d%d)},",
			blk.Name, blk.Off, i)
	}
	f("}")
	f("")
	for i, blk := range blocks {
		f("// d%d is %s: %d bytes at %04Xh.",
			i, blk.Name, len(blk.Data), int(m.Base)+blk.Off)
		f("const d%d = %s", i, goString(blk.Data))
	}
	return format.Source(b.Bytes())
}

// goString renders bytes as a Go string constant, split so no line is absurd.
func goString(b []byte) string {
	const per = 24
	var out strings.Builder
	for i := 0; i < len(b); i += per {
		j := i + per
		if j > len(b) {
			j = len(b)
		}
		if i > 0 {
			out.WriteString(" +\n\t")
		}
		out.WriteByte('"')
		for _, c := range b[i:j] {
			fmt.Fprintf(&out, "\\x%02x", c)
		}
		out.WriteByte('"')
	}
	if len(b) == 0 {
		return `""`
	}
	return out.String()
}

func ints(v []int) string {
	s := make([]string, len(v))
	for i, x := range v {
		s[i] = fmt.Sprint(x)
	}
	return strings.Join(s, ", ")
}

// sortBlocks keeps the generated file stable between runs.
func sortBlocks(b []z80.Block) {
	sort.Slice(b, func(i, j int) bool { return b[i].Off < b[j].Off })
}

// moduleNotice ships with every generated module: the character set in the
// runtime's font.go is C-BIOS's, and its BSD notice must travel with any
// redistribution, source or binary.
const moduleNotice = `This module was generated by msx2go
(https://github.com/brunoga/msx2go).

It includes the character set (font glyph data) from C-BIOS
(http://cbios.sourceforge.net/), in z80/font.go, distributed under the
BSD 2-Clause license:

  Copyright (c) 2002-2005 BouKiCHi
                2003      Reikan
                2004-2005 Patrick van Arkel
                2004-2006 Joost Yervante Damad
                2004-2006 Jussi Pitkanen
                2004-2008 Eric Boon
                2004-2011 Albert Beevendorp
                2004-2011 Manuel Bilderbeek
                2004-2014 Maarten ter Huurne
                2010      FRS
  All rights reserved.

  Redistribution and use in source and binary forms, with or without
  modification, are permitted provided that the following conditions
  are met:
  1. Redistributions of source code must retain the above copyright
     notice, this list of conditions and the following disclaimer.
  2. Redistributions in binary form must reproduce the above copyright
     notice, this list of conditions and the following disclaimer in the
     documentation and/or other materials provided with the distribution.

  THIS SOFTWARE IS PROVIDED BY THE AUTHOR ` + "``" + `AS IS'' AND ANY EXPRESS OR
  IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES
  OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE DISCLAIMED.
  IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY DIRECT, INDIRECT,
  INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT
  NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
  DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
  THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
  (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE OF
  THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.

The game code and data in rom_gen.go, data_gen.go and any .dat sidecar are
derived from the image this module was generated from, and carry that
image's copyright.
`
