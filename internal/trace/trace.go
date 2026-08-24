package trace

import (
	"fmt"
	"sort"

	"github.com/brunoga/msx2go/internal/dis"
	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
)

// maxStates caps the search. Every distinct (address, banks, accumulator) is a
// state, and a program that indexes a dispatcher with a value the tracer
// cannot pin down can mint a great many of them.
const maxStates = 400000

// Config is what a cartridge needs said about it that cannot be read off the
// bytes. Everything here is a last resort: the tracer finds entry points,
// dispatchers and tables on its own, and each of these exists because some
// cartridge does something no amount of looking will reveal.
type Config struct {
	// Base is where the cartridge is mapped, and Mapper how it pages.
	Base   Addr   `json:"base"`
	Mapper string `json:"mapper"`
	// Entries are extra places execution can begin: an alternate entry to
	// a routine that nothing reaches by a call, usually.
	Entries []Entry `json:"entries"`
	// InlineDispatchers are `call` targets followed by a table of code
	// pointers rather than by code. They are detected as well as declared.
	InlineDispatchers []Addr `json:"inline_dispatchers"`
	// StopAfterCall are calls that never return.
	StopAfterCall []Addr `json:"stop_after_call"`
	// ForceData are ranges to refuse to decode, for data that happens to
	// look like reachable code.
	ForceData []Range `json:"force_data"`
	// Tables are pointer tables the tracer cannot find on its own.
	Tables []TableSpec `json:"tables"`
	// NopRunLimit is how many zero bytes in a row read as padding rather
	// than as code. AllowRST38 lets `rst 38h` be real.
	NopRunLimit int  `json:"nop_run_limit"`
	AllowRST38  bool `json:"allow_rst38"`
	// BankShadows are RAM bytes holding which bank is in each page. They
	// are discovered by tracing; declaring one only saves a round.
	BankShadows map[Addr]int `json:"bank_shadows"`
	// AcceptAllPushedReturns takes every `ld rr,nn` + `push rr` at face
	// value, which is what tools/z80trace.py does. It exists so that the
	// difference between the two tracers can be shown to be exactly this
	// and nothing else; nobody should turn it on to trace a cartridge.
	AcceptAllPushedReturns bool `json:"accept_all_pushed_returns"`
}

// Addr is a hex-or-decimal address in the config, written as a string.
type Addr uint16

// Entry is a place execution can begin, and why anyone thought so.
type Entry struct {
	Addr   Addr   `json:"addr"`
	Reason string `json:"reason"`
}

// Range is a half-open span of addresses.
type Range struct {
	Start Addr `json:"start"`
	End   Addr `json:"end"`
}

// TableSpec is a declared pointer table.
type TableSpec struct {
	Addr   Addr   `json:"addr"`
	Count  int    `json:"count"`
	Kind   string `json:"kind"` // "code" or "data"
	Reason string `json:"reason"`
}

// Coverage is what a byte turned out to be.
const (
	CovData = iota
	CovCode
	CovPointer
)

// Table is a run of 16-bit pointers, and where the tracer learnt of it.
type Table struct {
	Addr  uint16
	Count int
	Kind  string
	From  string
}

