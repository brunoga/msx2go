package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
	"github.com/brunoga/msx2go/internal/trace"
)

// Finding the code.
//
// Static analysis of a megaROM runs out of certainty long before it runs out
// of program. A bank register written with a value the tracer cannot follow,
// an indirect jump through a table it did not find, a routine that leaves the
// mapping changed on return -- each stops a walk, and everything past it goes
// untranslated. Guessing at any of them risks decoding one bank's bytes as
// another's, which is a silent wrong answer.
//
// Running the cartridge settles it, and there are two ways to run it.
//
// The first is to interpret it, which is what sweep does. The interpreter in
// the runtime executes the image directly, so it needs nothing translated to
// get started and has no coverage ceiling: every address it executes is code,
// by observation rather than inference, including everything behind the jumps
// a static trace cannot follow. A sweep of a hundred thousand frames takes two
// minutes and finds more than the loop below found in an hour. That it is
// entitled to be believed is not an assumption -- interpreting King's Valley
// and running the translated King's Valley produce the same video memory, the
// same work RAM and the same sound registers at every checkpoint over three
// thousand frames, and the same is true of Salamander.
//
// The second is the older loop: generate, build, run, feed back the address it
// died on, go round again. It is kept because it observes the *translated*
// machine rather than the interpreter, so it is the thing that would catch the
// two disagreeing. As a way of finding code it has been superseded.
//
// Neither can find code the cartridge does not execute while it is watched. A
// sweep that ends without finding anything new means "nothing more on this
// path", not "nothing more" -- which is why sweep can also put a monkey on the
// controls and why a recorded tape covers more of a game than an attract
// screen ever will.

var noLabelLine = regexp.MustCompile(
	`MSX2GO-NOLABEL ([0-9a-f]{4})(?: ([0-9,]+))?`)

// transferLine is a dynamic transfer the running cartridge made -- an
// indirect jump, a computed return -- while still on its own correct path.
var transferLine = regexp.MustCompile(
	`MSX2GO-TRANSFER ([0-9a-f]{4})(?: ([0-9,]+))?`)

// panicked reports a run that died some other way, which is worth surfacing
// rather than reading as convergence.
var panicLine = regexp.MustCompile(`^panic: `)

// sweepOpts is what a sweep needs.
type sweepOpts struct {
	rom     []byte
	info    z80.Info
	base    uint16
	sites   string
	frames  int
	quota   int
	tape    string
	monkeys int
}

