# msx2go

A single Go binary that turns an MSX cartridge ROM into Go source: a static
recompilation of its code, the data that code reads pulled out beside it as
named Go byte slices, and a machine model to run it against. Compile the
output and you have a standalone program that behaves like the cartridge, with
no emulator loop and no ROM image -- the instructions are Go, and only the
bytes they *read* are still bytes.

This is the generalisation of `kvgo`, which is the same thing done once, by
hand-driven Python tools, for King's Valley.

    msx2go -rom game.rom -out ./gamego
    cd gamego && go build -tags msxdata ./cmd/game-play

## Why not an emulator

An emulator fetches, decodes and dispatches every instruction, every time.
A static recompilation decodes once, ahead of time, and emits straight-line Go
with a label per instruction. What you get back is not a faster emulator; it is
*source you can read and change*. That is the point: the output is the starting
material for turning a game into an engine, and `kvgo` -> `kingsvalley` is the
proof that the road goes somewhere.

Two things follow from that and shape everything below:

- **The Z80 stack is modelled explicitly**, not mapped onto Go call frames.
  Real cartridge code treats return addresses as data -- King's Valley's
  dispatcher at 404Bh pops its own return address to find the jump table that
  follows the call, and the handler's `ret` unwinds two levels. Go's stack
  cannot express that.
- **An address the tracer never proved reachable has no label.** Reaching one
  at runtime is a panic naming the address, which is a bug report rather than a
  silent wrong answer.

## What exists already

`tools/` in the parent directory has the whole pipeline in Python, driven by a
hand-written per-ROM JSON config:

| tool | lines | what it does | fate |
|---|---|---|---|
| `z80trace.py` | 1602 | decoder, mappers, recursive-descent trace with abstract interpretation | port the tracing half |
| `z80togo.py` | 600 | emits `rom_gen.go` | port whole |
| `mappers.py` | 194 | mapper shapes and detection | port whole |
| `machines.py` | 165 | BIOS/system symbol tables | port the parts the runtime needs |
| `emit.py`, `mkblocks.py`, `annotate.py`, `mksym.py`, `verify.py`, `analyze.py`, `unpack_gfx.py`, `peek.py`, `xref.py` | ~800 | the *listing* pipeline: `.asm` output and its round-trip proof | **stays in Python**, out of scope |

`kvgo/internal/z80/` has the runtime the generated code runs against:
`machine.go` (593 lines of Z80 semantics), `hardware.go` (TMS9918 VDP + AY-3-8910
PSG), `bios.go` (nine BIOS shims), `boot.go`, `input.go`. All of it is written
for one game on one machine; it becomes the seed of the generic runtime.

Three ROMs are already in the tree with configs, and the Python pipeline
already traces all three:

- `kingsvalley.rom` -- 16K flat, MSX1. The proof case.
- `salamander.rom` -- 128K Konami+SCC, MSX1.
- `spacemanbow.rom` -- 256K Konami+SCC, MSX2.

## Shape of the output

`msx2go -rom game.rom -out ./gamego` writes a complete, self-contained Go
module:

    gamego/
      go.mod
      z80/
        rom_gen.go       generated: a label per reachable instruction
        rom_meta.go      generated: name, machine, mapper, the data manifest
        data_gen.go      generated: the kept runs as named Go slices -- `msxdata`
        data_none.go     the empty stand-in -- build tag `!msxdata`
        data_check.go    the hole map and the checked read -- `msxcheck`
        machine.go       runtime: registers, flags, ALU, the explicit stack
        memory.go        runtime: address space + mapper paging
        data.go          runtime: find, load and verify the data blocks
        vdp.go           runtime: TMS9918 / V9938 / V9958
        psg.go           runtime: AY-3-8910
        scc.go           runtime: Konami SCC, when the mapper wants it
        bios.go          runtime: BIOS entry shims
        boot.go          runtime: reset, INIT, the interrupt
      cmd/game/main.go   a runnable harness: Ebiten v2 window, keys, sound

The runtime files are not templates. They live in `msx2go/internal/emit/runtime/`
as ordinary Go, compiled and tested as part of msx2go, and are emitted
verbatim with only the `package` clause rewritten. One source of truth, and it
is one that the compiler and the test suite already check.

## Fire and forget