// Result is what a trace found.
type Result struct {
	// Insns is every instruction, by its offset in the image, and Logical
	// the address it was decoded at.
	Insns   map[int]dis.Insn
	Logical map[int]uint16
	// Starts is every address the code can be entered at, sorted. This is
	// the emitter's input and the whole point of the exercise.
	Starts []uint16
	// Cov is one byte per image byte: data, code or pointer.
	Cov []byte
	// SiteBanks is the paging each instruction was decoded under, by
	// offset. See where it is filled in.
	SiteBanks map[int][]int
	// EntryReason says how each address was first reached, which is the
	// first thing to look at when the trace goes somewhere surprising.
	EntryReason map[uint16]string
	// CallTargets and JumpTargets are the sites that reach each address.
	CallTargets map[uint16][]uint16
	JumpTargets map[uint16][]uint16
	// DataRefs are immediates that point into the image, by offset;
	// ExtRefs those that point outside it, which is RAM and the BIOS.
	DataRefs map[int][]uint16
	ExtRefs  map[uint16][]uint16
	// PortIO is which addresses touch which port.
	PortIO map[byte][]uint16
	// Tables are the pointer tables, by address.
	Tables map[uint16]Table
	// IndirectJumps are the `jp (hl)` sites, which are where a trace goes
	// blind unless something else resolves them.
	IndirectJumps []uint16
	// Hooks are interrupt handlers installed by writing to the hook area.
	Hooks map[uint16]string
	// PushedReturns are addresses reached by `ld hl,nn / push hl` where
	// the value really is consumed by a ret.
	PushedReturns map[uint16][]uint16
	// RejectedReturns are the ones that looked like it and were not: a
	// pointer loaded and saved. They are the difference between this
	// tracer and the Python one, and each is a byte of data that would
	// otherwise have been translated as code.
	RejectedReturns []uint16
	// Conflicts are image bytes decoded at two different addresses, which
	// for a flat image means something has gone wrong.
	Conflicts []Conflict
	// Bad is addresses that would not decode.
	Bad map[uint16]string
	// PaddingStops are the runs of FFh or 00h the trace refused to walk
	// into, which is usually the right answer and occasionally the reason
	// something is missing.
	PaddingStops []PaddingStop
	// Dispatchers is the set of inline-table dispatchers the trace ended
	// up using, declared and detected together.
	Dispatchers []Addr
	// ShadowCandidates are RAM bytes that behave like a bank shadow, by
	// the page each holds, found in the round that produced this result.
	// Shadows is the set the whole fixed point settled on. See banks.go.
	ShadowCandidates map[int]int
	Shadows          map[int]int
	// BankSwitches is every write to a bank register the trace saw.
	BankSwitches []BankSwitch
	// ObservedPages is which banks were ever put in each page.
	ObservedPages map[int][]int
	// ShadowRestores are the writes that put a page back from its shadow,
	// and UnresolvedSwitches the ones where the bank could not be worked
	// out at all -- each of which stopped a walk, so each is code that
	// may be missing.
	ShadowRestores     []Restore
	UnresolvedSwitches []Restore
	// States is how many distinct trace states were explored, and
	// Overflowed whether the cap stopped it early.
	States     int
	Overflowed bool
}

// BankSwitch is a write to a bank register: where, which page, which bank.
type BankSwitch struct {
	At   uint16
	Page int
	Bank int
}

// Restore is a bank-register write the trace could not evaluate, and the page
// it was for.
type Restore struct {
	At   uint16
	Page int
}

// Conflict is one image byte decoded at two addresses.
type Conflict struct {
	Off        int
	First, Now uint16
}

// PaddingStop is where the trace stopped, and what it took for padding.
type PaddingStop struct {
	Addr uint16
	Why  string
}

// Tracer walks a cartridge.
type Tracer struct {
	rom    []byte
	mapper z80.Mapper
	base   int
	nbanks int
	cfg    Config

	res   Result
	visit map[state]bool

	dispatchers map[uint16]bool
	stopAfter   map[uint16]bool
	candidates  []candidate

	// shadowMap is the RAM byte holding the bank in each page, by address,
	// with -1 for anything that is not one. shadowIdx numbers them so a
	// trace state can carry their values.
	shadowMap   map[int]int
	shadowIdx   map[int]int
	shadowAddrs []int
	shadowNotes map[int]shadowNote
	recent      []recentStore
}

