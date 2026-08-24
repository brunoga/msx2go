package dis

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The decoder in this package replaces the Python one in tools/z80trace.py,
// and "replaces" has to mean "agrees with", instruction for instruction, or
// the two halves of the pipeline disagree about where code begins and the
// generated Go panics the first time the cartridge takes a path the emitter
// wrote down differently.
//
// tools/dumpdecode.py writes one line per address for a whole image. The
// King's Valley dump is checked in whole, because it is small and it is the
// case everything else is measured against; the two megaROMs are checked by
// hash, because four hundred thousand lines of golden file buys nothing that
// a hash does not. When a hash fails, run
//
//	tools/dumpdecode.py salamander.rom > /tmp/py.decode
//	go test ./internal/dis -run Decode -dump /tmp/go.decode
//
// and diff them.

var dumpTo = flag.String("dump", "",
	"write this package's decode dump here, for diffing against the Python one")

func TestDecodeMatchesThePythonDecoder(t *testing.T) {
	cases := []struct {
		rom    string
		bank   int
		golden string
		sha1   string
	}{
		{"kingsvalley.rom", 0, "testdata/kingsvalley.decode",
			"5d27e8cc20afefbde9844c9daf56015a84910c86"},
		{"salamander.rom", 0x2000, "",
			"8bfa9a8a3ac12637d8fe3e4107e4ccefc2a189e6"},
		{"spacemanbow.rom", 0x2000, "",
			"f6f44abf3bdfb6940d179f09c6201d1b3332aa70"},
	}
	for _, c := range cases {
		t.Run(c.rom, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("../../..", c.rom))
			if err != nil {
				t.Skipf("%v", err)
			}
			got := dump(data, 0x4000, c.bank)
			if *dumpTo != "" {
				os.WriteFile(*dumpTo, []byte(got), 0o644)
			}
			sum := sha1.Sum([]byte(got))
			if h := hex.EncodeToString(sum[:]); h != c.sha1 {
				t.Errorf("decode dump SHA-1 is %s, want %s", h, c.sha1)
			}
			if c.golden == "" {
				return
			}
			want, err := os.ReadFile(c.golden)
			if err != nil {
				t.Fatal(err)
			}
			compare(t, string(want), got)
		})
	}
}

// dump renders every decode in the image the way dumpdecode.py does: one
// bank at a time, each mapped at the same base, so that every address stays
// inside sixteen bits however big the image is.
func dump(data []byte, base uint16, bank int) string {
	var b strings.Builder
	w := bufio.NewWriter(&b)
	step := bank
	if step == 0 {
		step = len(data)
	}
	for off := 0; off < len(data); off += step {
		end := off + step
		if end > len(data) {
			end = len(data)
		}
		r := Rom{Data: data[off:end], Base: base}
		dumpBank(w, r, off/step)
	}
	w.Flush()
	return b.String()
}

func dumpBank(w *bufio.Writer, r Rom, bank int) {
	for i := 0; i < len(r.Data); i++ {
		a := r.Base + uint16(i)
		ins, ok := Decode(r, a)
		if !ok {
			fmt.Fprintf(w, "%d %04x -\n", bank, a)
			continue
		}
		target := "."
		if hasTarget(ins.Kind) {
			target = fmt.Sprintf("%04x", ins.Target)
		}
		refs := "."
		if len(ins.Refs) > 0 {
			parts := make([]string, len(ins.Refs))
			for j, r := range ins.Refs {
				parts[j] = fmt.Sprintf("%04x", r)
			}
			refs = strings.Join(parts, ",")
		}
		cond := "."
		if ins.Cond != None {
			cond = ins.Cond.Name()
		}
		fmt.Fprintf(w, "%d %04x %d %s %s %s %s\n",
			bank, a, ins.Len, kindName(ins.Kind), cond, target, refs)
	}
}

// hasTarget mirrors the Python decoder, which leaves target unset except on
// the kinds that carry one.
func hasTarget(k Kind) bool {
	switch k {
	case Jp, Jr, Call, Djnz, Rst:
		return true
	}
	return false
}

func kindName(k Kind) string {
	return [...]string{"normal", "jp", "jr", "call", "djnz", "ret", "reti",
		"rst", "ijp", "halt"}[k]
}

// compare reports the first few differing lines, which is what makes a
// failure a lead rather than a verdict.
func compare(t *testing.T, want, got string) {
	t.Helper()
	wl := strings.Split(strings.TrimRight(want, "\n"), "\n")
	gl := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(wl) != len(gl) {
		t.Errorf("dump has %d lines, want %d", len(gl), len(wl))
	}
	n := 0
	for i := 0; i < len(wl) && i < len(gl); i++ {
		if wl[i] == gl[i] {
			continue
		}
		if n++; n <= 10 {
			t.Errorf("line %d:\n  python: %s\n  go:     %s",
				i+1, wl[i], gl[i])
		}
	}
	if n > 10 {
		t.Errorf("... and %d more differing lines", n-10)
	}
}
