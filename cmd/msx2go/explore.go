package main

// Exploration maps code by forcing it to run.
//
// The static tracer takes both arms of every conditional for free; what it
// cannot see is the dynamic transfer -- `jp (hl)`, a dispatcher's table, a
// threaded interpreter -- whose target is data. A sweep sees those targets
// concretely but only along the paths a player happens to take. Exploration
// is the third producer of sites: run the machine concretely, and at every
// conditional branch fork it, one copy down each arm. The forks' histories
// are real machine states, so every dynamic jump has a concrete target; and
// because the goal is *code* coverage, not path coverage, a branch site is
// forked at most once per arm and the work stays linear in the code.
//
// A forced arm can be a lie -- a loop guard taken the wrong way walks off
// the end of its table -- so forks die on the signs of nonsense (a stack
// pointer in page zero, an FFh sled, a long stretch of nothing new), and
// what exploration writes are *sites*, subject to the same verification as
// every other site: the translation they seed is checked against the
// interpreter twin, and an address that was never really code just becomes
// a label nothing jumps to.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brunoga/msx2go/internal/dis"
	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
	"github.com/brunoga/msx2go/internal/trace"
)

// exploredPath is where exploration's findings go: beside sites.txt, but
// never in it. The two files carry different kinds of truth. A site is an
// observation -- the address ran -- and pruning is entitled to believe it.
// An explored address ran only under a forced branch, so it seeds the
// tracer with candidates but must never tell the pruner a data byte is
// code: a forced arm that walked into a table would prune the table.
func exploredPath(out string) string {
	return filepath.Join(out, "explored.txt")
}

// exploreImage boots a cartridge and explores from there. What it finds
// goes to explored.txt; sites.txt keeps only what was really seen to run.
func exploreImage(rom []byte, info z80.Info, base uint16, out string,
	budget, quota int) error {
	seen := map[string]bool{}
	for _, f := range []string{sitesPath("", out), exploredPath(out)} {
		if old, err := trace.ReadSites(f); err == nil {
			for _, st := range old {
				seen[siteLine(st.Addr, st.Banks)] = true
			}
		}
	}
	before := len(seen)
	fresh := func() *z80.M { return z80.New(rom, info.Mapper) }
	m := fresh()
	if err := m.InterpretRun(base, 1, quota, nil); err != nil {
		return fmt.Errorf("booting for exploration: %w", err)
	}
	explore(m, fresh, budget, seen)
	if err := writeSites(exploredPath(out), seen,
		"# Written by msx2go -explore: forced arms are candidates, not "+
			"observations. The tracer reads them; pruning does not.\n"); err != nil {
		return err
	}
	fmt.Printf("  explore  %d instruction budget: %d address(es), %d new "+
		"-> %s\n", budget, len(seen), len(seen)-before, exploredPath(out))
	return nil
}