// candidate is a `ld rr,nn` + `push rr` that may or may not be pushing a
// return address.
//
// It looks the same either way, and often is one. It is also exactly what
// loading a pointer and saving it looks like: King's Valley's arrow drawer
// begins `ld de,7A79h` -- the arrow *pattern table* -- and falls into a
// routine whose first instruction is `push de`, keeping it while it works.
// Taking that for a return address sends the trace into the table and through
// the ending text beyond it, which disassembles perfectly well and is not code
// at all. Every byte of it would then be translated, and pruned out of the
// data as a result, and the game would draw its arrows out of nothing.
//
// What separates the two is whether the push begins a routine. A push that
// something jumps or calls to is a routine's own first instruction and has
// nothing to do with the load before it; a push in the middle of a routine is
// the idiom. That question can only be answered once the trace has found every
// call and jump, so these are collected and decided afterwards -- and if any
// are accepted, the code they reach may reveal more, so it runs again.
type candidate struct {
	target, push   uint16
	banks, shadows []int
	regs           *Regs
}

// state is one point in the search: where, under what paging, with what in the
// accumulator. A dispatcher indexes its table with A, so two arrivals at the
// same address with different A are genuinely different and both have to run.
type state struct {
	addr    uint16
	banks   uint64
	shadows uint64
	acc     int16
}

// work is a queued state, with the register file it arrived with.
type work struct {
	addr    uint16
	banks   []int
	shadows []int
	regs    *Regs
	reason  string
}

// New prepares a trace of an image.
func New(rom []byte, mapper z80.Mapper, cfg Config) *Tracer {
	if cfg.NopRunLimit == 0 {
		cfg.NopRunLimit = 8
	}
	t := &Tracer{
		rom: rom, mapper: mapper, base: int(cfg.Base),
		nbanks: mapper.BankCount(len(rom)), cfg: cfg,
		visit:       map[state]bool{},
		dispatchers: map[uint16]bool{},
		stopAfter:   map[uint16]bool{},
	}
	if t.base == 0 {
		t.base = 0x4000
	}
	for _, a := range cfg.InlineDispatchers {
		t.dispatchers[uint16(a)] = true
	}
	for _, a := range cfg.StopAfterCall {
		t.stopAfter[uint16(a)] = true
	}
	t.shadowMap = map[int]int{}
	t.shadowNotes = map[int]shadowNote{}
	for a, p := range cfg.BankShadows {
		t.shadowMap[int(a)] = p
	}
	for a := range t.shadowMap {
		t.shadowAddrs = append(t.shadowAddrs, a)
	}
	sort.Ints(t.shadowAddrs)
	t.shadowIdx = map[int]int{}
	for i, a := range t.shadowAddrs {
		t.shadowIdx[a] = i
	}
	t.res = Result{
		Insns: map[int]dis.Insn{}, Logical: map[int]uint16{},
		SiteBanks:        map[int][]int{},
		Cov:              make([]byte, len(rom)),
		EntryReason:      map[uint16]string{},
		CallTargets:      map[uint16][]uint16{},
		JumpTargets:      map[uint16][]uint16{},
		DataRefs:         map[int][]uint16{},
		ExtRefs:          map[uint16][]uint16{},
		PortIO:           map[byte][]uint16{},
		Tables:           map[uint16]Table{},
		Hooks:            map[uint16]string{},
		PushedReturns:    map[uint16][]uint16{},
		Bad:              map[uint16]string{},
		ShadowCandidates: map[int]int{},
		ObservedPages:    map[int][]int{},
	}
	return t
}

// HeaderEntries are the entry points a cartridge declares in its own header:
// INIT, STATEMENT, DEVICE and TEXT, whichever are non-zero.
func HeaderEntries(rom []byte) []Entry {
	if len(rom) < 10 || rom[0] != 'A' || rom[1] != 'B' {
		return nil
	}
	var out []Entry
	for i, name := range []string{"INIT", "STATEMENT", "DEVICE", "TEXT"} {
		a := uint16(rom[2+i*2]) | uint16(rom[3+i*2])<<8
		if a != 0 {
			out = append(out, Entry{Addr(a), "ROM header " + name})
		}
	}
	return out
}

// view reads the image under a paging state.
type view struct {
	t       *Tracer
	banks   []int
	shadows []int
}

func (v view) phys(addr uint16) int {
	off := v.t.mapper.Phys(v.banks, int(addr), v.t.nbanks)
	if off < 0 || off >= len(v.t.rom) {
		return -1
	}
	return off
}

