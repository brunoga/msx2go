package emit

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/brunoga/msx2go/internal/dis"
)

// pythonStarts reads the instruction boundaries out of tools/z80trace.py's
// report.
func pythonStarts(path string) ([]uint16, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rep struct {
		Banks []struct {
			InsnStarts []uint16 `json:"insn_starts"`
		} `json:"banks"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil {
		return nil, err
	}
	var out []uint16
	for _, b := range rep.Banks {
		out = append(out, b.InsnStarts...)
	}
	return out, nil
}

// The check for the emitter is the strongest one available: the file it writes
// has to be the file that is already in the tree.
//
// kvgo/internal/z80/rom_gen.go was written by tools/z80togo.py and has been
// driven through fifty-three recorded tapes against the real cartridge,
// frame for frame, pixel for pixel. Reproducing it byte for byte is not a
// stylistic claim -- it is the whole of the evidence that this emitter
// translates the Z80 correctly, borrowed wholesale from the Python one.
//
// Only the header comment differs, because it credits a different generator.
func TestGeneratedFileMatchesTheOneInTheTree(t *testing.T) {
	rom, err := os.ReadFile("../../../kingsvalley.rom")
	if err != nil {
		t.Skipf("%v", err)
	}
	want, err := os.ReadFile("../../../kvgo/internal/z80/rom_gen.go")
	if err != nil {
		t.Skipf("%v", err)
	}
	// The instruction boundaries come from the Python trace, not from this
	// tree's tracer. That is deliberate: this test is about the emitter
	// and nothing else, and feeding it the same input the Python emitter
	// had is what makes byte-identical output mean "the translation is
	// the same" rather than "the trace happened to agree too". The tracer
	// has its own test.
	starts, err := pythonStarts("../../../build/trace.json")
	if err != nil {
		t.Skipf("%v -- run `make` in the parent tree first", err)
	}

	got, bad, err := Run{
		Package: "z80",
		Source:  "kingsvalley.rom",
		View:    dis.Rom{Data: rom, Base: 0x4000},
		Starts:  starts,
	}.Generate()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range bad {
		t.Errorf("no translation: %v", e)
	}

	// The header credits the generator, so it is expected to differ. The
	// rest is not -- with one deliberate exception: `ld a,r` now reads a
	// modelled refresh register where the Python emitter pinned it to
	// zero, because a constant R sent Salamander's demo down a degenerate
	// path no real machine takes. King's Valley reads R exactly once and
	// never branches on it, so the old battery stands either way.
	gotBody := bytes.ReplaceAll(afterPackage(got),
		[]byte("m.A = m.refreshR()"),
		[]byte("m.A = 0 // ld a,i / ld a,r - no meaningful value"))
	wantBody := afterPackage(want)
	if bytes.Equal(gotBody, wantBody) {
		return
	}
	gl := strings.Split(string(gotBody), "\n")
	wl := strings.Split(string(wantBody), "\n")
	if len(gl) != len(wl) {
		t.Errorf("generated %d lines, the tree's file has %d",
			len(gl), len(wl))
	}
	n := 0
	for i := 0; i < len(gl) && i < len(wl); i++ {
		if gl[i] == wl[i] {
			continue
		}
		if n++; n <= 10 {
			t.Errorf("line %d:\n  tree: %s\n  ours: %s",
				i+1, wl[i], gl[i])
		}
	}
	if n > 10 {
		t.Errorf("... and %d more differing lines", n-10)
	}
	if n == 0 && len(gl) == len(wl) {
		t.Error("files differ but no line does; whitespace at the end?")
	}
}

// afterPackage drops the header comment, which names the generator.
func afterPackage(b []byte) []byte {
	if i := bytes.Index(b, []byte("\npackage ")); i >= 0 {
		return b[i:]
	}
	return b
}
