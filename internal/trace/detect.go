package trace

import (
	"sort"

	"github.com/brunoga/msx2go/internal/dis"
	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
)

// LooksLikeInlineDispatcher asks whether an address is a "jump through the
// table that follows the call" helper.
//
// The shape is shared by every Konami MSX title looked at so far:
//
//	pop hl          ; hl = the return address, i.e. the table base
//	...             ; index it by A
//	jp (hl)
//
// The test is deliberately narrow -- a `pop hl` reached from the entry with no
// push outstanding, ending at a `jp (hl)` -- because a false positive makes
// the tracer read a table out of real code, and then the real code never gets
// translated and the game panics the first time it is called.
func LooksLikeInlineDispatcher(r dis.Reader, entry uint16, limit int) bool {
	addr, popped, pushes := entry, false, 0
	for i := 0; i < limit; i++ {
		ins, ok := dis.Decode(r, addr)
		if !ok {
			return false
		}
		switch op := ins.Op; {
		case op == 0xC5 || op == 0xD5 || op == 0xE5 || op == 0xF5:
			pushes++
		case op == 0xE1: // pop hl
			if pushes == 0 {
				popped = true
			} else {
				pushes--
			}
		case op == 0xC1 || op == 0xD1 || op == 0xF1:
			if pushes > 0 {
				pushes--
			}
		case op == 0xE9: // jp (hl)
			return popped && pushes == 0
		default:
			switch ins.Kind {
			case dis.Ret, dis.Reti:
				return false
			case dis.Jp, dis.Jr:
				if ins.Cond == dis.None {
					addr = ins.Target
					continue
				}
			}
		}
		addr = ins.End()
	}
	return false
}

// Trace runs the tracer to a fixed point.
//
// Dispatchers are found by tracing and knowing about one changes what the next
// trace can reach -- a table's handlers are code nothing else refers to -- so
// the whole thing runs again until the set stops growing. Three or four rounds
// is usual; the cap is there so that a pathological image stops rather than
// spins.
func Trace(rom []byte, mapper z80.Mapper, cfg Config, entries []Entry,
	extra []State) (*Result, int) {
	found := map[uint16]bool{}
	for _, a := range cfg.InlineDispatchers {
		found[uint16(a)] = true
	}
	shadows := map[Addr]int{}
	for a, p := range cfg.BankShadows {
		shadows[a] = p
	}
	var res *Result
	rounds := 0
	for {
		rounds++
		round := cfg
		round.BankShadows = shadows
		t := New(rom, mapper, round)
		for a := range found {
			t.dispatchers[a] = true
		}
		res = t.Run(entries, extra)

		grew := false
		initial := append([]int(nil), mapper.Initial...)
		v := view{t, initial, nil}
		for target := range res.CallTargets {
			if found[target] || v.phys(target) < 0 {
				continue
			}
			if LooksLikeInlineDispatcher(v, target, 16) {
				found[target] = true
				grew = true
			}
		}
		// A shadow found this round lets the next one follow a restore
		// instead of stopping at it, which reaches code that reveals
		// more shadows. It settles in three or four rounds.
		for a, p := range res.ShadowCandidates {
			if _, seen := shadows[Addr(a)]; !seen {
				shadows[Addr(a)] = p
				grew = true
			}
		}
		if !grew || rounds >= 12 {
			break
		}
	}
	// Report the set that was actually used, in a stable order.
	keys := make([]Addr, 0, len(found))
	for a := range found {
		keys = append(keys, Addr(a))
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	res.Dispatchers = keys
	res.Shadows = map[int]int{}
	for a, p := range shadows {
		res.Shadows[int(a)] = p
	}
	return res, rounds
}