func (v view) Readable(addr uint16, n int) bool {
	for i := 0; i < n; i++ {
		if v.phys(addr+uint16(i)) < 0 {
			return false
		}
	}
	return true
}

func (v view) Byte(addr uint16) byte { return v.t.rom[v.phys(addr)] }

func (v view) Word(addr uint16) uint16 {
	return uint16(v.Byte(addr)) | uint16(v.Byte(addr+1))<<8
}

// bankKey packs a paging state into something a map can hold. Four pages of
// eight bits is every mapper in Mappers.
func bankKey(banks []int) uint64 {
	var k uint64
	for i, b := range banks {
		if i >= 8 {
			break
		}
		k |= uint64(byte(b)) << (8 * i)
	}
	return k
}

// State is a place execution was seen to be, with the paging that was in
// force. It is what the discovery loop feeds back: an address the trace never
// reached, caught by actually running the cartridge.
type State struct {
	Addr   uint16
	Banks  []int
	Reason string
}

// Run traces from a set of entry points, and from any states discovered by
// running the cartridge.
func (t *Tracer) Run(entries []Entry, extra []State) *Result {
	initial := append([]int(nil), t.mapper.Initial...)
	shadow0 := make([]int, len(t.shadowAddrs))
	for i := range shadow0 {
		shadow0[i] = unknown
	}
	var queue []work
	for _, e := range entries {
		t.enqueue(&queue, uint16(e.Addr), initial, shadow0, nil, e.Reason)
	}
	for _, st := range extra {
		banks := st.Banks
		if len(banks) != len(initial) {
			banks = initial
		}
		reason := st.Reason
		if reason == "" {
			reason = "seen while running"
		}
		t.enqueue(&queue, st.Addr, banks, shadow0, nil, reason)
	}
	for _, spec := range t.cfg.Tables {
		t.declaredTable(&queue, spec, initial, shadow0)
	}

	for round := 0; ; round++ {
		for len(queue) > 0 {
			w := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			t.walk(&queue, w)
		}
		if round >= 8 || !t.decideCandidates(&queue) {
			break
		}
	}

	t.finish()
	return &t.res
}

// decideCandidates settles the pushed-return candidates now that every call
// and jump target is known, and reports whether any were accepted.
func (t *Tracer) decideCandidates(queue *[]work) bool {
	pending := t.candidates
	t.candidates = nil
	grew := false
	for _, c := range pending {
		if !t.cfg.AcceptAllPushedReturns && t.entryPoint(c.push) {
			// The push begins a routine; the load before it was
			// somebody else's business.
			t.res.RejectedReturns = append(t.res.RejectedReturns, c.target)
			continue
		}
		if _, seen := t.res.PushedReturns[c.target]; !seen {
			grew = true
		}
		t.res.PushedReturns[c.target] =
			append(t.res.PushedReturns[c.target], c.push)
		t.enqueue(queue, c.target, c.banks, c.shadows, c.regs,
			fmt.Sprintf("pushed return from %04x", c.push))
	}
	return grew && len(*queue) > 0
}

// entryPoint reports whether anything jumps or calls to an address, which is
// what makes it a routine's own beginning rather than a step in one.
func (t *Tracer) entryPoint(addr uint16) bool {
	return len(t.res.CallTargets[addr]) > 0 || len(t.res.JumpTargets[addr]) > 0
}

// declaredTable marks a pointer table the config names and queues its entries.
func (t *Tracer) declaredTable(queue *[]work, spec TableSpec, banks,
	shadows []int) {
	v := view{t, banks, shadows}
	base := uint16(spec.Addr)
	kind := spec.Kind
	if kind == "" {
		kind = "code"
	}
	from := spec.Reason
	if from == "" {
		from = "config"
	}
	t.res.Tables[base] = Table{base, spec.Count, kind, from}
	t.mark(v, base, spec.Count*2, CovPointer)
	if kind != "code" {
		return
	}
	for i := 0; i < spec.Count; i++ {
		at := base + uint16(i*2)
		if !v.Readable(at, 2) {
			continue
		}
		t.enqueue(queue, v.Word(at), banks, shadows, nil,
			fmt.Sprintf("table %04x[%d]", base, i))
	}
}

