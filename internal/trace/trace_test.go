package trace

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
)

// This is the check the whole tool rests on.
//
// The Python tracer in tools/z80trace.py found 5,481 instructions in King's
// Valley, and every one of them became a label in kvgo's rom_gen.go, and that
// file has been driven through fifty-three recorded tapes without once
// reaching an address it had no label for. So its instruction-start set is not
// merely a second opinion to agree with -- it is a set that has been *shown*
// to cover everything the game does. Reproducing it exactly is the strongest
// evidence available that this tracer is right.
//
// Anything this trace finds that the Python one did not is a byte about to be
// translated as code that has never been proved to be code. Anything it misses
// is an address the generated game will panic on.

// pyReport is the part of tools/z80trace.py's JSON that matters here.
type pyReport struct {
	Base   int    `json:"base"`
	Mapper string `json:"mapper"`
	Banks  []struct {
		InsnStarts []uint16 `json:"insn_starts"`
	} `json:"banks"`
}

func TestTraceFindsWhatThePythonTracerFound(t *testing.T) {
	rom, err := os.ReadFile("../../../kingsvalley.rom")
	if err != nil {
		t.Skipf("%v", err)
	}
	raw, err := os.ReadFile("../../../build/trace.json")
	if err != nil {
		t.Skipf("%v -- run `make` in the parent tree first", err)
	}
	var rep pyReport
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	want := map[uint16]bool{}
	for _, b := range rep.Banks {
		for _, a := range b.InsnStarts {
			want[a] = true
		}
	}
	if len(want) == 0 {
		t.Fatal("the Python report has no instruction starts")
	}

	mapper, err := z80.NamedMapper(rep.Mapper, rep.Base, len(rom))
	if err != nil {
		t.Fatal(err)
	}

	// Unaided: nothing but the cartridge's own header. What comes out has
	// to be a subset -- anything this finds that the Python trace did not
	// is a byte about to be translated as code that has never been proved
	// to be code -- and what it misses says what a config is *for*.
	t.Run("unaided", func(t *testing.T) {
		res, rounds := Trace(rom, mapper, Config{Base: Addr(rep.Base)},
			HeaderEntries(rom), nil)
		missing, extra := diff(want, res.Starts)
		t.Logf("%d instructions in %d round(s), %d states, "+
			"dispatchers %v", len(res.Starts), rounds, res.States,
			res.Dispatchers)
		t.Logf("%d addresses the Python trace has and this one does not; "+
			"they are the unreferenced routines kv.json declares",
			len(missing))
		report(t, "in this trace and not in the Python one", extra)
	})

	// Taking every pushed return at face value, as the Python tracer does:
	// an exact match, address for address. That is what pins the
	// difference down to one rule and nothing else -- it is not that this
	// tracer finds different code, it is that it refuses 31 bytes the
	// other one accepts.
	t.Run("with kv.json, accepting every pushed return", func(t *testing.T) {
		cfg, err := LoadConfig("../../../kv.json")
		if err != nil {
			t.Skipf("%v", err)
		}
		cfg.Base = Addr(rep.Base)
		cfg.AcceptAllPushedReturns = true
		entries := append(HeaderEntries(rom), cfg.Entries...)
		res, _ := Trace(rom, mapper, cfg, entries, nil)
		missing, extra := diff(want, res.Starts)
		report(t, "in the Python trace and not in this one", missing)
		report(t, "in this trace and not in the Python one", extra)
	})

	// And with the rule on, the difference is the arrow pattern table at
	// 7A79h and the ending text past it, which the Python tracer walks
	// into because `ld de,7A79h` is followed by a `push de` that belongs
	// to the routine after it. Harmless while the whole image is carried;
	// fatal once the data is pruned to what is not code, because then
	// those bytes are dropped and the arrows are drawn out of nothing.
	// Five routines
	// in King's Valley are reachable from nothing at all -- an alternate
	// entry two bytes before a real one, a read counterpart nobody calls
	// -- and no amount of tracing will find them. Declaring them is the
	// config's whole job, and with them declared the two tracers agree
	// completely.
	t.Run("with kv.json", func(t *testing.T) {
		cfg, err := LoadConfig("../../../kv.json")
		if err != nil {
			t.Skipf("%v", err)
		}
		cfg.Base = Addr(rep.Base)
		entries := append(HeaderEntries(rom), cfg.Entries...)
		res, rounds := Trace(rom, mapper, cfg, entries, nil)
		missing, extra := diff(want, res.Starts)
		t.Logf("%d instructions in %d round(s); %d fewer than the Python "+
			"tracer, all of them in the %d rejected pushed return(s) %v",
			len(res.Starts), rounds, len(missing),
			len(res.RejectedReturns), res.RejectedReturns)
		report(t, "in this trace and not in the Python one", extra)

		// And what is missing is exactly what the rule removed: trace
		// again accepting every pushed return and the two sets differ
		// by nothing else.
		loose := cfg
		loose.AcceptAllPushedReturns = true
		all, _ := Trace(rom, mapper, loose, entries, nil)
		strict := map[uint16]bool{}
		for _, a := range res.Starts {
			strict[a] = true
		}
		removed := map[uint16]bool{}
		for _, a := range all.Starts {
			if !strict[a] {
				removed[a] = true
			}
		}
		for _, a := range missing {
			if !removed[a] {
				t.Errorf("%04x is missing for some reason other "+
					"than the pushed-return rule", a)
			}
		}
	})
}

// diff is what each side has that the other does not.
func diff(want map[uint16]bool, starts []uint16) (missing, extra []uint16) {
	got := map[uint16]bool{}
	for _, a := range starts {
		got[a] = true
	}
	for a := range want {
		if !got[a] {
			missing = append(missing, a)
		}
	}
	for a := range got {
		if !want[a] {
			extra = append(extra, a)
		}
	}
	return uniq(missing), uniq(extra)
}

func report(t *testing.T, what string, v []uint16) {
	t.Helper()
	if len(v) == 0 {
		return
	}
	s := ""
	for i, a := range v {
		if i == 12 {
			s += fmt.Sprintf("... and %d more", len(v)-12)
			break
		}
		s += fmt.Sprintf("%04x ", a)
	}
	t.Errorf("%s: %d addresses: %s", what, len(v), s)
}