// sweep interprets the cartridge and writes down every address it executes.
//
// The demo a cartridge plays to an empty arcade is only the code it runs when
// nobody is playing, and it saturates -- on Salamander after a few thousand
// frames. So the sweep is run once watching the demo and then once per monkey,
// each of which holds a direction for a third of a second at a time and leans
// on the fire button, which is what gets past a title screen and into the
// game. The runs are independent and their answers are unioned.
func sweep(o sweepOpts) (int, error) {
	seen := map[string]bool{}
	if old, err := trace.ReadSites(o.sites); err == nil {
		for _, st := range old {
			seen[siteLine(st.Addr, st.Banks)] = true
		}
	}
	before := len(seen)

	run := func(seed int64) error {
		m := z80.New(o.rom, o.info.Mapper)
		m.Observe = func(pc uint16, banks []int) {
			if pc < 0x4000 {
				return // the BIOS, which is shims here
			}
			seen[siteLine(pc, banks)] = true
		}
		keys, err := readTape(o.tape)
		if err != nil {
			return err
		}
		mk := newMonkey(seed)
		return m.InterpretRun(o.base, o.frames, o.quota, func(f int) {
			if f < len(keys) {
				m.SetInput(keys[f])
			} else if mk != nil {
				m.SetInput(mk.next(f))
			}
		})
	}
	// The demo first, then the monkeys. A run that ends badly is not fatal:
	// what it saw before it went wrong is still code.
	if err := run(0); err != nil {
		fmt.Printf("  sweep    watching the demo: %v\n", err)
	}
	for i := 1; i <= o.monkeys; i++ {
		if err := run(int64(i)); err != nil {
			fmt.Printf("  sweep    monkey %d: %v\n", i, err)
		}
	}

	lines := make([]string, 0, len(seen))
	for s := range seen {
		lines = append(lines, s)
	}
	sort.Strings(lines)
	var b strings.Builder
	b.WriteString("# Every address the cartridge was seen to execute, with " +
		"the banks in force.\n")
	b.WriteString("# Written by msx2go -sweep: observation, not inference.\n")
	for _, s := range lines {
		b.WriteString(s)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(o.sites), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(o.sites, []byte(b.String()), 0o644); err != nil {
		return 0, err
	}
	fmt.Printf("  sweep    %d frames x %d run(s): %d address(es) executed, "+
		"%d new -> %s\n", o.frames, o.monkeys+1, len(lines),
		len(lines)-before, o.sites)
	return len(lines), nil
}

// siteLine is one address and its paging in the form trace.ReadSites reads.
func siteLine(pc uint16, banks []int) string {
	if len(banks) <= 1 {
		return fmt.Sprintf("%04X", pc)
	}
	b := make([]string, 0, len(banks))
	for _, n := range banks {
		b = append(b, strconv.Itoa(n))
	}
	return fmt.Sprintf("%04X %s", pc, strings.Join(b, ","))
}

// A monkey at the controls: a direction held long enough to go somewhere, fire
// always, and start often, since on most cartridges the same button does both.
// Deterministic, because a seed is cheaper to write down than a tape.
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
	if f%20 == 0 {
		k.dir = z80.Buttons(1 << (k.rand() % 4))
	}
	b := k.dir | z80.TriggerA
	if f%180 < 4 {
		b |= z80.TriggerB
	}
	return b
}

// readTape reads a recording of one button byte per frame.
func readTape(path string) ([]z80.Buttons, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := make([]z80.Buttons, len(b))
	for i, v := range b {
		out[i] = z80.Buttons(v)
	}
	return out, nil
}

// discoverOpts is what a round of the loop needs.
type discoverOpts struct {
	rom     []byte
	info    z80.Info
	out     string
	config  string
	modpath string
	sites   string
	whole   bool
	minrun  int
	rounds  int
	frames  int
	tape    string
}

// discover runs the loop until the cartridge stops finding new ground.
func discover(o discoverOpts) error {
	if o.sites == "" {
		o.sites = filepath.Join(o.out, "sites.txt")
	}
	for round := 1; round <= o.rounds; round++ {
		sites, err := trace.ReadSites(o.sites)
		if err != nil {
			return err
		}
		fmt.Printf("\n== round %d: %d discovered site(s)\n", round, len(sites))
		if err := generate(o.rom, o.info, o.out, o.config, o.modpath,
			o.whole, o.minrun, sites, nil); err != nil {
			return err
		}
		bin, err := buildHarness(o.out, o.info.Name)
		if err != nil {
			return err
		}
		found, err := runHarness(bin, o.out, o.frames, o.tape)
		if err != nil {
			return err
		}
		if len(found) == 0 {
			fmt.Printf("\n  the cartridge ran %d frames without "+
				"reaching an address the trace had missed.\n",
				o.frames)
			return nil
		}
		inside := 0
		for _, st := range found {
			if o.info.Mapper.Name == "none" ||
				o.info.Mapper.PageOf(int(st.Addr)) >= 0 {
				inside++
			}
		}
		if inside == 0 {
			fmt.Printf("\n  the cartridge ran %d frames without "+
				"reaching an address in the image that the "+
				"trace had missed.\n", o.frames)
			return nil
		}
		// An address outside the cartridge is not something the trace
		// can be told about: it is the program returning somewhere that
		// does not exist, which in this build is usually the fault of
		// the fake returns this build makes. Report and move on.
		var outside []trace.State
		fresh := 0
		for _, st := range found {
			if o.info.Mapper.PageOf(int(st.Addr)) < 0 &&
				o.info.Mapper.Name != "none" {
				outside = append(outside, st)
				continue
			}
			added, err := trace.AddSite(o.sites, st)
			if err != nil {
				return err
			}
			if added {
				fresh++
			}
		}
		fmt.Printf("  %d address(es) reported, %d of them new to the "+
			"trace\n", len(found)-len(outside), fresh)
		if len(outside) > 0 {
			fmt.Printf("  %d of them were outside the cartridge "+
				"(%04X...), which is this build's own fake "+
				"returns unwinding somewhere real code would "+
				"not\n", len(outside), outside[0].Addr)
		}
		if fresh == 0 && len(found) > len(outside) {
			return fmt.Errorf(
				"msx2go: the cartridge still cannot reach %d "+
					"address(es) already in %s. The trace refuses "+
					"them -- they may be inside a range it takes "+
					"for data, or the paging recorded with them "+
					"may be wrong. First is %04X %v",
				len(found), o.sites, found[0].Addr, found[0].Banks)
		}
	}
	return fmt.Errorf("msx2go: still finding new ground after %d rounds; "+
		"raise -rounds or look at what it keeps finding", o.rounds)
}