// walk follows one state until the code runs out.
func (t *Tracer) walk(queue *[]work, w work) {
	addr, banks, shadows, regs := w.addr, w.banks, w.shadows, w.regs
	if regs == nil {
		regs = NewRegs()
	}
	entry := banks
	for {
		v := view{t, banks, shadows}
		if len(t.visit) > maxStates {
			t.res.Overflowed = true
			return
		}
		acc := int16(regs.A())
		st := state{addr, bankKey(banks), bankKey(shadows), acc}
		if t.visit[st] {
			return
		}
		off := v.phys(addr)
		if off < 0 || t.forcedData(addr) {
			return
		}
		t.visit[st] = true

		ins, ok := dis.Decode(v, addr)
		if !ok {
			t.res.Bad[addr] = "unmapped or truncated"
			return
		}
		if why, pad := t.padding(v, ins); pad {
			t.res.PaddingStops = append(t.res.PaddingStops,
				PaddingStop{addr, why})
			return
		}

		if prev, seen := t.res.Logical[off]; seen && prev != addr {
			t.res.Conflicts = append(t.res.Conflicts,
				Conflict{off, prev, addr})
		} else {
			t.res.Logical[off] = addr
			t.res.Insns[off] = ins
			// And the paging it was decoded under. An instruction
			// at the end of a bank reads its operands out of the
			// next page, which is a different bank, so the address
			// alone does not say what its bytes were.
			t.res.SiteBanks[off] = append([]int(nil), banks...)
		}
		t.mark(v, addr, ins.Len, CovCode)

		for _, r := range ins.Refs {
			if p := v.phys(r); p >= 0 {
				t.res.DataRefs[p] = append(t.res.DataRefs[p], addr)
			} else {
				t.res.ExtRefs[r] = append(t.res.ExtRefs[r], addr)
			}
		}
		if ins.Op == 0xD3 || ins.Op == 0xDB {
			p := v.Byte(addr + 1)
			t.res.PortIO[p] = append(t.res.PortIO[p], addr)
		}
		t.pushedReturn(queue, v, ins, banks, shadows, regs)

		stores := Step(regs, v, ins, v)
		var stop bool
		banks, shadows, stop = t.applyStores(stores, addr, banks, entry,
			shadows)
		t.installedHook(queue, v, ins, banks, shadows, regs)
		if stop {
			return
		}

		switch ins.Kind {
		case dis.Call:
			t.res.CallTargets[ins.Target] =
				append(t.res.CallTargets[ins.Target], addr)
			t.enqueue(queue, ins.Target, banks, shadows, regs,
				fmt.Sprintf("call from %04x", addr))
			if t.dispatchers[ins.Target] {
				addr = t.inlineTable(queue, v, ins.End(), banks,
					shadows, regs, fmt.Sprintf("%04x", addr))
				continue
			}
			if t.stopAfter[ins.Target] {
				return
			}
		case dis.Jp, dis.Jr, dis.Djnz:
			t.res.JumpTargets[ins.Target] =
				append(t.res.JumpTargets[ins.Target], addr)
			t.enqueue(queue, ins.Target, banks, shadows, regs,
				fmt.Sprintf("jump from %04x", addr))
		case dis.Ijp:
			t.res.IndirectJumps = append(t.res.IndirectJumps, addr)
		case dis.Rst:
			t.res.CallTargets[ins.Target] =
				append(t.res.CallTargets[ins.Target], addr)
		}

		if !ins.FallsThrough() {
			return
		}
		addr = ins.End()
	}
}