The config file is an escape hatch, not an input. Nothing is required beyond
the image:

    msx2go -rom kingsvalley.rom -out ./kv

produces a cartridge that matches the hand-made one frame for frame over all
eight comparison runs, with the checked build reading no pruned byte. Given
`kv.json` as well it finds 23 more instructions and behaves identically,
because those 23 are five routines nothing in the cartridge ever calls. They
are in `kv.json` for the *listing* pipeline, which documents the whole ROM;
a port only needs the code the game reaches.

What is worked out rather than declared:

| | how |
|---|---|
| where the cartridge is mapped | the `AB` header |
| the entry points | INIT, STATEMENT, DEVICE and TEXT out of that header |
| the mapper | what the code writes to: every scheme's bank registers are at known addresses, and the whole ranking is printed to be argued with |
| the interrupt handler | following the write into H.KEYI or H.TIMI, which is the only way to reach a game's frame at all |
| inline dispatch tables | by shape: a `pop hl` reached from the call with no push outstanding, ending at `jp (hl)` |
| which bytes are data | everything no translated instruction covers |
| where a data block ends | the next translated byte |

What is left to declare, and only when a cartridge needs it: entry points
nothing reaches, ranges to refuse to decode, calls that never return. A
cartridge that needs none of those -- and King's Valley, which looks like it
needs five, does not -- is handed over with the image and nothing else.

## The data

The translated code is not the whole cartridge. Everything the code *reads* --
graphics, level layouts, tables, sound streams -- is still fetched out of the
image at runtime, so those bytes have to be somewhere.

The obvious answer is "embed the whole image", and it is the wrong one. Sixty
per cent of King's Valley is code, and that code has just been translated into
Go: shipping the image whole means shipping every instruction twice, once as
the Go that runs and once as bytes that never will. For a project whose point
is to end up with an engine rather than an emulator, a ROM blob sitting beside
the source is exactly the thing to get rid of.

So msx2go prunes, and it can be exact about it. **A byte covered by a
translated instruction is a byte the data cannot need**, because the tracer
proved it is executed and the translation now carries its meaning. Everything
else is kept. Measured on King's Valley:

    16,384 bytes total
     9,902 covered by 5,481 translated instructions   60.4%
     6,482 kept, in 42 contiguous runs                39.6%

and the runs that come out are, without anything being told to it, the regions
a person found by hand and wrote into `kv.json`:

    5D8B-6518  1933   level data and its pointer table
    520E-57FF  1521   the in-game tile and sprite streams
    7CE9-8000   791   note periods, stream pointers, the music itself
    4874-4B0C   664   the title screen
    467D-4823   422   the font and the text

That is the check that this is not guesswork: the automatic answer and the
hand-written one agree.

The kept runs are emitted as **named Go byte slices**, not an opaque blob --
`levelData`, `soundStreams`, or `data5D8B` where the config has no name for
them -- and loaded into their addresses at startup. Which is also the point
the engine work starts from: the parent tree's `kingsvalley` package reads
decoded assets, JSON and PNG, produced by extractors written by hand for this
one game. Turning `levelData` into `levels.json` will always be game-specific.
Getting to *a named block of bytes at a known address, byte-exact* need not be,
and that is what this step automates.

### Being wrong about it, cheaply

Pruning is a hypothesis: that nothing reads a byte the tracer proved to be an
instruction. On King's Valley it holds -- of the seven references into the
image the trace records, none lands on a translated byte -- but "holds here"
is not "holds always", and a cartridge that reads its own code (a checksum, a
table packed between routines) would break in a way that is nasty to find.

So the hypothesis is made falsifiable rather than argued about. Every pruned
run is recorded as a hole, and

    go build -tags msxcheck ./cmd/game

makes every read consult the hole map and panic naming the address and the run
it fell in. A pruning mistake reports itself the first time the game takes the
path that touches it -- and for King's Valley we can do better than wait for
it, because there are fifty-three recorded tapes: run the battery under the
checking build and the hypothesis is not likely, it is tested over every path
those tapes cover.

`msx2go -data whole` keeps the image entire, for a cartridge where the check
does fire and the reason is not worth chasing.

### Shipping it, or not

The kept data is still the cartridge's copyrighted content, so it stays
separable:

    go build -tags msxdata ./cmd/game   # one file, plays out of the box
    go build ./cmd/game                 # engine only; brings its own nothing

