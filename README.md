# msx2go

msx2go turns an MSX game — a cartridge ROM or a floppy image — into a
standalone Go program. Not an emulator with a ROM inside it: the game's
machine code is translated to Go source ahead of time, its data sits beside
the code as named byte slices, and a machine model (VDP, PSG, SCC, BIOS,
disk) gives that code a world to run in. The output is a self-contained Go
module; build it and you have a native program that plays the game, with a
window, keys and sound — or headless, for scripting and verification.

    msx2go -rom salamander.rom -out ./salamander
    cd salamander
    go mod tidy
    go build -tags msxdata ./cmd/salamander-play
    ./salamander-play

Verified titles, checked frame-by-frame against openMSX running the real
machine ROMs: King's Valley, Salamander, Space Manbow (retail and the FRS
re-release), King's Valley II, King's Valley Plus (floppy), Breaker (floppy).

## Requirements

- Go 1.26 or later. Install with:

      go install github.com/brunoga/msx2go/cmd/msx2go@latest
      go install github.com/brunoga/msx2go/cmd/msxrun@latest

- For the windowed build: whatever [Ebitengine](https://ebiten.org) needs on
  your platform (on Linux: X11/GL development libraries; nothing at run
  time). The headless build needs nothing.
- A game image you have the right to use. msx2go ships no ROMs.

## Converting a cartridge

    msx2go -rom game.rom -out ./gamego

msx2go detects the mapper (flat, Konami, Konami+SCC, ASCII-8/16) and the
machine (MSX1/MSX2) from the image, traces every instruction reachable from
the cartridge's INIT, interprets the game for a while to see what else runs
(`-sweep`, default 20000 frames), and writes a Go module. Useful flags:

| flag | what it does |
|---|---|
| `-rom file` | the cartridge image |
| `-out dir` | where the module goes; without it, just report on the image |
| `-name n` | short name; commands are built as `cmd/<n>` and `cmd/<n>-play` |
| `-module path` | the go.mod module path (default `example.com/<name>`) |
| `-machine m` | `msx1`, `msx2` or `msx2plus`; default is detected |
| `-mapper m` | override mapper detection |
| `-base a` | where the cartridge maps (default 4000h) |
| `-sweep n` | frames to interpret before translating; 0 = static trace only |
| `-tape f` | a recorded input tape to drive the sweep deeper into the game |
| `-config f` | extra entry points and do-not-decode ranges, for the rare image that needs them |
| `-whole` | keep the whole image instead of pruning bytes the translation covers |
| `-interpret` | emit an interpreter-only module: same machine, same harness, no translation. The build to reach for when a translation is suspected of diverging |

## Converting a floppy

    msx2go -dsk game.dsk -run GAME.BAS -out ./gamego

A disk has no cartridge header. What it has is a BASIC loader, and msx2go
runs it: the tokenised program is interpreted — `BLOAD`, `LOAD`, `SCREEN`,
`SET PAGE`, `POKE`, the loader vocabulary — files land in RAM and VRAM
exactly as they would on the machine, and the program the loader ends by
starting is the game. `-run` names the BASIC program to begin with when the
disk has more than one and no `AUTOEXEC.BAS`.

The conversion boots the disk once and records what the boot revealed: the
program's shape (does it settle into an interrupt handler, or is the loop
the program itself), the machine (a boot that programs the V9938 is an MSX2
program), and the region of RAM the loader filled with code. That region is
snapshotted, traced from the game's interrupt hooks, and translated; its
SHA-1 goes into the module. At run time the same loader runs again, and the
moment it finishes the loaded bytes are hashed: a match turns the
translation on, a mismatch — an edited floppy — leaves the machine
interpreting, which is always correct and merely slower.

If the game writes its floppy (a high-score file, a level editor), the
changed image is written back beside the save state on exit.

## The output module

    gamego/
      go.mod
      cmd/<name>/         the headless harness: frames, digests, PNGs
      cmd/<name>-play/    the windowed harness: keys, sound, snapshots
      z80/
        rom_gen.go        generated: the translation, a label per instruction
        rom_meta.go       generated: name, machine, mapper, hashes
        data_gen.go       generated: the game's bytes  (build tag `msxdata`)
        *.go              the runtime: machine, VDP, PSG, SCC, BIOS, disk

Build with `-tags msxdata` and the game's data is compiled in; the binary is
the game. Build without the tag and the binary looks for `<name>.dat` beside
itself instead (`<name> -extract game.dat` writes it), so a binary can be
shared separately from the data it needs.

Everything the module imports is inside it. It builds with no network and no
reference back to msx2go.

## Playing

    go build -tags msxdata ./cmd/<name>-play
    ./<name>-play

Arrows and space are the joystick and trigger A; Z is also trigger A, shift
and X trigger B. The rest of the MSX keyboard is there too — letters,
digits, function keys, RETURN, ESC, TAB — for games that read keys by name.
Harness keys: **F9** saves a snapshot, **F10** restores it, **F11** toggles
fullscreen.

| flag | what it does |
|---|---|
| `-scale n` | window scale |
| `-fullscreen` | start fullscreen |
| `-border` | draw the hardware border (default on) |
| `-speed x` | run faster or slower than real time |
| `-cpu x` | processor speed as a multiple of a stock MSX; the default 1 reproduces the slowdown the game was tuned around |
| `-hz n` | 50 or 60 |
| `-state dir` | where snapshots go |
| `-rectape f` | record every frame's input to a tape |
| `-data f` | where to find `<name>.dat` for a build without `msxdata` |
| `-disk f` | where a changed floppy is written on exit (floppy builds) |

## The headless harness

    go build -tags msxdata ./cmd/<name>
    ./<name> -frames 3000 -digest 500 -png last.png