// padding decides whether the trace has walked into filler.
//
// Padding decodes as instructions that fall through, so a run of it turns into
// thousands of bytes of fake code: FFh is `rst 38h` and 00h is `nop`. On an
// MSX cartridge 38h is the interrupt vector and calling it is essentially
// unheard of, and no compiler or hand-written Z80 emits eight NOPs in a row.
// Either one means the path went wrong, so stop before marking anything.
func (t *Tracer) padding(v view, ins dis.Insn) (string, bool) {
	if ins.Kind == dis.Rst && ins.Target == 0x38 && !t.cfg.AllowRST38 {
		return "rst 38h", true
	}
	if ins.Op == 0x00 && v.Readable(ins.Addr, t.cfg.NopRunLimit) {
		all := true
		for i := 0; i < t.cfg.NopRunLimit; i++ {
			if v.Byte(ins.Addr+uint16(i)) != 0 {
				all = false
				break
			}
		}
		if all {
			return "nop run", true
		}
	}
	return "", false
}

// pushedReturn handles `ld rr,nn` followed by `push rr`, which pushes nn as a
// return address: the routine that follows will `ret` to it.
func (t *Tracer) pushedReturn(queue *[]work, v view, ins dis.Insn,
	banks, shadows []int, regs *Regs) {
	var wantPush byte
	switch ins.Op {
	case 0x01:
		wantPush = 0xC5
	case 0x11:
		wantPush = 0xD5
	case 0x21:
		wantPush = 0xE5
	default:
		return
	}
	if !v.Readable(ins.End(), 1) || v.Byte(ins.End()) != wantPush {
		return
	}
	if len(ins.Refs) == 0 {
		return
	}
	tgt := ins.Refs[0]
	if v.phys(tgt) < 0 {
		return
	}
	// Whether this really is a pushed return cannot be decided here: it
	// turns on whether the push is the first instruction of a routine, and
	// that is not known until the trace has found every call and jump. So
	// it is a candidate, and Run decides once it has.
	t.candidates = append(t.candidates,
		candidate{tgt, ins.End(), banks, shadows, regs.Copy()})
}

// hookArea is the MSX interrupt hook area in page 3 RAM. A cartridge installs
// a handler by writing `jp nnnn` there, and nothing calls that handler
// statically -- so without following the write the trace stops at the idle
// loop and finds none of the game.
var hookArea = [2]uint16{0xFD9A, 0xFFCA}

var hookNames = map[uint16]string{0xFD9A: "H.KEYI", 0xFD9F: "H.TIMI"}

// installedHook follows `ld (hook+1),hl`.
//
// Step leaves HL alone for this opcode, so reading it back after the step is
// safe and gets the address that was written.
func (t *Tracer) installedHook(queue *[]work, v view, ins dis.Insn,
	banks, shadows []int, regs *Regs) {
	var dst uint16
	switch {
	case ins.Op == 0x22:
		dst = v.Word(ins.Addr + 1)
	case ins.Op == 0xED && ins.Sub == 0x63:
		dst = v.Word(ins.Addr + 2)
	default:
		return
	}
	if dst < hookArea[0] || dst >= hookArea[1] {
		return
	}
	hl := regs.Pair(2)
	if hl == unknown {
		return
	}
	name := hookNames[dst-1]
	if name == "" {
		name = fmt.Sprintf("hook %04Xh", dst-1)
	}
	if _, seen := t.res.Hooks[uint16(hl)]; !seen {
		t.res.Hooks[uint16(hl)] = name
	}
	t.enqueue(queue, uint16(hl), banks, shadows, regs,
		fmt.Sprintf("%s installed at %04x", name, ins.Addr))
}

