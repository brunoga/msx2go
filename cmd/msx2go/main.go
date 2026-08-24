// Command msx2go turns an MSX cartridge ROM into Go source: a static
// recompilation of its code, the data it reads, and a machine to run it
// against.
//
// See PLAN.md beside this tree for what it does, why, and what is not built
// yet. Today it reports what it can work out about an image; the tracer and
// the emitter arrive in milestones M2 and M3.
package main

import (
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/brunoga/msx2go/internal/dis"
	"github.com/brunoga/msx2go/internal/emit"
	z80 "github.com/brunoga/msx2go/internal/emit/runtime"
	"github.com/brunoga/msx2go/internal/trace"
)

func main() {
	rom := flag.String("rom", "", "cartridge image to translate")
	runBas := flag.String("run", "", "the BASIC program a floppy starts "+
		"with, for a disk that has no AUTOEXEC.BAS")
	dsk := flag.String("dsk", "", "disk image to convert instead of a cartridge")
	interpretOnly := flag.Bool("interpret", false, "emit a module that "+
		"interprets rather than translates: same machine, same "+
		"harness, no static translation at all. It runs hundreds of "+
		"times faster than the hardware either way, so this costs "+
		"nothing you can hear or see, and it is the build to reach for "+
		"when a translation is suspected of diverging")
	out := flag.String("out", "", "directory to write the Go module into")
	name := flag.String("name", "", "short name for the cartridge "+
		"(default: the image's file name without its extension)")
	mapper := flag.String("mapper", "", "mapper to use; empty means detect")
	machine := flag.String("machine", "", "msx1, msx2 or msx2plus; "+
		"empty means guess from the image's size and mapper")
	base := flag.Int("base", 0x4000, "where the cartridge is mapped")
	config := flag.String("config", "", "optional: entry points nothing "+
		"reaches, ranges to refuse to decode, and the other things no "+
		"amount of looking at the image will reveal. Most cartridges "+
		"need none of it")
	modpath := flag.String("module", "", "module path for the generated "+
		"go.mod (default: example.com/<name>)")
	whole := flag.Bool("whole", false, "keep the whole image rather than "+
		"pruning it to the bytes no translated instruction covers")
	explain := flag.String("explain", "", "say how the trace reached an "+
		"address, and keep asking of whatever reached it")
	minrun := flag.Int("minrun", 0, "shortest run of translated bytes worth "+
		"pruning; 1 prunes every one of them")
	sitesFile := flag.String("sites", "", "places a running cartridge was "+
		"seen to go that the trace had not (default <out>/sites.txt)")
	discoverN := flag.Int("discover", 0, "generate, build and run in a loop, "+
		"feeding back every address the cartridge reaches that the "+
		"trace missed, for at most this many rounds")
	frames := flag.Int("frames", 3000, "how many frames a discovery round runs")
	exploreN := flag.Int("explore", 0, "fork the booted machine at every "+
		"conditional branch and run both arms, for at most this many "+
		"instructions in total: forced coverage of code no played run "+
		"reaches, fed into sites.txt beside the sweep's observations. "+
		"0 is off")
	sweepN := flag.Int("sweep", 20000, "interpret the cartridge for this many "+
		"frames first and translate what it was seen to execute; 0 to "+
		"translate only what static analysis can prove")
	monkeys := flag.Int("monkeys", 4, "how many played runs the sweep does "+
		"beyond watching the demo, each with a different seed")
	quota := flag.Int("quota", 400000, "instructions a sweep gives one frame "+
		"before moving on; there is no cycle counting here")
	tape := flag.String("tape", "", "a recorded tape to drive discovery with, "+
		"which reaches far more of a game than an attract screen does")
	flag.Parse()

	if *rom == "" && *dsk == "" {
		fmt.Fprintln(os.Stderr, "msx2go: -rom or -dsk is required")
		flag.Usage()
		os.Exit(2)
	}
	if *dsk != "" {
		if err := disk(*dsk, *name, *out, *modpath, *machine, *runBas,
			*exploreN); err != nil {
			die(err)
		}
		return
	}
	data, err := os.ReadFile(*rom)
	if err != nil {
		die(err)
	}
	if *name == "" {
		*name = strings.TrimSuffix(filepath.Base(*rom),
			filepath.Ext(*rom))
	}

	info, err := describe(data, *name, *mapper, *machine, *base)
	if err != nil {
		die(err)
	}
	// Which shape of cartridge this is: an INIT that settles into an idle
	// loop and works from its interrupt handler, or one whose game loop is
	// INIT itself. Booting the image and watching is the only way to know,
	// and the answer changes how the module is generated -- a main-thread
	// game keeps its whole image, because its main thread runs interpreted
	// and an interpreter executes from memory.
	{
		probe := z80.New(data, info.Mapper)
		probe.InterpretRun(uint16(*base), 1, 400000, nil)
		info.MainThread = probe.MainThread
		if info.MainThread {
			fmt.Printf("  shape    the game loop is INIT itself; its " +
				"main thread will run interpreted and the image " +
				"stays whole\n")
		}
	}
	report(os.Stdout, data, info, *base)

	if *explain != "" {
		if err := explainAddr(data, info, *config, *explain); err != nil {
			die(err)
		}
		return
	}
	if *out == "" {
		return
	}
	if *discoverN > 0 {
		if err := discover(discoverOpts{
			rom: data, info: info, out: *out, config: *config,
			modpath: *modpath, sites: *sitesFile, whole: *whole,
			minrun: *minrun, rounds: *discoverN, frames: *frames,
			tape: *tape,
		}); err != nil {
			die(err)
		}
		return
	}
	if *interpretOnly {
		if err := interpreted(data, info, *out, *modpath, uint16(*base)); err != nil {
			die(err)
		}
		return
	}
	// Watch it run before translating it. A static trace cannot follow an
	// indirect jump and a running cartridge does not have to: what it
	// executes is code, and that is the whole of the argument.
	var executed map[int]bool
	if *sweepN > 0 {
		if _, err := sweep(sweepOpts{
			rom: data, info: info, base: uint16(*base),
			sites:  sitesPath(*sitesFile, *out),
			frames: *sweepN, quota: *quota, tape: *tape,
			monkeys: *monkeys,
		}); err != nil {
			die(err)
		}
	}
	if *exploreN > 0 {
		if err := exploreImage(data, info, uint16(*base), *out,
			*exploreN, *quota); err != nil {
			die(err)
		}
	}
	sites, err := trace.ReadSites(sitesPath(*sitesFile, *out))
	if err != nil {
		die(err)
	}
	// What was watched executing is what pruning may believe; the
	// explored candidates only seed the tracer, below.
	observed := sites
	if explored, err := trace.ReadSites(exploredPath(*out)); err == nil {
		sites = append(append([]trace.State{}, sites...), explored...)
	}
	// What was watched executing, as image offsets: the fact pruning is
	// entitled to believe. See emit.Module.blocks.
	//
	// This applies to any sites file, not just one a sweep in this run
	// produced. Every line in one is an address something was seen to
	// execute -- that is what the file is -- so believing it can only
	// keep too much, never too little. Deriving it only when -sweep ran
	// meant `-sweep 0 -sites <the sweep's own answer>` silently fell back
	// to the tracer's guess and pruned away data the cartridge needs.
	if len(observed) > 0 {
		executed = executedOffsets(observed, info, len(data))
	}
	if err := generate(data, info, *out, *config, *modpath, *whole,
		*minrun, sites, executed); err != nil {
		die(err)
	}
}