It runs frames and reports. `-digest n` prints a hash of VRAM, work RAM and
the sound registers every n frames — the equality test between two builds.
`-png` writes the last frame; `-vramspan from:to:file` appends VRAM and the
VDP registers each frame for `vramcmp`; `-tape` replays input;
`-savestate`/`-loadstate` snapshot and resume; `-extract` writes the data
sidecar; `-learn` records every address that had to run interpreted, which
feeds the discovery loop:

## The discovery loop

A translation covers the code that was seen. To grow it:

    ./<name> -frames 5000 -learn round.txt     # play, record what interpreted
    grep -v '^#' round.txt >> sites.txt        # feed it back
    msx2go -rom game.rom -out ./gamego        # regenerate: translated next time

`msx2go -discover n` automates up to n rounds of that for cartridges, and
`-tape` drives the runs deeper than an attract mode goes. An address never
fed back still works — it falls back to the interpreter at run time and is
written down. The loop is done when a long, varied run reports nothing new.

`-explore n` maps by force instead of play: it boots the machine, then
forks it at every conditional branch and runs both arms, at most n
instructions in total. Every fork is a real machine state, so dynamic
jumps — dispatchers, threaded code — have concrete targets, and because
only code coverage is wanted, each branch arm is forked once and the work
stays linear. What it finds goes to `explored.txt` beside `sites.txt`:
explored addresses seed the tracer but are candidates, not observations —
a forced arm can walk into data — so pruning never believes them, and the
`-interpret` twin comparison remains the test of truth. On Breaker, one
72-second exploration reaches within a few percent of what three
interactive learn rounds found.

For a floppy, the main game thread runs interpreted by design (the
translation currently enters through the per-frame interrupt path);
`sites.txt` in the output directory is picked up the same way.

## msxrun: the workbench

    go build ./cmd/msxrun
    msxrun -rom game.rom -frames 600 -digest 600
    msxrun -dsk game.dsk -run GAME.BAS -frames 600

msxrun interprets an image directly — no generation step — with the same
machine the generated modules use. It exists to answer questions: most of
its several dozen flags are instruments (watch a memory range, log VDP
commands, trace BIOS calls, dump VRAM at a frame) grown while verifying the
titles above, and `msxrun -h` describes each. The three worth knowing:
`-frames`, `-digest`, `-vramdump`.

`vramcmp` renders a reference VRAM dump and a `-vramspan` capture through
the same rasteriser and passes if any frame in the span shows the identical
picture — the tool for "does it draw what the real machine draws".

## Verification

The claim "behaves like the cartridge" is checked, not assumed:

- The `-interpret` twin. Generate the same game twice, once translated and
  once interpreter-only, and compare `-digest` output over thousands of
  frames. Any divergence is a translation bug with a frame number attached.
- The reference machine. `ref/` drives openMSX headless over its control
  channel and digests work RAM at every interrupt, VRAM at every frame, for
  comparison against the real BIOS ROMs. See `ref/README.md`.
- `go test ./...` covers the machine model itself.

## What is modelled

- **Z80**, with an explicitly modelled stack — cartridge code treats return
  addresses as data, and the translation preserves that.
- **VDP**: TMS9918 and V9938 — tile and bitmap screens 0–8, the command
  engine, sprites in both modes, mid-frame register changes (split screens,
  line interrupts), vertical scroll, and the interleaved VRAM of screens
  7/8. The V9958's extras (YJK screens 10–12) are not modelled yet, so
  `msx2plus` covers only titles that stay within V9938 features.
- **Sound**: AY-3-8910 PSG and the Konami SCC, synthesised on the machine's
  clock.
- **BIOS and sub-ROM**: implemented as documented routines, not copied
  code — each entry written from the datasheet and measured against a real
  machine when the documentation ran out. The character set is C-BIOS's
  (BSD-licensed), not a copy of any Microsoft ROM. An entry no verified
  game has needed yet reports itself loudly rather than guessing.
- **Disk**: FAT12 floppies, the BASIC loader vocabulary, the disk BIOS and
  BDOS calls the verified titles use, write-back for games that save.
- **Timing**: instructions cost cycles; a handler that overruns the frame
  loses its next interrupt, which is what the hardware does and what the
  games were tuned around. `-cpu` relaxes it deliberately.

## Honest limitations

- Coverage follows verification. The BIOS and machine model implement what
  the verified titles exercise, measured against real hardware; other games
  will work to the extent they use the same surface, and a missing routine
  names itself at run time rather than failing silently.
- Floppy main threads run interpreted (see above). The interpreter is
  hundreds of times faster than the hardware, so this costs nothing you can
  see or hear.
- No cassette, no MSX-DOS 2, no mouse yet, no Kanji ROM, no V9958.
- PAL/NTSC is a `-hz` switch, not a per-machine model.

## Legal

msx2go contains no copyrighted ROM code or data, and asks you to supply
the game image. What the tool generates from that image contains the
game's code (as translated source) and data: **a generated module, and any
binary built from it with `-tags msxdata`, embodies the original game and
carries its copyright.** Keep the output as private as the image it came
from, unless you hold the rights.

msx2go itself is licensed under the Apache License 2.0 (see LICENSE). The
built-in character set is from [C-BIOS](https://cbios.sourceforge.net/),
BSD-licensed; its notice is in the NOTICE file and travels with every
generated module. openMSX is used only as a development reference and is
not part of msx2go or its output.

## More

- `PLAN.md` — the design: why translation, the output's shape, milestones.
- `docs/bios.md` — how BIOS entries are implemented and measured.
- `games/*/README.md` — per-title verification notes.
- `ref/README.md` — the openMSX reference rig.