// inlineTable reads a table of code pointers that follows a call.
//
// The table ends at the first of: a word that is not a plausible address in
// the image, a byte already known to be code, the lowest target seen so far --
// handlers almost always follow the table directly, so it cannot extend into
// its own first handler -- or a target in a different page from the first
// entry, since a dispatch table jumps within one bank of code and a stray word
// is an outlier. Without that last rule the table runs one entry past its end
// and swallows the first two bytes of the code that follows, which then read
// as a plausible-looking address somewhere else entirely.
func (t *Tracer) inlineTable(queue *[]work, v view, at uint16, banks,
	shadows []int, regs *Regs, site string) uint16 {
	var entries []uint16
	a, limit := at, 0x10000
	firstPage := -2
	for int(a) < limit && v.Readable(a, 2) {
		if a != at {
			if p := v.phys(a); p >= 0 && t.res.Cov[p] == CovCode {
				break
			}
		}
		w := v.Word(a)
		if v.phys(w) < 0 {
			break
		}
		page := t.mapper.PageOf(int(w))
		if firstPage == -2 {
			firstPage = page
		} else if page != firstPage {
			break
		}
		entries = append(entries, w)
		a += 2
		if int(w) > int(at) && int(w) < limit {
			limit = int(w)
		}
		if len(entries) > 256 {
			break
		}
	}
	if len(entries) == 0 {
		return at
	}
	t.res.Tables[at] = Table{at, len(entries), "code", site}
	t.mark(v, at, len(entries)*2, CovPointer)
	for _, w := range entries {
		t.enqueue(queue, w, banks, shadows, regs,
			fmt.Sprintf("inline jump table at %04x", at))
	}
	return a
}

// mark records what a run of bytes turned out to be.
func (t *Tracer) mark(v view, addr uint16, n int, what byte) {
	for i := 0; i < n; i++ {
		if p := v.phys(addr + uint16(i)); p >= 0 {
			t.res.Cov[p] = what
		}
	}
}

func (t *Tracer) forcedData(addr uint16) bool {
	for _, r := range t.cfg.ForceData {
		if addr >= uint16(r.Start) && addr < uint16(r.End) {
			return true
		}
	}
	return false
}

// enqueue queues a state.
//
// The abstract register file travels with it: a `jr` into the tail of a
// multi-entry routine must not lose the `ld a,N` that preceded it, which is
// exactly how MSX bank-set helpers are written.
func (t *Tracer) enqueue(queue *[]work, addr uint16, banks, shadows []int,
	regs *Regs, reason string) {
	v := view{t, banks, shadows}
	if v.phys(addr) < 0 || t.forcedData(addr) {
		return
	}
	if _, seen := t.res.EntryReason[addr]; !seen {
		t.res.EntryReason[addr] = reason
	}
	acc := int16(unknown)
	var copied *Regs
	if regs != nil {
		acc = int16(regs.A())
		copied = regs.Copy()
	}
	if t.visit[state{addr, bankKey(banks), bankKey(shadows), acc}] {
		return
	}
	*queue = append(*queue, work{addr, banks, shadows, copied, reason})
}

// finish sorts what has to be sorted, so that two runs of the same trace
// produce the same report rather than whatever the maps felt like.
func (t *Tracer) finish() {
	t.res.States = len(t.visit)
	starts := make([]uint16, 0, len(t.res.Logical))
	for _, a := range t.res.Logical {
		starts = append(starts, a)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
	t.res.Starts = starts
	sort.Slice(t.res.IndirectJumps, func(i, j int) bool {
		return t.res.IndirectJumps[i] < t.res.IndirectJumps[j]
	})
	t.res.RejectedReturns = uniq(t.res.RejectedReturns)
	for _, m := range []map[uint16][]uint16{
		t.res.CallTargets, t.res.JumpTargets, t.res.ExtRefs,
		t.res.PushedReturns,
	} {
		for k, v := range m {
			m[k] = uniq(v)
		}
	}
	for k, v := range t.res.DataRefs {
		t.res.DataRefs[k] = uniq(v)
	}
	for k, v := range t.res.PortIO {
		t.res.PortIO[k] = uniq(v)
	}
}

func uniq(v []uint16) []uint16 {
	sort.Slice(v, func(i, j int) bool { return v[i] < v[j] })
	out := v[:0]
	for i, x := range v {
		if i == 0 || x != out[len(out)-1] {
			out = append(out, x)
		}
	}
	return out
}