With `msxdata` the blocks are compiled in. Without it the binary looks for
`<name>.dat` -- the same blocks, same order -- in, in order: the path given by
`-data`, the user's data directory, beside the executable, and the working
directory. Not finding it is a message naming every place it looked. Either
way the blocks are checked against the SHA-1 recorded when the code was
generated, so a file from a different dump is refused by name rather than left
to produce a game that is subtly wrong three levels in.

And a build that has the data can give it back:

    game -extract ./data     # writes ./data/<name>.dat and stops

which is what makes the two builds interchangeable. On **wasm** there is no
filesystem to look in, so a web build is an `msxdata` build; the loader says
so rather than searching four places that cannot exist.

## Milestones

Each one ends in a check that can fail. Nothing is "done" on inspection.

### M0 -- scaffolding    **done**

Module `github.com/brunoga/msx2go`, added to `go.work`. `cmd/msx2go` parses
flags. `internal/emit/runtime` holds the kvgo runtime, moved across and made
machine-agnostic where that is free.

**Check:** `msx2go -rom kingsvalley.rom -out /tmp/kv` emits a module whose
runtime files compile.

### M1 -- decoder and mappers    **done**

Port `decode()` and the mapper table to Go. The decoder is ~100 lines of
Python and mechanical; the mappers are shapes (bank size, pages, switch
addresses) plus a detector.

**Check:** a differential test decodes every address of all three ROMs with
both implementations and compares length, kind, target and operands. Any
disagreement is a failure, recorded as a golden file so it stays fixed.

### M2a -- the tracer, flat images    **done**

Port the reachability half of `z80trace.py`: `Regs` (abstract register state
with provenance), `_step` (the abstract interpreter), and the work queue with
its inline tables, dispatcher detection, padding stops and installed-hook
following. The listing half does not come, and neither does the bank
machinery, which a single-bank cartridge never touches.

**Check:** the Go tracer's instruction-start set for `kingsvalley.rom` equals
the Python tracer's, address for address -- 5,481 instructions -- when it is
told to accept every pushed return the way the Python one does. With its own
rule it finds 5,450, and the 31 it refuses are the arrow pattern table at
7A79h and the ending text past it.

`ld rr,nn` followed by `push rr` looks like pushing a return address, and often
is. It is also what loading a pointer and saving it looks like: King's Valley's
arrow drawer begins `ld de,7A79h` and falls into a routine whose first
instruction is `push de`. What separates the two is whether the push begins a
routine -- a push something jumps to is a routine's own first instruction and
has nothing to do with the load before it -- and that is only knowable once the
trace has found every call and jump, so the candidates are decided afterwards
and the trace runs again if any are accepted.

Harmless while the whole image is carried, which is why it survived in the
Python tracer and in kvgo. Fatal once the data is pruned: those bytes are
translated, so they are dropped, so the arrows are drawn out of nothing. **The
precision of the trace is the thing pruning rests on**, and this was the first
place that showed.

Unaided -- given nothing but the cartridge's own header -- it finds 5,458 of
them in two rounds, including the dispatcher at 404Bh and the interrupt hook
the ROM installs at 401Ah, and nothing that is *not* in the Python trace. The
23 it does not find are five routines nothing in the cartridge reaches: an
alternate entry two bytes before a real one, a read counterpart nobody calls.
No tracer will find those, and declaring them is what a config is for.

### M2b -- the tracer, banked images