// writeSites writes the merged site set in the form trace.ReadSites reads.
func writeSites(path string, seen map[string]bool, header string) error {
	lines := make([]string, 0, len(seen))
	for s := range seen {
		lines = append(lines, s)
	}
	sort.Strings(lines)
	var b strings.Builder
	b.WriteString("# Every address the machine was seen to execute, with " +
		"the banks in force.\n")
	b.WriteString(header)
	for _, s := range lines {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// memView adapts the machine's flat address space to the disassembler. The
// machine keeps one flat Mem with banks paged into it, so the current view
// is exactly what executes.
type memView struct{ m *z80.M }

func (v memView) Readable(addr uint16, n int) bool { return true }
func (v memView) Byte(a uint16) byte               { return v.m.Mem[a] }
func (v memView) Word(a uint16) uint16 {
	return uint16(v.m.Mem[a]) | uint16(v.m.Mem[a+1])<<8
}

const (
	// exploreForkBudget is how many instructions one fork may run.
	exploreForkBudget = 200_000
	// exploreStale ends a fork that has executed this many consecutive
	// already-mapped instructions: it is retreading, and any fork that
	// still has something to show gets there well within this.
	exploreStale = 4_000
)

// explore runs the fork driver from a booted machine, adding every game
// address it executes to seen (as siteLine strings) until the instruction
// budget runs out. fresh builds a machine compatible with m's snapshots --
// same image, same mapper, same disk -- for the forks to be restored into.
func explore(m *z80.M, fresh func() *z80.M, budget int,
	seen map[string]bool) int {

	save := func(m *z80.M) []byte {
		var b bytes.Buffer
		if err := m.SaveState(&b, "explore"); err != nil {
			return nil
		}
		return b.Bytes()
	}
	load := func(state []byte) *z80.M {
		mm := fresh()
		if err := mm.LoadState(bytes.NewReader(state), "explore"); err != nil {
			return nil
		}
		return mm
	}

	// The roots: the machine as it stands, and each interrupt hook entered
	// the way an interrupt enters it -- with a return address on the stack
	// that the interpreter recognises as the end of the run. FFFFh is the
	// interpreter's own sentinel, so a handler that returns simply stops.
	var queue [][]byte
	if s := save(m); s != nil {
		queue = append(queue, s)
	}
	for _, h := range m.InterruptEntries() {
		clone := load(save(m))
		if clone == nil {
			continue
		}
		clone.SP -= 2
		clone.Mem[clone.SP] = 0xFF
		clone.Mem[clone.SP+1] = 0xFF
		clone.PC = h
		if s := save(clone); s != nil {
			queue = append(queue, s)
		}
	}

	// armDone says a branch site's arm has been queued already; one fork
	// per arm is what keeps this linear.
	armDone := map[string]bool{}
	before := len(seen)
	steps := 0

	runFork := func(state []byte) {
		m := load(state)
		if m == nil {
			return
		}
		// A fork that wanders into an unimplemented BIOS entry panics
		// with the address; that is the fork's death, not the
		// explorer's.
		defer func() { recover() }()
		stale := 0
		for n := 0; n < exploreForkBudget && steps < budget; n++ {
			pc := m.PC
			if pc == 0xFFFF || m.SP < 0x4000 {
				return // returned through the root, or gone mad
			}
			if pc >= 0x4000 {
				line := siteLine(pc, m.Banks())
				if !seen[line] {
					seen[line] = true
					stale = 0
				} else if stale++; stale > exploreStale {
					return
				}
			}
			if m.Mem[pc] == 0xFF && m.Mem[pc+1] == 0xFF {
				return // an FFh sled: `rst 38h` into nothing
			}

			// Decode before stepping, to know the branch's shape;
			// step; then fork the arm that was not taken, if it is
			// new. The fork starts from the pre-step state with
			// only the program counter -- and for a call or a
			// return, the stack -- adjusted to the other arm.
			ins, ok := dis.Decode(memView{m}, pc)
			var pre []byte
			conditional := ok && ins.Cond != dis.None &&
				(ins.Kind == dis.Jp || ins.Kind == dis.Jr ||
					ins.Kind == dis.Call || ins.Kind == dis.Ret)
			if ok && ins.Kind == dis.Djnz {
				conditional = true
			}
			if conditional {
				pre = save(m)
			}

			m.Interpret(0, 1)
			steps++
			if idle, _ := m.Stopped(); idle {
				return
			}
			m.ResumeFromHalt() // an interrupt would; carry on

			if !conditional || pre == nil {
				continue
			}
			taken := m.PC != ins.End()
			var other uint16
			if taken {
				other = ins.End()
			} else if ins.Kind == dis.Ret {
				other = 0 // resolved from the fork's own stack
			} else {
				other = ins.Target
				if other < 0x4000 {
					continue // a BIOS shim; nothing to map
				}
			}
			key := fmt.Sprintf("%s>%v", siteLine(pc, m.Banks()), taken)
			if armDone[key] {
				continue
			}
			armDone[key] = true
			clone := load(pre)
			if clone == nil {
				continue
			}
			switch {
			case ins.Kind == dis.Ret && !taken:
				// Force the return: pop the address the stack
				// really holds.
				clone.PC = uint16(clone.Mem[clone.SP]) |
					uint16(clone.Mem[clone.SP+1])<<8
				clone.SP += 2
			case ins.Kind == dis.Call && !taken:
				// Force the call: the return address goes on
				// the stack the way the hardware puts it.
				clone.SP -= 2
				clone.Mem[clone.SP] = byte(ins.End())
				clone.Mem[clone.SP+1] = byte(ins.End() >> 8)
				clone.PC = ins.Target
			default:
				clone.PC = other
			}
			if s := save(clone); s != nil {
				queue = append(queue, s)
			}
		}
	}

	for len(queue) > 0 && steps < budget {
		state := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		runFork(state)
	}
	return len(seen) - before
}