// interpreted writes a module with no translated code in it.
//
// Everything else is the same: the same machine, the same harness, the same
// window, sound and snapshots, and the image whole so the interpreter can
// execute from it. What it buys is that the module and msx2go's own
// interpreter are then the same program, so a cartridge whose translation is
// suspected of diverging can be played and compared without one.
func interpreted(rom []byte, info z80.Info, out, modpath string, base uint16) error {
	if out == "" {
		return nil
	}
	if modpath == "" {
		modpath = "example.com/" + info.Name
	}
	rep, err := emit.Module{
		Dir: out, Info: info, ModPath: modpath,
		ROM: rom, Base: base, Whole: true,
	}.Write()
	if err != nil {
		return err
	}
	fmt.Printf("  translate none: every instruction interprets\n")
	fmt.Printf("  data     %d bytes in %d blocks (the whole image)\n",
		rep.DataBytes, rep.Blocks)
	fmt.Printf("  wrote    %s: rom_meta.go, data_gen.go, %d runtime files, "+
		"and two harnesses\n", out, rep.RuntimeFiles)
	fmt.Printf(`
    cd %[1]s
    go mod tidy
    go build -tags msxdata ./cmd/%[2]s-play
`, out, info.Name)
	return nil
}

// disk converts a floppy rather than a cartridge.
//
// A disk program's code arrives in RAM, put there by the BASIC loader the
// disk boots through, so there is no image at a fixed address to trace and
// nothing yet to translate: the module this writes embeds the floppy and
// interprets what runs off it. Everything else about it -- the harness, the
// window, the sound, the snapshots -- is the same module the cartridges get.
//
// What the disk *is* comes out of the image, so any disk can be handed over:
// the geometry from its BIOS parameter block, the boot program from its
// directory, and the shape of the program from booting it and watching.
func disk(path, name, out, modpath, machine, runBas string, exploreBudget int) error {
	img, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	d, err := z80.NewDisk(img)
	if err != nil {
		return err
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	guessMachine := machine == ""
	if guessMachine {
		machine = "msx1"
	}
	sum := sha1.Sum(img)
	fmt.Printf("%s\n", name)
	fmt.Printf("  image    %d bytes (%d KB), SHA-1 %s\n",
		len(img), len(img)/1024, hex.EncodeToString(sum[:]))
	fmt.Printf("  medium   floppy, %d files\n", len(d.Files()))
	for _, f := range d.Files() {
		fmt.Printf("    %-14s %7d\n", f.Name, f.Size)
	}

	info := z80.Info{
		Name: name, Machine: machine, Mapper: z80.Mapper{},
		Size: len(img), Fill: 0x00, Floppy: true, Run: runBas,
	}
	// The same question a cartridge is asked: does the program settle into
	// an idle loop and work from its interrupt handler, or is the loop the
	// program itself? Booting it and watching is the only way to know.
	var snap []byte
	var starts []uint16
	{
		probe := z80.New(nil, z80.Mapper{})
		probe.Disk = d
		probe.DiskRun = runBas
		if err := probe.BootDisk(d, runBas); err != nil {
			return fmt.Errorf("booting %s: %w", path, err)
		}
		info.MainThread = probe.MainThread
		fmt.Printf("  shape    %s\n", shapeOf(info.MainThread))
		// The machine, decided the same way: a program that woke the
		// V9938 during its boot is an MSX2 program, whatever the
		// image's size says.
		if guessMachine && probe.VDP.V9938 {
			info.Machine = "msx2"
			fmt.Printf("  machine  msx2: the boot programmed the V9938\n")
		}
		// The code a disk runs is what its loader just built in RAM,
		// and this moment -- the loader finished, the game barely
		// started -- is the one the runtime can reproduce exactly. A
		// snapshot of that region is the translation's source, its
		// hash the proof at run time that the floppy still makes the
		// same program. Tracing starts from the interrupt hooks; the
		// main thread stays interpreted, and sites.txt feeds back
		// everything a run discovers. See -discover for cartridges.
		if lo, hi, ok := probe.LoadedRange(); ok {
			snap = append([]byte(nil), probe.Mem[lo:int(hi)+1]...)
			info.TransBase = lo
			info.TransSize = len(snap)
			sum := sha1.Sum(snap)
			info.TransSHA1 = hex.EncodeToString(sum[:])
			var entries []trace.Entry
			for _, h := range probe.InterruptEntries() {
				if h >= lo && h <= hi {
					entries = append(entries, trace.Entry{
						Addr: trace.Addr(h), Reason: "interrupt hook"})
				}
			}
			if exploreBudget > 0 && out != "" {
				seen := map[string]bool{}
				for _, f := range []string{sitesPath("", out),
					exploredPath(out)} {
					if old, err := trace.ReadSites(f); err == nil {
						for _, st := range old {
							seen[siteLine(st.Addr, st.Banks)] = true
						}
					}
				}
				before := len(seen)
				freshDisk := func() *z80.M {
					mm := z80.New(nil, z80.Mapper{})
					mm.Disk = d
					return mm
				}
				explore(probe, freshDisk, exploreBudget, seen)
				if err := writeSites(exploredPath(out), seen,
					"# Written by msx2go -explore: candidates, "+
						"not observations.\n"); err != nil {
					return err
				}
				fmt.Printf("  explore  %d instruction budget: "+
					"%d address(es), %d new\n",
					exploreBudget, len(seen), len(seen)-before)
			}
			sites, err := trace.ReadSites(sitesPath("", out))
			if err != nil {
				return err
			}
			if explored, err := trace.ReadSites(exploredPath(out)); err == nil {
				sites = append(sites, explored...)
			}
			kept := sites[:0]
			for _, st := range sites {
				if st.Addr >= lo && st.Addr <= hi {
					kept = append(kept, st)
				}
			}
			cfg := trace.Config{Base: trace.Addr(lo)}
			res, rounds := trace.Trace(snap,
				z80.Flat(int(lo), len(snap)), cfg, entries, kept)
			starts = res.Starts
			fmt.Printf("  code     %04X-%04X, hashed; trace found %d "+
				"instructions in %d round(s) from %d hook(s) and %d site(s)\n",
				lo, hi, len(res.Insns), rounds, len(entries), len(kept))
		}
	}
	if out == "" {
		return nil
	}
	if modpath == "" {
		modpath = "example.com/" + name
	}
	rep, err := emit.Module{
		Dir: out, Info: info, ModPath: modpath,
		ROM: img, Base: 0x4000, Whole: true,
		Starts: starts, TransROM: snap, TransBase: info.TransBase,
	}.Write()
	if err != nil {
		return err
	}
	fmt.Printf("  data     %d bytes in %d blocks (the whole floppy)\n",
		rep.DataBytes, rep.Blocks)
	fmt.Printf("  wrote    %s: rom_meta.go, data_gen.go, %d runtime files, "+
		"and two harnesses\n", out, rep.RuntimeFiles)
	fmt.Printf(`
    cd %[1]s
    go build -tags msxdata ./cmd/%[2]s        # headless: frames, digests, PNGs
    go mod tidy
    go build -tags msxdata ./cmd/%[2]s-play   # a window, keys and sound

The floppy is written back to %[2]s.dsk beside the save state when the
program changes it, which is what a disk with a level editor on it does.
`, out, name)
	return nil
}

func shapeOf(mainThread bool) string {
	if mainThread {
		return "the game loop is the program itself; it runs interpreted"
	}
	return "the program settles into an idle loop and works from its handler"
}

// describe works out what the image is, from what was asked for and what can
// be told from the bytes.
func describe(data []byte, name, mapper, machine string, base int) (z80.Info, error) {
	if mapper == "" {
		mapper = dis.DetectMapper(data)
	}
	mp, err := z80.NamedMapper(mapper, base, len(data))
	if err != nil {
		return z80.Info{}, err
	}
	if mp.BankSize > 0 && len(data)%mp.BankSize != 0 {
		return z80.Info{}, fmt.Errorf(
			"msx2go: %s wants banks of %d bytes and the image is %d, "+
				"which is not a whole number of them",
			mp.Name, mp.BankSize, len(data))
	}
	guessMachine := machine == ""
	if guessMachine {
		machine = "msx1"
	}
	// SHA1 is left empty: it belongs to the packed data blocks and is
	// filled in when there are any, which is the emitter's business.
	return z80.Info{
		Name: name, Machine: machine, Mapper: mp,
		Size: len(data), Fill: 0xFF,
	}, nil
}

// report says what msx2go makes of an image, which is the whole of what it
// can do so far and is worth being able to ask on its own.
func report(w *os.File, data []byte, info z80.Info, base int) {
	fmt.Fprintf(w, "%s\n", info.Name)
	sum := sha1.Sum(data)
	fmt.Fprintf(w, "  image    %d bytes (%d KB), SHA-1 %s\n",
		info.Size, info.Size/1024, hex.EncodeToString(sum[:]))
	fmt.Fprintf(w, "  machine  %s\n", info.Machine)
	fmt.Fprintf(w, "  mapper   %s", info.Mapper.Name)
	if n := info.Mapper.BankCount(info.Size); info.Mapper.Name != "none" {
		fmt.Fprintf(w, ", %d banks of %d", n, info.Mapper.BankSize)
	}
	fmt.Fprintln(w)
	for _, r := range dis.RankMappers(data) {
		fmt.Fprintf(w, "    %-12s score %3d\n", r.Name, r.Score)
	}

	m := z80.New(data, info.Mapper)
	h := m.ReadHeader(uint16(base))
	fmt.Fprintf(w, "  header   ")
	if !h.Valid {
		fmt.Fprintf(w, "no AB signature at %04Xh\n", base)
		return
	}
	fmt.Fprintf(w, "AB, INIT %04Xh", h.Init)
	for _, e := range []struct {
		name string
		addr uint16
	}{{"STATEMENT", h.Statement}, {"DEVICE", h.Device}, {"TEXT", h.Text}} {
		if e.addr != 0 {
			fmt.Fprintf(w, ", %s %04Xh", e.name, e.addr)
		}
	}
	fmt.Fprintln(w)
}

// explainAddr says how the trace reached an address, and then how it reached
// whatever reached that, and so on.
//
// A trace that walks into data is not obvious from its output -- it just has
// more instructions than it should -- and the chain of reasons is the only
// thing that shows where it went wrong. Text disassembles perfectly well.
func explainAddr(rom []byte, info z80.Info, config, want string) error {
	cfg, err := trace.LoadConfig(config)
	if err != nil {
		return err
	}
	if cfg.Base == 0 {
		cfg.Base = trace.Addr(0x4000)
	}
	entries := append(trace.HeaderEntries(rom), cfg.Entries...)
	res, _ := trace.Trace(rom, info.Mapper, cfg, entries, nil)

	var addr uint32
	if _, err := fmt.Sscanf(want, "%x", &addr); err != nil {
		return fmt.Errorf("%q is not an address", want)
	}
	at := uint16(addr)
	for i := 0; i < 24; i++ {
		reason, ok := res.EntryReason[at]
		if !ok {
			fmt.Printf("  %04X   not an entry point the trace recorded; "+
				"it was reached by falling through\n", at)
			return nil
		}
		fmt.Printf("  %04X   %s\n", at, reason)
		var from uint32
		if _, err := fmt.Sscanf(reason, "%*s from %x", &from); err != nil {
			if _, err := fmt.Sscanf(reason, "%*s %*s table at %x",
				&from); err != nil {
				return nil
			}
		}
		if uint16(from) == at {
			return nil
		}
		at = uint16(from)
	}
	return nil
}

// generate traces the cartridge and writes the module.
// sitesPath is where the discovered sites live: beside the generated module
// unless somewhere else was asked for.
func sitesPath(given, out string) string {
	if given != "" {
		return given
	}
	if out == "" {
		return ""
	}
	return filepath.Join(out, "sites.txt")
}

func generate(rom []byte, info z80.Info, out, config, modpath string,
	whole bool, minrun int, sites []trace.State,
	executed map[int]bool) error {
	cfg, err := trace.LoadConfig(config)
	if err != nil {
		return err
	}
	if cfg.Base == 0 {
		cfg.Base = trace.Addr(0x4000)
	}
	entries := trace.HeaderEntries(rom)
	if len(entries) == 0 {
		return fmt.Errorf("no AB signature: this does not look like a " +
			"cartridge, and there is nothing to start tracing from")
	}
	entries = append(entries, cfg.Entries...)

	res, rounds := trace.Trace(rom, info.Mapper, cfg, entries, sites)
	fmt.Printf("  trace    %d instructions, %d states, %d round(s)",
		len(res.Insns), res.States, rounds)
	if len(sites) > 0 {
		fmt.Printf(", from %d discovered site(s)", len(sites))
	}
	fmt.Println()
	if res.Overflowed {
		fmt.Println("  WARNING  the trace hit its state limit; " +
			"some code may be missing")
	}
	if len(res.Bad) > 0 {
		fmt.Printf("  WARNING  %d addresses would not decode\n", len(res.Bad))
	}
	// Every BIOS entry the cartridge calls, and whether the runtime has
	// one. Saying this now beats a panic three levels into a game.
	var missing []uint16
	seen := map[uint16]bool{}
	for target := range res.CallTargets {
		if target >= 0x4000 || seen[target] {
			continue
		}
		seen[target] = true
		if _, ok := z80.BIOSImplemented[target]; !ok {
			missing = append(missing, target)
		}
	}
	if len(seen) > 0 {
		names := make([]string, 0, len(seen))
		for a := range seen {
			if n, ok := z80.BIOSImplemented[a]; ok {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		fmt.Printf("  bios     %d entry point(s): %s\n",
			len(seen), strings.Join(names, " "))
	}
	if len(missing) > 0 {
		sort.Slice(missing, func(i, j int) bool {
			return missing[i] < missing[j]
		})
		fmt.Print("  WARNING  no shim for")
		for _, a := range missing {
			fmt.Printf(" %04Xh", a)
		}
		fmt.Println("; the cartridge will panic if it calls one.\n" +
			"           They are not part of the image and have to " +
			"be written into the runtime's bios.go.")
	}
	if len(res.Shadows) > 0 {
		fmt.Printf("  shadows  %d RAM byte(s) holding which bank is in "+
			"a page:", len(res.Shadows))
		keys := make([]int, 0, len(res.Shadows))
		for a := range res.Shadows {
			keys = append(keys, a)
		}
		sort.Ints(keys)
		for _, a := range keys {
			fmt.Printf(" %04Xh->page %d", a, res.Shadows[a])
		}
		fmt.Println()
	}
	if n := len(res.BankSwitches); n > 0 {
		seen := map[[2]int]int{}
		for _, sw := range res.BankSwitches {
			seen[[2]int{sw.Page, sw.Bank}]++
		}
		fmt.Printf("  banking  %d switch site(s), %d distinct "+
			"(page, bank) pairs\n", n, len(seen))
	}
	if n := len(res.UnresolvedSwitches); n > 0 {
		fmt.Printf("  WARNING  %d bank switch(es) the trace could not "+
			"evaluate; each one stopped a walk, so each is code "+
			"that may be missing\n", n)
	}
	if len(res.IndirectJumps) > 0 {
		fmt.Printf("  note     %d indirect jumps; any table they read that "+
			"was not found is code that will be missing\n",
			len(res.IndirectJumps))
	}
	// The two kinds of instruction a static trace cannot follow, written
	// where ref/resolve.sh can ask a real machine what they actually do.
	// Observation beats inference here: the target of `jp (hl)` is
	// whatever a table indexed by a game variable holds, and the set of
	// possible values is the whole table with no way to bound it.
	if out != "" {
		var b strings.Builder
		for _, a := range res.IndirectJumps {
			fmt.Fprintf(&b, "jp %d\n", a)
		}
		for _, u := range res.UnresolvedSwitches {
			fmt.Fprintf(&b, "sw %d %d\n", u.At, u.Page)
		}
		if b.Len() > 0 {
			p := filepath.Join(out, "unresolved.txt")
			os.MkdirAll(out, 0o755)
			os.WriteFile(p, []byte(b.String()), 0o644)
			fmt.Printf("  ask      %d unresolved site(s) written to %s; "+
				"ref/resolve.sh will ask a real machine what they do\n",
				len(res.IndirectJumps)+len(res.UnresolvedSwitches), p)
		}
	}
	if modpath == "" {
		modpath = "example.com/" + info.Name
	}
	rep, err := emit.Module{
		Dir: out, Info: info, ModPath: modpath,
		ROM: rom, Starts: res.Starts, Insns: res.Insns,
		Logical: res.Logical, SiteBanks: res.SiteBanks,
		Base: uint16(cfg.Base), Whole: whole, MinPruneRun: minrun,
		Executed: executed,
	}.Write()
	if err != nil {
		return err
	}
	for _, e := range rep.Unsupported {
		fmt.Printf("  WARNING  %v\n", e)
	}
	if n := len(rep.PointerLoads); n > 0 {
		fmt.Printf("  note     %d pointer(s) into translated code: ", n)
		for i, a := range rep.PointerLoads {
			if i == 8 {
				fmt.Printf("... and %d more", n-8)
				break
			}
			fmt.Printf("%04X ", a)
		}
		fmt.Println("\n           their width is not knowable, so those " +
			"bytes are pruned; build with -tags msxcheck to find out " +
			"whether the cartridge really reads through one")
	}
	pct := 100.0 * float64(rep.DataBytes) / float64(len(rom))
	fmt.Printf("  data     %d bytes in %d blocks (%.1f%% of the image; "+
		"the rest is code and is now Go)\n", rep.DataBytes, rep.Blocks, pct)
	fmt.Printf("  wrote    %s: rom_gen.go, rom_meta.go, data_gen.go, "+
		"%d runtime files, and two harnesses\n", out, rep.RuntimeFiles)
	fmt.Printf(`
    cd %[1]s
    go build -tags msxdata ./cmd/%[2]s        # headless: frames, digests, PNGs
    go mod tidy
    go build -tags msxdata ./cmd/%[2]s-play   # a window, keys and sound
    GOOS=js GOARCH=wasm go build -tags msxdata ./cmd/%[2]s-play

Without -tags msxdata either one looks for %[2]s.dat instead, which
./%[2]s -extract . will write for it. Add -tags msxcheck to find out
whether this cartridge reads a byte the pruning threw away.
`, out, info.Name)
	return nil
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "msx2go:", err)
	os.Exit(1)
}

// executedOffsets turns observed addresses into image offsets. The banks
// matter: two cartridge addresses share one Z80 address, and which one ran is
// the whole content of the observation.
func executedOffsets(sites []trace.State, info z80.Info, size int) map[int]bool {
	out := make(map[int]bool, len(sites))
	nb := info.Mapper.BankCount(size)
	for _, st := range sites {
		banks := st.Banks
		if len(banks) == 0 {
			banks = info.Mapper.Initial
		}
		if off := info.Mapper.Phys(banks, int(st.Addr), nb); off >= 0 &&
			off < size {
			out[off] = true
		}
	}
	return out
}