The rest of `z80trace.py`: bank shadows (RAM bytes holding "which bank is in
page N", discovered from value provenance), bank switches and the observed
per-page bank sets, interprocedural summaries for routines whose whole job is
to change the mapping, inter-bank call gates with their inline argument
blocks, and the fixed-point loop over all of it.

**Check:** the same equality, per bank, for Salamander and Space Manbow.
Needed by M5, not by M3.

### M3 -- the emitter    **done**

Port `z80togo.py`. For a single-bank ROM the output must be what is in the
tree today.

**Check:** `msx2go -rom kingsvalley.rom -config kv.json` produces a
`rom_gen.go` byte-identical to `kvgo/internal/z80/rom_gen.go` apart from the
header comment, which credits a different generator. It does. That file has
been driven through fifty-three recorded tapes against the real cartridge,
frame for frame and pixel for pixel, so reproducing it is not a stylistic
claim -- it is the whole of the evidence that the Z80 is translated correctly,
borrowed wholesale from the Python emitter.

One deliberate difference, off by default and on when generating: an
unconditional jump to itself compiles to `m.Idle(); return` rather than a Go
loop that never ends. That is the generic boot -- see M4.

### M4 -- standalone King's Valley    **done**

    msx2go -rom kingsvalley.rom -config kv.json -out ./kv
      trace    5450 instructions
      data     6532 bytes in 42 blocks (39.9% of the image)

    cd kv
    go build -tags msxdata ./cmd/kingsvalley        # headless
    go mod tidy
    go build -tags msxdata ./cmd/kingsvalley-play   # a window, keys and sound
    GOOS=js GOARCH=wasm go build -tags msxdata ./cmd/kingsvalley-play

The generated cartridge carries no ROM image -- 6.5K of data blocks and 30,000
lines of Go -- and matches the hand-made one **frame for frame**: same video
memory, same RAM, same sound registers, over 30,000 frames of attract mode and
every recorded power-on tape. `kvgo/cmd/kvdigest` and the headless harness
print the same three hashes a frame and the two outputs are identical, eight
runs out of eight. Their rendered frames are identical too, byte for byte,
which is the same title screen a person would see.

Under `-tags msxcheck`, over the same eight runs, nothing ever reads a pruned
byte. The pruning is not argued for; it is tested.

Two things it took to get there, and both were worth having:

- **An absolute load names its address and its opcode fixes its width.** King's
  Valley builds a two-byte routine in RAM by copying the `pop hl` opcode out of
  its own code at 404Ch, so that byte is code *and* data. `ld a,(nn)` reads one
  byte and `ld hl,(nn)` two, so those are kept exactly rather than guessed at.
- **The pushed-return rule** in M2a. A heuristic that kept short runs of code
  was tried first and worked, and was then taken out again: once the tracer
  stopped walking into data there was nothing left for it to catch, and a rule
  that guards against nothing observable is a fudge. It survives as `-minrun`
  for a cartridge where the checked build says otherwise.

Two harnesses rather than one, because they want different things. The headless
one has no dependencies at all, which is what makes it useful for comparing two
machines; the windowed one is Ebiten, so desktop and browser come from the same
source.

The video is Graphics 2 -- SCREEN 2, what an MSX1 game uses -- decoded from the
registers rather than assumed, since cartridges reprogram them. Text and
multicolour are not drawn. Sound is the AY-3-8910, handed the chip's registers
once a frame, which is how often the driver writes them.

### M5 -- banked ROMs, and Salamander    *partly done*

The design change is made and it works. A label is the **offset in the image**,
which names a bank and an address together, and the dispatch works that offset
out at run time from whatever the mapper has mapped. Two things follow:

- The bank is **not** carried on the Z80 stack. A `ret` pops sixteen bits and
  lands wherever the mapper has that page pointed right then -- which is what
  the hardware does, and what makes an inter-bank trampoline work without the
  translation knowing anything about it.
- A jump *within the same page* still gets a direct `goto`. It has to be the
  same bank: the code doing the jumping is in that page, so nothing can have
  changed the mapping between the jump and its target. Almost every branch is
  local, so the dispatch is paid only where the hardware pays too.

Bank shadows are found rather than declared. The stepper records where each
value was loaded from, so a bank register written from RAM makes that address a
candidate -- and a candidate is only believed once it has been seen from both
sides, read *into* a register for a page and written *alongside* a switch of
that same page. On Salamander it finds F0F1h, F0F2h and F0F3h for pages 1, 2
and 3, which is exactly the set `tools/z80trace.py` needed and got.

The Konami SCC is implemented: five channels of 32-sample wavetable that appear
at 9800h when bank 3Fh goes into page 2, mirrored into the address space the
way a bank is because the registers read back, and mixed over the PSG because
that is what two chips in one machine do.

### Finding the code

A static trace of a megaROM runs out of certainty long before it runs out of
program: a bank register written with a value it cannot follow, an indirect
jump through a table it did not find. Each stops a walk, and everything past it
goes untranslated.

The answer is to run the cartridge and write down where it goes, and the thing
that runs it is `internal/emit/runtime/interp.go` -- a fetch/decode/execute loop
over the same machine the translated code runs on. It needs nothing translated
to get started, so it has no coverage ceiling: every address it executes is
code, including everything behind the jumps a static trace cannot follow.

It dispatches onto the same ALU helpers the translated code calls, so the two
cannot compute a different answer -- and they do not. Interpreting King's Valley
and running the translated King's Valley give the same VRAM, work RAM and sound
registers at every checkpoint over 3,000 frames; the same holds for Salamander
over 20,000. That agreement is the licence for everything below.

It buys three things.

**Discovery.** `msx2go -rom x.rom -out dir` sweeps before it translates: one
run watching the demo, then four with a monkey on the controls, since attract
mode is only what a cartridge does when nobody is playing and it saturates.
Salamander: 12,690 addresses in 95 seconds, against 1,052 from an hour of the
old generate-build-crash loop.

**Pruning that is true.** A byte the cartridge executes cannot also be needed as
data. Which bytes those are used to be the tracer's guess, and on Salamander it
over-reached -- the parameter table at BB6F in bank 15 got decoded as
instructions, pruned, and the game read `FF FF` where a pointer should be. The
symptom was a frozen screen 2,000 frames later. A sweep cannot over-reach: what
executed, executed. So pruning believes the sweep, and an instruction the tracer
translated but never saw run is kept as data as well. Paying twice for those
bytes is the cheap side of the trade.

**No more crashes on a missing label.** `noLabel` hands the address to the
interpreter, which runs the rest of the way and unwinds out; the address is
written down, because translating it next time is free speed. `m.Fussy` restores
the panic, which is what you want while teaching msx2go a new cartridge.

The old loop is still in `discover.go` under `-discover`. It watches the
*translated* machine rather than the interpreter, so it is the thing that would
catch the two disagreeing. As a way of finding code it has been superseded.

Neither can find code the cartridge does not execute while it is watched. A
sweep that finds nothing new means "nothing more on this path", not "nothing
more".

## The old discovery loop

Static analysis of a megaROM runs out of certainty long before it runs out of
program. A bank register written with a value the tracer cannot follow, an
indirect jump through a table it did not find, a routine that leaves the
mapping changed on return -- each stops a walk, and everything past it goes
untranslated. Guessing at any of them risks decoding one bank's bytes as
another's, which is a silent wrong answer.

Running the cartridge settles it:

    msx2go -rom salamander.rom -out ./sal -discover 60 -frames 3000

generates, builds and runs, and every address the cartridge reaches that has no
label is written down *with the paging that was in force* -- which is exactly
the pair the tracer needs to carry on from. Feed it back, regenerate, run
again. It is ground truth rather than inference.

A discovery build does not stop at the first one. Reaching an untranslated
address records it and *returns* in the Z80 sense -- pop and dispatch -- so the
program keeps moving and one run finds everything on that path rather than the
first thing. What it is doing after that is not the cartridge's own behaviour
any more, and that is the trade: this build exists to map the program, not to
play it.

On Salamander it converges in five rounds, 13,959 instructions to 18,263, and
then the running cartridge finds nothing more.

**What it cannot do** is find code the cartridge does not execute while it is
being watched. `tools/z80trace.py` covers 43,974 bytes of Salamander; this
reaches 34,336. The difference is the game past its attract sequence, and the
answer to that is a recorded tape -- `-tape` drives the loop with one -- not a
better analysis.

### The reference machine

`ref/` runs the same cartridge under openMSX with C-BIOS -- extracted from
the Debian packages without root, driven headless over the stdio control
channel -- and digests the work RAM at every entry to the cartridge's own
interrupt handler. That rig settled, byte by byte, what no amount of reading
the listing had:

- **The real BIOS enables interrupts inside its own calls**, so the first
  frames of a game's ISR run in the middle of its own INIT, and games are
  built for exactly that: Salamander guards its slow path with e205, King's
  Valley with E005h. Boot now delivers nested interrupts at the
  interrupt-enabling shims, with the full register save the BIOS does around
  its hook, each delivery counting as a vertical blank. With that, the first
  post-init frame of Salamander matches the reference across the whole work
  RAM.
- **What remains is pace, not state.** The reference's slow path overruns
  real frames and drops state-machine ticks -- which is why the guard byte
  exists -- and a machine with no cycle time never drops any. That is the
  known ceiling of the approach made concrete, and the game's own guard is
  what makes it legal.

The screen check lives with it: `ref/verify.sh` takes the reference's VRAM
and registers at a chosen frame and a window of the translation's frames,
renders both through the same rasteriser, and passes if any frame in the
window shows the identical picture. The window absorbs the pace. King's
Valley passes it at the title, pixel for pixel, twelve frames of skew and
all.

That check supersedes the kvgo battery for the runtime: the fifty-three
tapes proved the generated King's Valley identical to kvgo, but kvgo's own
boot never delivered mid-INIT interrupts, so it proved self-consistency
rather than hardware fidelity. The emitter's byte-identical test still
stands -- it is about the translation, not the machine around it.

### M6 -- MSX2, and Space Manbow

V9938: the extra registers, 128K VRAM, the command engine (VDP copy/fill/line),
80-column and bitmap screen modes, the palette. This is the largest single
piece of new hardware in the project and it is not optional -- MSX2 games use
the blitter constantly.

**Check:** Space Manbow boots and plays. Same standard as M5.

### M7 -- MSX2+

V9958: YJK colour, horizontal scroll registers. Small next to M6.

## Where Salamander stands

`msx2go -rom salamander.rom -out /tmp/sal8 -sweep 40000 -monkeys 6` -- no
config, no arguments, four and a half minutes -- produces a module that:

- runs 24,000 frames with **no** fall-back to the interpreter,
- is byte-identical to the interpreter at every checkpoint,
- keeps 82.4% of the image as data and translates the rest,
- passes `msxcheck`, so nothing pruned is ever read,
- and whose **title screen is pixel-identical to openMSX with C-BIOS**
  (`ref/verify.sh salamander.rom /tmp/sal8 salamander 1600`).

It builds and plays, on a desktop and in a browser. The sweep's answer is kept
in `games/salamander/sites.txt` so the module can be rebuilt without running
one; it is not an input anyone has to write.

### What the screen check was worth

It found a bug nothing else could. The four VDP block routines were in the
wrong order in `bios.go`: FILVRM is 0056h and LDIRVM 005Ch, and this package
had them swapped, with CHGMOD where LDIRMV belongs. King's Valley calls none of
the four, so the whole King's Valley effort -- every digest, every pixel
comparison, a hundred tapes -- never touched them. Salamander asks 0056h to
clear all 16K of video RAM before drawing its title, got a copy from address
zero instead, and drew its title over whatever the previous screen had left
behind. Its 005Ch copy ran as a fill of 0Bh into the name table.

The lesson is about coverage rather than about the BIOS: a shim no cartridge in
the test set calls is a shim nobody has checked, and the way to find out is to
run a cartridge that calls it against a machine that is not ours.

### Open

The attract loop's second pass draws with the wrong graphics: "KONAMI PRESENTS"
gets the wrong glyphs and the title after it has game sprites where its kanji
belong. The first pass is right, and it is the pass that matches openMSX pixel
for pixel.

Three things are ruled out. It is not the translation -- interpreter and
translated build agree byte for byte over 20,000 frames. It is not the mapper
-- the bank-11 selector at 41BFh is reached, from 436Eh, at the very frame the
upload goes wrong. And it is not sprite collision or the fifth-sprite flag,
which this machine does not model but which this cartridge never reads: the
image holds one `in a,(99h)` and masks it with nothing but 80h.

So it is a state divergence upstream of the upload, and finding it wants a
per-frame comparison against openMSX rather than more screenshots. See
`games/salamander/README.md` for what has to be fixed in ref.tcl first.

### Watching the machine

Three hooks, added because that bug needed them and the next one will too:
`M.OnBank` (every bank-register write), `M.BiosTrace` (every BIOS entry, with
the registers the cartridge set -- which is how a call site says which routine
it believes it is calling), and `VDP.OnWrite`. `cmd/msxrun` exposes them as
`-bankwatch`, `-biostrace`, `-vwatch lo:hi` (attribute a VRAM range to the code
that wrote it) and `-vbyte` (one byte's whole history, including writes that
bypass the data port, which is how a failed clear shows itself).

### A caution about ref/verify.sh on a banked cartridge

`ref.tcl` counts an interrupt only when 4000h reads `AB`. Salamander pages
banks into page 1, so interrupts arriving under another bank go uncounted and
the reference's ordinals run behind real frames. Comparing "our frame N" to
"reference frame N" is therefore not sound for a mapper that switches page 1;
`verify.sh` gets away with it only because it compares a 121-frame window.

## Open questions, honestly

- **BIOS coverage.** kvgo shims nine entry points because King's Valley calls
  nine. A generic tool must either shim a much larger set or run a real BIOS
  image, which we do not ship and cannot. The plan is: shim by demand, have the
  tracer list every BIOS address the ROM reaches, and *fail generation loudly*
  with that list rather than emitting a program that panics on level three.
- **Self-modifying code** defeats static translation outright. The tracer can
  see writes into the ROM's own address space when a mapper makes RAM visible
  there; if it finds any, it must say so.
- **Timing.** There is no cycle counting. A game that depends on where the
  raster is -- split-screen effects, mid-frame palette changes -- will need the
  VDP status registers to be driven from a frame clock, and some will still be
  wrong. This is the known ceiling on the approach.
- **Interrupt granularity.** Everything here assumes the game does its work in
  one interrupt handler per frame, as King's Valley does. A game that runs a
  main loop and merely *syncs* on the interrupt needs `Run` to be re-enterable
  at an arbitrary point, which the self-jump trick does not cover. M4 will show
  whether that matters.

## Snapshots

`M.SaveState` / `M.LoadState` write the whole machine down and put it back.
`runtime/snapshot.go`; about 26 KB gzipped for a running game.

What makes it possible is that the translated code is not a coroutine. A frame
is one call to `Run`, and between frames there is no Go control flow to
preserve -- no program counter halfway down thirty thousand labels, no Go stack
to unwind. Everything that survives a frame boundary is a field of `M`, so
writing those fields down is writing down the machine.

The rule that follows: **a snapshot may only be taken between frames.** Save
refuses inside an interrupt handler, and there is no way to lift that short of
translating to a coroutine.

The address space is written whole rather than as "RAM plus which bank is
paged where". Two thirds of it is the cartridge and re-derivable, so this is
wasteful and deliberately so: paging is per-cartridge, and a snapshot that
reconstructed it would be a snapshot that could be wrong.

A snapshot carries the cartridge's SHA-1 and refuses to load into a different
game. It also carries `irqTaken`, the debt of a handler that overran its frame,
so a resumed machine has not forgotten that it was behind.

Verified by running each game straight through to frame 2400 and against a
snapshot taken at 1200 and resumed: byte-identical VRAM, RAM and sound
registers, on King's Valley (flat) and Salamander (konami-scc) alike.

`TestSnapshotCoversTheMachine` counts `M`'s fields and fails when one is added,
because the likeliest bug here is a new piece of state nobody wrote down -- and
it would show up as a game that resumes *almost* right.

In a generated game: F9 saves, F10 restores, `-state` picks the file. Headless:
`-savestate` and `-loadstate`.

## The harness's own keys

An MSX keyboard has F1 to F5 and no more, so F9 upward are free to mean
something to the harness without taking anything from the cartridge: F9 saves a
snapshot, F10 restores one, F11 fills the screen. `-fullscreen` starts that way.
Ebiten scales the layout and keeps its proportions, so a full screen letterboxes
the picture rather than stretching it.

## The border

There is no aspect ratio to correct. 256x192 is exactly 4:3 and MSX pixels are
square, so stretching the picture would make it *less* faithful, not more.

What a television showed that a bare framebuffer does not is the border: a
TMS9918 draws the 256x192 image inside a larger raster and paints the surround
in the colour register 7's low nibble names. It is not decoration -- games set
it deliberately and some flash it -- so the harness reads it every frame.
`-border` is on by default and the window is 320x240 accordingly.

The renderer still produces the 256x192 image alone, deliberately: every
comparison against the reference machine is of video memory rendered at that
size, and complicating it to carry a border would be a poor trade. The harness
draws the surround around what the renderer gives it.

King's Valley and Salamander both set register 7 to E0h, so their border is
black and the difference is framing rather than colour. Games that set a
coloured one are common enough to be worth having.