// buildHarness compiles the headless program, which has no dependencies and so
// needs nothing fetched.
func buildHarness(dir, name string) (string, error) {
	bin := filepath.Join(dir, name+"-discover")
	// The discovery build writes down every address it cannot reach and
	// gives up on that frame, rather than stopping at the first one.
	cmd := exec.Command("go", "build", "-tags", "msxdata msxdiscover",
		"-o", bin, "./cmd/"+name)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("msx2go: the generated module does not "+
			"build:\n%s", out)
	}
	return bin, nil
}

// runHarness runs it and reports every address it could not reach.
// parseState reads an address and its paging out of a reported line.
func parseState(m []string) (trace.State, bool) {
	addr, err := strconv.ParseUint(m[1], 16, 16)
	if err != nil {
		return trace.State{}, false
	}
	st := trace.State{Addr: uint16(addr)}
	if m[2] != "" {
		for _, b := range strings.Split(m[2], ",") {
			n, _ := strconv.Atoi(b)
			st.Banks = append(st.Banks, n)
		}
	}
	return st, true
}

func runHarness(bin, dir string, frames int, tape string) ([]trace.State, error) {
	args := []string{"-frames", strconv.Itoa(frames), "-stoponmiss"}
	if tape != "" {
		abs, err := filepath.Abs(tape)
		if err != nil {
			return nil, err
		}
		args = append(args, "-tape", abs)
	}
	// A discovery run that has taken a wrong turn can spin forever inside
	// one frame -- a poll loop on RAM that will never change is not a
	// self-jump the emitter can compile away. The misses it found are
	// already printed, so killing it loses nothing.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	if ctx.Err() != nil {
		fmt.Println("  (the run was cut off after ten minutes; " +
			"using what it reported)")
	}

	var found, transfers []trace.State
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		if m := transferLine.FindStringSubmatch(sc.Text()); m != nil {
			if st, ok := parseState(m); ok {
				transfers = append(transfers, st)
			}
			continue
		}
		m := noLabelLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		addr, err := strconv.ParseUint(m[1], 16, 16)
		if err != nil {
			continue
		}
		st := trace.State{Addr: uint16(addr)}
		if m[2] != "" {
			for _, b := range strings.Split(m[2], ",") {
				n, _ := strconv.Atoi(b)
				st.Banks = append(st.Banks, n)
			}
		}
		found = append(found, st)
	}
	// The transfers come first: they are what correct execution proved,
	// and feeding them all back is what makes a round worth its compile.
	// The crash site goes on the end, and only the first of those.
	if len(found) > 1 {
		found = found[:1]
	}
	if len(transfers) > 0 || len(found) > 0 {
		return append(transfers, found...), nil
	}
	if len(found) > 0 {
		return found[:1], nil
	}
	// Nothing missed. If the run also failed some other way, that is not
	// convergence, and the output says why.
	for _, l := range strings.Split(string(out), "\n") {
		if panicLine.MatchString(l) {
			fmt.Fprintln(os.Stderr, "  the run itself failed:", l)
			break
		}
	}
	return nil, nil
}
