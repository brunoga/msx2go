# Space Manbow

Konami, 1989. 256 KB, konami-scc, **MSX2**.

*(This file is the development journal, kept in the order things were
learned. Its opening describes where the port stood at the start; by the
end — and today — the game is verified against the reference machine,
V9938 and all. Read it as history, not status.)*

It boots and runs. It does not yet look like anything, and the reason is not
subtle: this is a V9938 game and the machine has a TMS9918.

## What it took to get this far

Two assumptions this project had quietly built into it, both from having only
ever run MSX1 cartridges that work the same way as each other.

**Interrupts during INIT arrived only inside BIOS calls.** That model came from
King's Valley, whose INIT relies on the real BIOS enabling interrupts inside
its own routines, and it was the only model there was because the machine had
no clock. Space Manbow's INIT enables interrupts itself at 4306h and then spins
on a counter its handler increments:

    4306: fb           ei
    4307: 3a 14 c9     ld a,(C914)
    430A: fe 02        cp 02
    430C: 38 f9        jr c,4307      ; wait for two interrupts

It never calls the BIOS in that loop, so it waited forever. Now that cycles are
counted, an INIT that has had interrupts enabled for a second with none
arriving gets one from the clock. King's Valley and Salamander both get theirs
from the BIOS well inside that second and are untouched -- King's Valley's
output is byte-identical across 3,000 frames.

**A game's loop was assumed to be its interrupt handler.** King's Valley and
Salamander both settle INIT into an idle loop and do everything in the handler,
so `Boot` treated an INIT that did neither as a broken cartridge. Space Manbow's
game loop *is* INIT, running in the main thread with the handler underneath it.
`M.MainThread` says which shape a cartridge is, decided by whether INIT is still
running after ten seconds of machine time.

Also shimmed CHGCLR (0062h), read out of C-BIOS at 02D4h: register 7 takes the
foreground colour in its high nibble and the border in its low one, and in
SCREEN 1 the colour table is repainted as well.

## The V9938, so far

The chip is now the one the cartridge is expecting, and nothing has to be
configured to make that happen: naming a register a TMS9918 does not have is
the evidence, and `goV9938` grows video memory to 128K on the spot. An MSX1
cartridge never names one and stays at 16K, which matters because every digest
and every comparison against a reference is over the whole of it.

What the cartridge actually asked for, once the machine stopped mangling the
question:

    R14 written    417 times     the high bits of the video address
    R15 written  69154 times     which status register to read
    R16 written     64 times     the palette index
    R17 written    108 times     the indirect register pointer
    R32..R46      ~108 times     the command engine

It writes registers 0 to 7 **not once**. Everything goes through port 9Bh,
aimed by register 17, which the machine did not implement -- so the video chip
was one nobody had configured. And R0/R1 come from the BIOS's WRTVDP, which
masked the register number to three bits, sending register 9 (212 lines) into
register 1.

With those fixed it reports what it is: `mode ModeGraphic4, 212 lines, page
base 00000h, V9938=true, VRAM 128K` -- SCREEN 5, 256x212, four bits a pixel.

Implemented since: the register file at its real width, register 14's address
bits, indirect register writes, the palette (port 9Ah, three bits a component),
status register selection, the command engine's rectangle commands (LMMV, HMMV,
LMMM, HMMM, YMMM, LINE, PSET, POINT), Graphic 4 rendering, and the V9938's
sprite mode with its per-line colour table.

It draws. **The title screen is correct**, and so is the intro that follows it,
for about three thousand frames.

Two bugs stood between "draws" and "draws correctly", and neither was in the
cartridge:

**The palette was computed in byte arithmetic.** `n * 255 / 7` spreads three
bits of colour over eight, and `n*255` overflows a byte for anything above one,
so every component came out 35 or 36. A whole palette of identical dark grey
looks exactly like a game that has faded out, which is what it was mistaken for.

**The high-speed commands were given byte coordinates.** HMMV, HMMM and YMMM
move whole bytes but are still handed their coordinates in dots like every
other command. Reading a 256-dot fill as 256 bytes makes it twice as wide as
the 128-byte line it is filling, so it runs into the next one -- and a screen
wiped that way turns to stripes and then to noise a few thousand frames later.

## The renderer is right: checked against the hardware's own output

openMSX will dump video memory, the registers and the palette, and take its own
screenshot of the same moment. Rendering that dump with this renderer produces
that screenshot, pixel for pixel -- Graphic 4, the palette, the page, the
scroll. So the video path is not where the remaining trouble is.

    ref/: a -script file of `after time N { screenshot ...; debug read_block VRAM ... }`
    msxrun -renderdump <dump> -png <out>

That tool is worth keeping for its own sake: it separates a renderer that is
wrong from a machine that has put the wrong thing in memory, which is otherwise
very hard to tell apart from a screenshot.

## Fixed since: the vertical scroll register

Register 23 slides the display down the page and wraps at 256 lines. It is how
a V9938 game scrolls without moving a pixel, and Space Manbow sets it to C0h.
Ignoring it does not show a stationary picture -- it shows whatever else is in
memory at the lines you read instead, which is why the game appeared to
dissolve into noise the moment it started scrolling. With it honoured, the
status bar -- POWER, SCORE, HI -- draws correctly.

## It runs

The full attract sequence plays: Konami logo, story text, the planet
cinematic, and the stage-one gameplay demo -- fortress, turrets, boss cannon --
in SCREEN 4 with the vertical scroll doing the scrolling. What it took, in
order, each found by tracing rather than guessing:

- **The main thread was paced by an instruction quota, not the cycle budget** --
  twenty-seven frames of work per delivered vblank, so the handler found the
  flag already spent twenty-six times out of twenty-seven.
- **An unsigned underflow delivered an interrupt on every main-thread
  instruction.** The frame loop parks `lastIRQ` in the future to keep the
  clock quiet; `Cyc-lastIRQ` underflowed and fired instead.
- **The boot-to-main-thread hand-back happened mid-instruction.** The runaway
  fired from tick, after the opcode fetch; the resumed thread executed operand
  bytes as opcodes and was dead within a frame. It now stops at the next
  instruction boundary.
- **The interrupt stack frame.** The BIOS pushes ten words before calling the
  hook, and this game's ISR unwinds them itself and rets straight to the
  interrupted code. Delivered onto a bare stack, its epilogue ate the sentinel
  and returned into the ROM header's padding.
- **FH latches only when IE1 is enabled** -- measured on openMSX: through the
  whole intro, IE1 off, S#1 reads zero at every sampled instant. Arming it
  unconditionally left a stale flag that sent every vblank ISR down the line
  branch, and the frame counter the main thread waits on never moved.
- **The vblank flag rises on the clock**, so an ISR that waits for the *next*
  frame inside itself sees it rise.
- **S#2's HR bit is the horizontal retrace**, and the ISR spins on it at 42A4h
  to time a register change to the raster. A constant status register spun it
  for fifty million instructions. HR and VR now derive from the frame clock.
- **SCREEN 4 through a V9938 renderer**: table bases over all 128K, the
  palette, 212 lines, register 23's scroll, and sprite mode 2 with its
  masked attribute base.

## Generated and playable

`msx2go -rom spacemanbow.rom -out dir` detects the shape ("the game loop is
INIT itself"), keeps the image whole -- an interpreter executes from memory,
and pruning removes exactly the bytes the translation covers -- and produces a
module whose ISR runs translated while the main thread runs interpreted. It
builds, runs 6,000+ frames, plays in a window with the same harness as the
other games, and its clock agrees with the interpreter's to the cycle through
frame 2956.

### The learning loop

`-learn <file>` (both harnesses) writes every address that had to run
interpreted, in the form `msx2go -sites` reads. Append it to the module's
sites.txt and regenerate: interpreted this time, translated next time. A
3,000-frame demo run records 5,401 sites. Each generation interprets less
than the last, which is the road to a fully translated cartridge.

### The split screen

The gameplay demo splits its frame: the vblank ISR sets one scroll-and-page
state, a line interrupt at raster line 136 sets another, and register 18's
adjust cycles for sub-tile smooth scrolling. The machine now logs register
writes by raster line (in display coordinates -- our frame starts at the
blanking, fifty lines before the picture) and the renderer replays the log
per scanline. S#2's VR flag is likewise computed in display coordinates.

### The line interrupt is aimed relative to the scroll

The garble's last cause, measured rather than assumed: 400 samples of R23
against the frame phase on the reference machine show state A (the HUD's
scroll and page) in force for exactly 80 raster lines -- the blanking plus the
first ~28 of the display -- and state B (the playfield) for the other 182.
Which decodes the compare: **the line interrupt fires at display line
(R19 - R23) & 255**, not at R19 lines from anywhere. Space Manbow's split is
(DBh - C0h) & FFh = line 27, the bottom of its HUD.

And since each split handler re-points R19 for the next split, the machine
re-aims on every write to R19 or R23 -- which is also what lets several line
interrupts land in one frame. With that, every scene renders clean: the
fortress, the boss tower that used to be red-yellow noise, the mid-boss
carrier, the cinematic. Playable in a window.

## Sprites: the attribute table was 512 bytes low

The sprite attribute table's base is register 5 with its low **two** bits
dropped, over register 11 -- and in sprite mode 2 the colour table sits in the
512 bytes below it. Masking one bit too many (0xF8 where 0xFC belongs) landed
the attributes exactly on the colour table, where every Y byte reads as zero
and nothing draws at all: no ship, no shots, no enemies.

Measured against the reference machine's own memory: R5=EFh puts the
attributes at F600h and the colours at F400h, and F600h holds real entries
(y=32 x=16 pat=76, y=32 x=200 pat=132) where F400h holds zeros.

Two more found alongside it:

- **Early clock is per line.** Bit 7 of a line's colour byte shifts *that line*
  32 pixels left. Subtracting it once per line while scanning the sixteen
  colour bytes, as this did, could move a sprite 512 pixels off screen.
- **The end-of-list marker follows the line count**: 216 in a 212-line screen,
  208 in a 192-line one.

## The border colour needs the programmable palette

Space Manbow sets register 7 to FFh -- border colour 15 -- and then programs
colour 15 to black. Reading that index through the fixed MSX1 table paints a
white border around a game that should have none. `VDP.BorderRGBA` reads it
through whichever palette the chip has.

## Ground truth for the split, measured

Watching port 99h writes against the frame clock on the reference machine,
during the gameplay demo (frame 10,000), each frame does this:

    line  62.3  R1  <- 22   (display off)
    line  62.5  R0  <- 04
    line  62.7  R8  <- 28
    line  62.9  R23 <- 1C   \
    line  63.1  R18 <- 08    |  the playfield's state
    line  63.3  R2  <- 30    |
    line  63.5  R5  <- E7   /   <- a *different sprite table*
    line  63.7  R1  <- 62   (display on)
    line  63.9  R19 <- 88
    line  64.1  R0  <- 14
    line 143.9  R5  <- EF       <- and a third one, mid-playfield
    line 248.x  R23 <- C0, R18 <- 00, R2 <- 3F, R19 <- DB  (the HUD's state)

Two things follow. The split lands where we put it -- comparing the reference
screenshot row by row against our two states, display rows 0-27 match state A
and rows 120+ match state B, with the rows between identical in both. And
**register 5 changes twice per frame**: the game swaps sprite attribute tables
per band, which is how an MSX2 game shows more than 32 sprites, and which this
renderer does not model -- it draws every sprite from the last table of the
frame.

## The screen is 212 lines, and this drew 192

Register 9 bit 7 asks for a 212-line screen and Space Manbow's stages use it,
but the renderer's buffer was 192 lines however tall the screen was -- so the
bottom twenty rows were simply not drawn. In stage one that is the entire rock
floor.

The 192-line buffer is kept separate and returned unchanged for any screen that
asks for 192, because every MSX1 comparison in this project is of a 256x192
image and none of them should move. The harness follows the picture: the
texture is rebuilt when the height changes, which it does when Space Manbow
goes from its 192-line intro to its 212-line stages.

Measured against a paused reference (openMSX halted, then screenshot and VRAM
dumped together, so the two are of the same instant rather than a frame apart):
mid-screen now matches pixel for pixel, 256 of 256 on every sampled row from
32 to 144.

## How Space Manbow scrolls, and why the sprites matter

The V9938 has a vertical scroll register and no horizontal one. Konami scrolled
horizontally with **register 18**, the adjust register -- a calibration control
meant for centring the picture on a CRT, which shifts the whole video signal a
pixel at a time. Sixteen pixels of that, then a block copy moves the tiles over
and the adjust resets.

Because it shifts the *whole signal*, the borders jitter with it, and Konami
covered the left and right edges with black sprites. That makes the sprite path
part of the scrolling machinery rather than decoration: with the attribute
table read 512 bytes low, those masking sprites were among the ones that did
not draw, and the edges flickered.

## Open

- **The HUD does not draw.** Comparing pixel rows, our state A is entirely
  black where the reference has the POWER bar (display y=6) and the SCORE text
  (y=18). The name table state A selects (R2=3Fh, so FC00h) is empty at the
  rows the scroll points to, and no other R2 reproduces it either -- so
  something about how a scrolled tile row is addressed in Graphic 3 is still
  wrong. Note the ship is *not* a sprite in the reference dump: no attribute
  table holds an entry near its position, so the playfield's moving objects
  are tiles.
- **Per-band sprite tables** (above) are unmodelled, which is the likeliest
  cause of sprites appearing in bands they do not belong to.

- **Interpreter and translation part company at frame 2957**: one event
  costs the translated build 40,867 extra cycles, and thereafter the two
  drift by a frame or so of pacing. Scenes render identically; the
  byte-exact invariant the MSX1 games hold does not yet hold here.
- The in-game HUD region (top 28 lines) has not been verified against the
  reference during real play, only the demo scenes.

## Old notes: the playfield

The status bar is right and the playfield below it is not. What is known:

- The command engine stops being used entirely after about frame 3000 -- the
  counts at frame 3600 and frame 5200 are identical -- so the playfield is
  built once and then scrolled.
- During gameplay there are only about 365 data-port writes a frame, all into
  page 1, which is the edge of a scroll rather than a redraw.
- So the playfield was composed wrongly *earlier*, most likely by the 6,040
  LMMM copies that run before frame 3000.

Worth knowing while chasing it: our machine runs the intro faster than the
reference does -- at 95 seconds openMSX is still on a scene we passed a while
back -- so comparing "our frame N" against "the reference at N/60 seconds" is
not sound here the way it was for Salamander.

Also still true: the BIOS block routines mask their destination to fourteen
bits (`&0x3FFF` in `bios.go`), which is right for a TMS9918 and loses the page
on a V9938.

### Ruled out

- **The renderer.** It reproduces openMSX's own screenshot pixel for pixel from
  openMSX's own video memory. See above.
- **X running off the right edge of a copy.** The coordinate registers are nine
  bits and Graphic 4 is 256 dots wide, so a copy that overruns wraps within the
  line rather than spilling into the next. Fixed, and it changed nothing here --
  but it was wrong.
- **The line-interrupt flag.** Register 19 points at line 219 and IE1 is on, so
  status register 1's FH bit looked like something the game might be waiting
  for. Raising it once a frame made the game spend its entire instruction
  budget every frame and issue no extra commands at all -- so it is waiting on
  something, but not on that. Reverted rather than left in.

### The shape of it

The playfield stops being redrawn. A horizontal scroller on a V9938 moves its
screen with the command engine and writes only the new edge column directly,
and the direct writes are there -- about 365 a frame, which is roughly one
column of 212 lines -- while the copies are not. So the game reaches the code
that fills in the edge and not the code that moves everything else.

## What is next

The picture is garbage because every MSX2 video feature is missing, and one of
them is actively destructive:

- **Registers 8 to 23 do not exist.** `WriteCtrl` masks the register number with
  7, so a write to register 15 lands on register 7. An MSX2 game does not merely
  lose the new registers, it corrupts the old ones.
- **128 KB of VRAM**, against 16 KB, with register 14 supplying the high address
  bits.
- **Screen modes 4 to 8**, which are bitmap modes and not the tile-and-pattern
  arrangement the renderer knows.
- **A programmable palette**: 512 colours through register 16 and port 9Ah,
  rather than sixteen fixed ones.
- **The command engine** -- registers 32 to 46 and port 9Bh -- for copies, fills
  and lines. MSX2 games lean on it heavily.
- **Status registers 0 to 9**, selected by register 15, where the TMS9918 has
  one.

None of that is hard in the way the timing was hard; it is a known chip and a
known amount of work. It is milestone M6.

## The status panel is a different screen mode

The panel across the top is not tiles. Register 0 carries M3, and the frame
writes it twice:

    line   3  R0 <- 16    M4 and M3 -- SCREEN 5, a 4bpp bitmap
    line  80  R0 <- 14    M4 alone  -- SCREEN 4, the tiled playfield

so the top twenty-nine lines are a bitmap page and the rest is a tile screen,
in the same frame. Nothing in the tile path can draw the panel, and the search
that settled it is worth keeping: rendering the reference machine's own VRAM
through every one of the 128 name-table bases, all 32 rows, 8 fine rows and 4
pattern blocks -- comparing edges rather than colours, so a mid-frame palette
change could not hide a match -- found nothing above noise. Read as a SCREEN 5
page at 8000h (R2's bits 5-6, not its low seven) with R23=C0, the same memory
scores **99.5%** against the screenshot. The panel had been black since the
cartridge first booted.

Registers 0, 1, 8 and 11 therefore join the split log, and the renderer
dispatches *per scanline*: mode, page, scroll, sprite table and the sprite
plane's on/off bit are whatever that line's registers say. Register 1 bit 6
blanks the line or two a cartridge spends switching, as the hardware does.

Sprites are drawn per scanline for the same reason. R5 moves twice in a frame
(E7h for the panel, EFh mid-playfield), so a frame drawn from whichever table
the register held last scatters one band's sprites through the others.

## A line interrupt outlives the `di` it arrives in

Space Manbow's main thread copies to VRAM under `di`. When the raster reached
register 19's line inside such a copy, dueLine raised FH and threw the request
away, so the frame never switched back to the playfield state and the whole
screen was drawn in the panel's mode and scroll -- the flash.

The hardware holds INT asserted while FH stands and the processor takes it at
the next `ei`. Holding it the same way (fhHeld) takes the loss from 16 frames
in 300 to 8. Measured both sides: the reference machine writes register 19
exactly twice in each of 401 consecutive gameplay frames, never once or three
times, so any frame of ours that does not is ours to explain. The eight that
remain are frames whose work overruns the budget far enough to lose an
interrupt outright, which is a cycle-cost question, not a rendering one.

## openMSX's palette is gamma 1.1

Comparing our colours against openMSX's screenshots, every band mapped index
to colour at 100% consistency but with different RGB: level 1 is 43 there and
36 here, 2 is 81 against 72, 3 is 118 against 109. That is `(n/7)**(1/1.1)`
against our linear `n*255/7`. It is a display choice of openMSX's, not the
hardware's, but it is worth knowing before reading a percentage: it alone
scored bands that were pixel-exact at about 50%.

## The cartridge asks what machine it is on, and we were answering wrong

Three bytes a stock MSX2 has and msx2go did not install:

    002Dh    MSX version: 1 on an MSX2, and we left it 0
    F3DFh    RG0SAV..RG7SAV, the BIOS's copy of registers 0 to 7
    FFE7h    RG8SAV, its copy of register 8 -- 08h at boot

Bit 3 of register 8 is VR, which tells a V9938 its memory is 64K or more.
The cartridge reads the saved byte, keeps that bit and writes its own on top,
so a zero there configured the chip for a machine with a sixteenth of the
memory. Everything downstream moved: it put its sprite attribute tables at
E7h/F7h instead of F7h/FFh, and aimed the third line interrupt at register 19
= 88h instead of A8h -- display line 108 rather than 140. The player's ship
was read out of the wrong table, which is why it sat in the wrong place.

Read from the reference machine rather than guessed: `debug read memory 0x2D`
is 1, and FFE7h holds 08h three seconds after power-on, before the cartridge
has touched anything. With the bytes installed our register writes match the
reference's: R5 F7h, R19 A8h, R8 08h/0Ah against its 28h/2Ah.

King's Valley and Salamander are unaffected -- VRAM and PSG digests are
identical at frames 1000, 2000 and 3000 before and after. Only the RAM digest
moves, and that is the installed bytes themselves.

Still out: the reference sets register 8's TP bit (20h) and we do not, and its
frames do not alternate between two split layouts the way ours still do.

## The command engine takes time, and the intro was three times too fast

msx2go's VDP finished every command the moment it started, and reported its
busy flag clear for ever. Space Manbow's intro issues 6,638 commands and polls
S#2 ten thousand times waiting for them, so it ran its animation as fast as
the host could copy memory: gameplay began at our frame 3,087 where the
reference machine takes until t=153.06s, frame 9,184. Three times too fast,
and the audio -- paced by interrupts, not by the command engine -- stayed
where it was, which is why the two came apart.

The cost was measured on the reference, not guessed: a watchpoint on the write
of the command register notes the time, then the busy flag is polled every ten
microseconds of machine time until it falls.

    HMMV  32,640 bytes    8.24 cycles a byte
    HMMM   3,072 bytes   16.16 cycles a byte
    LMMM      64 pixels  23.40 cycles a pixel

Those are one, two and three memory accesses per unit -- a fill writes, a byte
copy reads and writes, a pixel copy reads the source byte, reads the
destination and writes it back -- so all three land on **8.2 cycles an
access**, which is the only constant the model needs. The memory still changes
all at once; only the flag waits.

With it, our intro reaches gameplay at frame 9,314 against the reference's
9,184: 1.4% out. King's Valley and Salamander never issue a command, and their
VRAM, RAM and PSG digests are unchanged.

## WRTVDP has to save what it writes

The BIOS keeps a copy of every VDP register it writes, because the registers
are write-only: a cartridge that wants to change one bit reads the saved byte,
edits it and writes it back. msx2go's WRTVDP shim wrote the chip and saved
nothing, so what a cartridge read back was whatever the boot values were.

Space Manbow's gameplay setup writes register 8 once with the transparency bit
set (2Ah, at t=150.85 on the reference, from cartridge code at 706Eh), and its
handler thereafter rebuilds the register from the saved copy twice a frame --
sprite plane off over the status panel, on again below. Ours wrote 2Ah exactly
once in a 9,500-frame run and 08h/0Ah for ever after; the reference writes
28h/2Ah every frame. The shim now saves to RG0SAV and RG8SAV as the real one
does, and our values match.

That bit matters to the picture. Register 8 bit 5 is TP: with it clear colour
0 is transparent and shows the backdrop, with it set colour 0 is palette entry
0 like any other colour. Space Manbow's playfield zeroes are a deep blue and
we were drawing them black. Scored against the reference's own memory and
screenshot, rows 30 to 211 go from 89.31% to **92.22%**.

What the horizontal adjust exposes at the edge stays the backdrop -- that is
the border, not a transparent pixel -- so the two colours are now carried
separately through the scanline.

## An interrupt outlives the `di` it arrives in -- the vertical blank too

The line interrupt was fixed to survive a `di`; the vertical blank was not.
mainThreadFrame delivered the frame's interrupt only `if m.IFF`, and dropped
it otherwise -- so a main thread that happened to be inside a block copy as
the frame turned lost that frame's handler outright: no split, no scroll, the
whole screen drawn in the status panel's state. That is the flash.

The hardware holds INT asserted while the flag stands and the processor takes
it at the next `ei`, so the frame is late, not lost. Held the same way, our
gameplay frames write register 19 exactly twice in **300 of 300**, which is
what the reference machine does in 401 of 401. It was 284 of 300 when the
hunt started.

## The SCC was an octave low, and it hissed

Two faults in the wavetable chip, both measured against a recording of the
reference machine made with openMSX's `soundlog` while its SCC period
registers were logged by watchpoint.

**The clock.** The SCC's tone is `clock/(32*(period+1))` and the clock is the
whole 3.579545 MHz colour-burst crystal. msx2go used `Clock`, which is that
crystal *halved* -- the rate the PSG counts -- so every SCC voice played an
octave down. The code even said 3.58 MHz in its comment while using the other
constant. Ten notes from the reference, each compared against the magnitude of
its own neighbourhood in the spectrum:

    3.58 MHz prediction   9.5x above the surrounding noise (32x, 27x, 16x)
    1.79 MHz prediction   1.4x -- which is the noise

**The hiss.** The chip walks its 32-byte table at up to 170 kHz and its
analogue stage averaged what came out. We point-sampled it at 44.1 kHz, so
every note whose table steps faster than the output rate folded its harmonics
back into the audible band. The PSG had always averaged over the ticks inside
each output sample; the SCC now does the same.

Both together, against the reference's own spectrum:

    band        reference   before    after
    <250 Hz          3.8      5.4      3.1
    250 Hz-1 kHz     8.2      8.2     10.3
    1-4 kHz          7.7      3.9      8.8     <- the octave, as a hole
    4-8 kHz         35.2     40.5     34.0     <- the hiss
    >8 kHz          45.1     42.0     43.8

Total deviation across the five bands halves, 13.8 points to 6.4. `msxrun
-wav` writes the sound the harness would have played, which is what made the
comparison possible.

## The sprite plane moves with the display

Sprites were drawn at the coordinates in their attribute table, as if the
scroll registers did not exist. They do: the vertical scroll shifts a sprite
up the screen exactly as it shifts the pattern under it, and register 18's
adjust slides it sideways with the rest of the signal. A sprite's display
position is `Y + 1 - R23` and `X - adjust`.

The tell was a band of debris across the top of the playfield that the
reference does not have. Space Manbow parks every sprite it is not using at
Y=32; with the scroll at 28 that is display line 5 -- under the status panel,
where the same handler has just turned the sprite plane off. Drawn without the
shift they land on line 33, in the open field. The horizontal half came out of
the same search: over every pattern base and offset, the only combination that
scored above noise was dy = -R23, dx = -adjust.

With it, the reference machine's own memory rendered through our renderer
reproduces its screenshot exactly:

    rows          before   after
    29-60          90.2%   100.0%
    61-150         99.4%   100.0%
    151-211        99.3%   100.0%

(Within a tolerance of 12 per channel, which absorbs openMSX's gamma -- see
above. Every band is otherwise pixel-for-pixel.)

## The sound is made on the machine's clock, not the card's

The harness generated samples inside the audio callback, from the chip
registers as they stood at that moment. The registers advance on the
emulation's clock -- one frame, sixty times a second -- and the callback
arrives on the sound card's, asking for whatever its buffer holds. Those are
two independent clocks, and a buffer that is not a whole number of frames long
covers two frames of register changes one time and three the next. That
modulates when every note begins, and it is heard as the music slowing down,
speeding up and slowing down again, in time with the beat between the rates.

Now a frame of emulation makes exactly one frame of sound -- 735 stereo
samples at sixty frames a second -- into a ring the callback drains. A frame
is always worth the same number of samples, so the tempo cannot wander. What
is left of the difference between the two clocks is a buffer that fills a
little faster or slower than it drains, and three frames of cushion absorb
that; the cushion is built before playing starts, and rebuilt if the ring ever
runs dry, so a game that was paused comes back without stuttering. An underrun
holds the last sample rather than snapping to silence.

## The interpreter needs the stack level it took over at

When translated code reaches an address the tracer never proved reachable,
`noLabel` hands over to the interpreter. `Run` in run_stub.go -- the one
msx2go itself uses -- has always started the interpreter with the stack level
the call began at, so that a return past it means the call is over. `noLabel`
passed zero, which turns that check off, leaving only "the program counter
reached the sentinel" to stop it. A routine that leaves through the stack
rather than by returning to the sentinel never trips that, and the interpreter
runs on into whatever follows.

Space Manbow does exactly this when a game is started. The symptom was a crash
into a restart vector -- `rst 38h`, which is what FFh is when it is executed --
somewhere far from the cause. The panic now says where the machine was and
what the stack thought called it, and says plainly that a restart vector means
something ran off into data rather than that a shim is missing.

## What is left

A second divergence remains, and it is a translation bug rather than a machine
one. With the full sweep's translation, starting a game on some paths burns
fifteen million cycles in one frame -- two hundred and fifty frames' worth --
and corrupts itself. The same module with its dispatch disabled, so that every
instruction interprets, runs the same input for nine thousand frames. Both
builds agree on the clock to the cycle through frame 247 and on every live
byte of memory, and part company inside frame 248, at the first hand-off.

A build made from the learned sites alone -- `-sweep 0 -sites <learned>`,
5,661 instructions translated instead of the sweep's whole set -- survives all
fourteen press patterns. It is a smaller translation, not a fixed one, and the
machine runs about five hundred times faster than it needs to, so nothing is
lost by it while the real cause is found.

## Starting a game is broken, and the first reading of it was wrong

Reported as "space does not start a game any more". It does what the
reference machine does, which was worth establishing before changing
anything. Driving openMSX's key matrix directly and watching register 9 for
the 212-line screen that only gameplay uses:

    a single quarter-second press at t=25   never starts   (reference and ours)
    the key held down from t=20 to t=60     never starts   (reference and ours)
    tapped every five seconds from t=20     starts by t=30 (reference and ours)

That looked like agreement and was not. Tapping *continuously* keeps
restarting the thing, so "the 212-line screen is still up" proves nothing
about whether a game is running. The experiment that decides it is to tap
enough to get in and then **stop**:

    tapped at t=20, 25, 30 and then nothing more
      reference   in gameplay at t=35, t=45, t=60, t=90
      ours        not in gameplay at any of them

So the cartridge does not need continuous tapping; our machine does, and even
then it falls out again within a few seconds. This is ours, not the
cartridge's, and the earlier reading of it here was wrong.

It is not the translation: a module built with -interpret, which has no
translated code in it, does the same. It is not the input path: the tools now
press keys exactly as the window does, through the joystick port and the
matrix together, and it makes no difference. It is not the stack rule that
bounds a hand-off: removing it entirely does not reach gameplay either, it
only fails differently.

What it looks like from the outside -- a level that starts at random and then
changes on its own -- is a game whose stage state is being driven by something
that should not be touching it. That is where to look next.

## The tools have to press keys the way the window does

The window sends one press to a cartridge twice: through the joystick port
and through the keyboard matrix, because a cartridge is entitled to read
either. Neither headless tool did. msxrun's `-tape` is the twelve-byte key
matrix and its monkey is the matrix too; the generated module's `-tape` was
the matrix as well. So a press that started a game in the window did nothing
in either tool, and comparisons between them were not comparisons at all --
hours were spent reading a difference that was the input, not the machine.

msxrun grows `-btape`, one z80.Buttons byte a frame driving both, and the
generated module's `-tape` now drives both. What the window does is what they
do.

## What is still wrong

The translated build enters gameplay and draws nothing: 212 lines of
backdrop. The same cartridge converted with `-interpret`, which emits the
same module with no translated code in it, draws the game. That is a
translation divergence and it is not fixed.

Beyond it, both of ours leave gameplay earlier than the reference does under
the same tapping -- ours at t=40 to t=100, the reference still playing at
t=210 -- so there is a second difference behind the first.

## The machine has to start where a real one starts

Three things a real MSX has at the moment a cartridge runs, and this machine
did not. All three read off the reference rather than invented.

**The VDP's registers.** A real machine is showing SCREEN 1 when the cartridge
gets control -- the BIOS put it there -- so registers 0 to 7 hold
`06 60 1F 80 01 EF 0F F1`, not zero. Space Manbow never writes register 3 and
on hardware finds the 80h the BIOS left. Ours had 00 there. Fixed, and at
t=19, before any keypress, our register file now matches the reference's
exactly and video memory differs in 379 bytes of 131,072.

**The slot layout.** Port A8h reads D4h while a game runs: page zero the
BIOS's slot 0, pages one and two the cartridge's slot 1, page three RAM in
slot 3. Ours read zero, which says every page is slot zero and is nothing
like this machine. EXPTBL is `00 00 00 80` and SLTTBL `00 00 00 A0`.

**Page zero is the BIOS only while the BIOS is selected there.** isBIOS
treated every address below 4000h as a BIOS entry point. A cartridge may
switch page zero to RAM and run code there -- the reference reports page zero
in slot 3 partway through Space Manbow's intro -- and a shim then runs in
place of the cartridge's own code. It now checks the slot register.

None of the three fixed the game, and they are worth having anyway.

## Where it actually goes wrong: a keypress during play ends the game

Isolated to one action, with the reference doing the opposite. Tap at t=20 and
t=25 to start a game -- both machines are then playing, ours from t=27 and the
reference from t=25.35 -- and then tap once more at t=35, well into the level:

    reference   in gameplay at t=33, still at t=38, still at t=45
    ours        in gameplay at t=33, out of it by t=38, blank by t=45

Two taps and no third: ours stays in gameplay at t=31, 35, 45, 60 and 90, the
same as the reference. So starting works. What does not is pressing anything
*while* playing, and that is the whole of the reported fault: hammering the
key starts a game, the next press of the same key ends it, the attract
sequence comes back round, the next press starts another -- which is what "it
starts in a random level and then switches levels by itself" looks like from
the outside.

It is the space bar itself, not the joystick: driving only the key matrix does
it too. The reference gets the same key, in the same row and bit, for the same
quarter second.

And it is space *specifically*. Pressing LEFT at the same moment does not end
anything -- the game plays on through t=38 and t=45 and the field scrolls, so
the key is read, acted on, and harmless. Only the fire button is fatal, which
narrows it from "input during play" to "what firing does".

## The line interrupt has to be re-aimed when its enable comes back on

Space Manbow's handler, at the split, does this:

    line 79   R0  <- 04    IE1 off
    line 79   R23 <- 1C    the playfield's scroll
    line 80   R19 <- 88    the next line to interrupt at
    line 80   R0  <- 14    IE1 on again

The compare on the chip is continuous: it does not care when the enable was
set, only that the raster reaches (R19 - R23). This machine re-aimed on writes
to registers 19 and 23 and nowhere else, and *cleared the schedule* when it
found the enable down -- so both writes above threw the schedule away, and
turning the enable back on re-aimed nothing.

The third interrupt of the frame therefore never arrived. Counted over twenty
seconds of play, the routine it exists to run went from **0 entries to 647**,
against the reference machine's 746, and the handler now runs three times a
frame the way the reference's does instead of twice. Register 0 joins 19 and
23 in re-aiming.

## The horizontal retrace is at the start of the line, not the end

The handler waits for S#2's bit 5 to rise before each raster-timed transfer.
How many times it has to look says which way round the line is, and the
reference machine answers it: over twenty seconds of play it reads the flag
1.05 times per transfer, because it is already set when the handler arrives.
This machine had the flag covering the last quarter of the line and read it
six times per transfer. Moved to the first quarter it reads 1.06 times.

Calibrated rather than assumed, through the game that depends on it. King's
Valley, Salamander and the disk keep their VRAM and PSG digests either way,
so nothing else in the set had an opinion.

## What is left, and what it is not

With the line interrupt aimed and the retrace flag the right way round, the
game starts, plays, and keeps playing for at least ninety seconds. Two things
still differ from the reference, and they are the same thing seen twice.

*A correction*: the "4158h entered 204 times against 300" in the note below
was an artifact. The two machines are at different points in the sequence
during those frames, so the windows do not line up. Counted over ten frames
where they do, both enter it once a frame, ten out of ten. Windows have to be
aligned on a state, not on a wall clock, before their counts mean anything.

**Our main thread loses time in the loading phase.** Between the first tap's
transition and gameplay the reference takes 4.5 seconds and ours 6.5. The work
in that phase is identical where it can be counted -- the graphics conversion
at AFAAh runs 17,632 times in both -- and the wait loop at 4CE0h spins 74,657
times on the reference against our 82,692. What differs is that the reference
enters the routine at 4158h three hundred times in three hundred frames, once
a frame, and ours enters it 204 times. Something calls it conditionally and
the condition fails in ours a third of the time.

**The second tap is not taken.** Both machines make a screen transition about
0.85 seconds after the first tap. The reference then *waits* -- and the tap at
t=25 starts a game 0.35 seconds later. Ours ignores that tap, whatever its
length, and arrives at a gameplay screen 2.3 seconds later by some other
route.

**So a later press is still a "start".** The fire press at t=35 runs the
screen-setup table at A6BDh -- eight registers including R9=00, 192 lines --
which is the transition the reference performs only between screens. The
reference writes R9 seven times in fifty seconds and never after entering
gameplay at t=25.35; ours writes it again at t=35.85, right after the press.

The two runs execute an identical *set* of addresses, so nothing branches
anywhere the other does not: what differs is the data they act on. The next
step needs a watch on RAM writes, naming the code that changes the byte the
game reads to decide whether a press means "start" -- which msxrun cannot do
yet.

Where firing goes: the code the fire press reaches and a run without it does
not is thirty-one addresses at 42BFh, inside the interrupt handler. It walks a
list through the pointer at C0A4h, and for each entry selects status register
2, spins on bit 5 -- the horizontal retrace -- and OTIRs a run of bytes to the
VDP. So firing queues a raster-timed transfer that the handler performs on the
next interrupt. That is what to follow: either the transfer lands somewhere it
should not, or the retrace flag it is timed against does not behave here as it
does on the chip. Ours sets that flag for the last quarter of every line,
derived from the frame clock, which is the right duration and an unverified
phase.

The delay loop at 60ADh that showed up first is a red herring: it is boot
code. The reference runs it 8192 times before a game starts and never again.

Ruled out along the way, each by measurement: the frame budget (both runs
count the same cycles to within tens, so nothing runs away); an abort branch
(the two runs execute the same code but for twelve addresses, a delay loop and
a lookup); and sprite collision, which was missing entirely and is now
implemented -- status register 0 reported only the vertical blank, with no
collision bit and no fifth-sprite number -- and which changes nothing here.
It costs nothing either: King's Valley and Salamander keep their VRAM, RAM and
PSG digests with it in.

## Where the start goes wrong (earlier reading)

Measured, with taps at t=20, 25 and 30 and nothing after:

    reference   register 9 <- 80h at t=25.35, and it stays
    ours        register 9 <- 80h at t=27.3, then 00h at t=30.8

So ours does enter gameplay and then leaves it, three and a half seconds
later, right after the third tap -- a tap that on the reference does nothing.
That is the thing to explain next: what a keypress during play does in this
machine that it does not do on hardware. Sprite collision is one candidate
that can be ruled in or out cheaply, because status register 0 here reports
only the vertical blank: bit 5, the collision flag, and the fifth-sprite
number are simply not implemented.

## The start, read from the source

The state machine, from the disassembly rather than from guessing (z80dasm on
banks 0 and 1; addresses are as mapped):

- **C900h** is the major state: 0/1 boot, 2 the logo-and-title sequence, 3 the
  attract that follows it, 4 game-start, 5 gameplay. **C901h** is the substate,
  **C914h** counts interrupts, **C904h** is the substate's frame counter.
- The main loop (435Bh) each iteration: clears C914h, reads input, and -- if
  bit 7 of C90Ch is set -- restarts the whole game (42F2h). Then it waits for
  C914h to exceed 0 (states 0..1) or 2 (states 2 and up): the game paces
  itself by *interrupts per frame*, which is why the missing third interrupt
  mattered so much.
- Input (4A8Ch): the keyboard through SNSMAT -- row 8 for space and the
  arrows, rotated into bit 4 -- and the joystick through RDPSG register 14,
  ORed together. The ISR edge-detects into C90Bh (rising edges only: holding
  the key gives exactly one edge), and the main loop consumes C90Bh into
  C907h once per iteration.
- The press handler (416Ch) acts by state: in state 2 it writes C900h <- 4 --
  start the game -- *no matter how early in state 2*. In other states it
  writes 2: exit to the logo sequence.

What that means for "space does not start":

- The title's load runs in the main thread, and while it runs the main loop
  is not consuming keys: a press then is a rising edge that either dies or
  sits latched until the load ends. The load ends racing the title's own
  timeout (the substate timer started when state 2 began, load included).
- On our machine the natural title (reached by letting the attract run) is
  responsive from about two seconds after the logo appears until the timeout
  -- about four seconds, measured: presses at f13850 and f13950 both start
  the game, a press at f13700 dies. The reference has the same shape.
- Pressing twice quickly during the demo starts the game 81 frames after the
  demo exits -- C900h goes 2 then 4 during the Konami logo -- because state 2
  accepts the start immediately. A game started that way comes up over
  whatever the demo left in memory, which is the "random level" report.
- After a demo-exit press, our load converts 40% more data than the
  reference's (17,632 iterations of the converter at AFAAh against at most
  12,608), which is why our press-initiated title has no responsive window
  at all: the timeout expires before the load ends. Whether that is extra
  work or a different entry phase is the open question.

## A handler that never returns owns the machine

The one bug under all of it, read from the source and fixed in the machine:
Space Manbow's press handler is *in the interrupt*. At 415Bh it checks this
frame's rising edges, and on a press it loads the next state from C945h,
writes C900h, **resets the stack pointer to F0F0h and jumps into the main
loop at 4357h** -- the interrupt never returns, and the interrupted main
thread is simply gone. The soft restart at 42F2h has the same shape.

deliver assumed handlers return: it saved the machine, ran the hook, and
restored. For a hijacking handler the restore *resurrects the dead thread*
over memory the game has already moved on from -- a white screen while the
resurrected load grinds on, a status panel built from half-changed state, a
game that ends when the fire button is pressed. Every one of those reports
was this.

Three parts to the fix, each forced by a measurement:

- **The tell is the stack.** A returning handler rets through its sentinel
  and never pops above the interrupt frame; `ld sp,F0F0h` sends SP above it
  in one instruction. Testing the program counter instead confused "chose
  not to return" with "was cut short", and a handler stopped at its first
  instruction by the frame's cycle limit got treated as a hijack.
- **Handlers run outside the frame budget** in the mid-frame delivery path
  too, so an interrupt near the end of a busy frame stops being an
  interrupt that never happened.
- **The unwind must abort the in-flight instruction.** The clock ticks
  partway through instructions, so the deliver runs with an opcode
  half-executed above it; letting that opcode finish against the handler's
  state executed the dead thread's tail on the new thread's registers and
  sent the machine into data at 363Ah. The hijack panics; every interpreter
  loop catches it at its instruction boundary and re-checks its own stack
  mark, cascading the unwind out of any nesting to the context that now
  owns the machine.

After it: a press at the naturally-reached title starts the game the same
frame and stays in gameplay; a press at the Konami logo -- the mid-load
abort-start -- comes up as a clean 212-line game with its status panel; and
firing during play no longer ends the game, because that was the same
resurrection. King's Valley, Salamander and the disk keep their digests to
the byte: no verified cartridge ever hijacks, so nothing else moved.

## The invulnerable ship is the reference's behaviour, reproduced

The report: the player ship cannot be destroyed. Established, with the same
counters on both machines:

- The collision engine runs constantly and works. Over twelve thousand frames
  of random flight the object-box scan at 7470h performs 19,115 tests and the
  deep handlers mark thousands of overlaps; hit records are written (75C4h,
  766Dh, 741Ah all fire).
- The ship-damage event is never raised. The game's damage entry is event 2
  through the jump at 4103h: the dispatcher at 4492h sends it to 44B3h, which
  subtracts 2 from the power bar at CB08h and sets the death flag at CB07h
  when the bar is empty. **4492h executes zero times** -- in played games, in
  the attract demo, on our machine and on openMSX with C-BIOS, over minutes
  of enemy contact and terrain scraping.
- openMSX + C-BIOS is invulnerable too: taps to start, then 4.5 minutes
  parked in enemy paths and pushed into walls -- no death, no game over, and
  collision counters identical to ours (7345h x37 vs our x39, 741Ah x0 both).

So the machine reproduces its reference exactly, and the fault -- if it is
one -- lives in what C-BIOS's environment lacks against a real BIOS. The
openMSX trainer confirms the mechanism the game intends: invincibility is
forced by holding CA53h/CA54h at 3, lives are CB0Fh, the power bar CB08h.
On real hardware hits drain the bar; here the chain from "hit recorded" to
"event raised" never completes its last step, and the consumer of those
records is the piece still unread.

A real MSX2 BIOS ROM in openMSX would separate "C-BIOS gap" from "game
design" in one run, and diffing the two references would name the exact
byte the game needs.

## The invulnerable ship: the ROM dump is trained

Followed to the bottom, as read from the source:

- The death verdict is at 440Fh: the frame poll returns "ship died" when the
  ship object's byte at CA40h reads zero. The consequence chain -- sequencer
  state 3, lives minus one at 6350h, death transition, respawn -- **works**:
  poking CA40h to zero mid-game runs the whole cascade correctly, lives 2 to
  1, clean respawn.
- The routine that begins a death is at 809Dh in the ship's bank: it sets the
  dying state, arms the explosion animation, and plays the explosion sound.
  **Nothing in the 256KB image references it.** No call, no jump, no vector
  entry, no jump-table slot, no computed address -- an exhaustive byte scan
  finds the address pair only twice, both inside sound-envelope data.
- The invulnerability reproduces identically on our machine, on openMSX with
  C-BIOS, and on openMSX with a real MSX2 BIOS -- machine environment ruled
  out three ways.
- The dump's SHA-1 (ad1081f398ac...) is in openMSX's software database as an
  unannotated alternate dump, not the verified original (GoodMSX2 RC-768,
  f6199f48ff99...). The built-in debug byte at 4032h is FFh -- retail -- so
  the tamper is not the debug flag; it is the missing invocation.

Conclusion: this copy of the game was trained -- the one instruction that
invokes the ship's death was patched out, decades ago, by whoever cracked the
cartridge. Every machine plays it invulnerably because the invulnerability is
in the bytes. The fix is a verified dump; diffing one against this copy will
name the exact patched bytes.

Also settled on the way: with a real MSX2 BIOS in openMSX the game's start
behaves properly, so the start flakiness earlier in this file was the C-BIOS
environment, as suspected -- and the disk drive ROMs the user supplied open
the way to running the disk titles under a real BIOS too.

## The machine now boots like a real BIOS, and the start is immediate

With real MSX2 main and sub ROMs available, the boot state could finally be
read at the *right moment*: a breakpoint on the cartridge's INIT, which is
the only sampling point that is neither the BIOS mid-setup nor the cartridge's
own work. Three corrections against what had been copied from C-BIOS:

- A cartridge enters INIT looking at **SCREEN 0** -- the text screen the boot
  messages were printed on -- so registers 0-7 are 00 60 06 80 00 36 07 04,
  not the SCREEN 1 set. RG0SAV matches.
- The slot register reads **F4h**: pages two and three are RAM, and the
  cartridge pages its own upper half in itself. D4h, with page two already
  claimed for the cartridge, was a fiction no real machine shows.
- A 50Hz machine says so consistently: locale 91h/11h, register 9 bit 1, and
  RG8SAV's second byte. msxrun grew -hz to run either way.

The payoff was larger than expected: with this boot state, the second press
after a demo-exit fires the ISR-immediate start at frame 1502 -- the frame
after the press -- where it used to sit latched until the load ended at 1640.
The start now behaves at 60Hz the way the real 50Hz machine demonstrated.
King's Valley, Salamander and the disk keep their VRAM, RAM and PSG digests
untouched.

## A halt is not an idle loop

The FRS Fix & Enhanced edition (the fan-repaired release, whose ship dies on
WebMSX, strengthening the trained-dump verdict on the other copy) locked at
boot on this machine while running fine on C-BIOS. Traced with a
last-instructions ring: its INIT reaches a `halt`, and the machine treated
the first halt as "INIT settled into its idle loop" -- classified the
cartridge handler-shaped and froze its main thread at the halt for ever.

On hardware a halt waits for the next interrupt and *continues*. That is a
third cartridge shape: not an INIT that returns, not a `jr $` idle, but a
halt-driven loop. The interpreter no longer marks halt as idle; the frame
engines clear it and resume; and INIT that halts gets the interrupt it is
waiting for and carries on, until it idles for real or the runaway declares
its loop main-thread-shaped.

An INIT that halts *repeatedly* is not setting up: the halt loop is its main
loop, and it is classified main-thread after a handful of halts, so the frame
engine feeds it an interrupt every frame with no cap. Riding the boot-time
interrupt path instead ran into that path's 256-delivery guard partway
through the logo reveal -- one strip per interrupt -- which is why the Konami
logo stopped two strips in.

With both, the FRS edition boots and draws its whole logo. Every digest holds --
King's Valley, Salamander, the disk -- and the retail dump's immediate start
is untouched. The FRS init also reads the MSX version byte, probes turbo R's
CHGCPU at 0180h, and installs a RAM ISR stub that accumulates VDP status
flags across reads: a modern checklist of what a faithful machine must have.

## Two stop flags, one cleared

The FRS edition still locked in a *generated module* after booting fine under
msxrun -- the two front ends disagreeing, which is the one thing this project
is not allowed to have.

The cause is a trap the code already documents. `noLabel`, where translated
code hands control to the interpreter, clears `idle` first, because Interpret
stops the moment it sees that flag: hand it an entry point with a stale one
and it returns without running a single instruction. Making halt a *separate*
stop flag added a second one, and only run_stub.go cleared it. A generated
module routes every hand-off through noLabel, so the first halt set a flag
nothing cleared and every later entry returned immediately. msxrun enters
Interpret directly and never sees that path, which is exactly why it worked.

Both flags are now cleared at every point that hands the interpreter an entry
point -- noLabel, bootRun, runEntry, run_stub. With that, interpreting the
cartridge and running its translation give byte-identical VRAM, RAM and PSG
over thirty frame-digests, and the generated module draws the logo and runs
the intro the way msxrun does.

A note on method: the first three readings after this fix said "still stuck",
and they were stale -- the generated binary had failed to build, the test
re-read the previous PNG, and nothing in the output said so. Deleting the
artefact before each run and checking it exists afterwards is the difference
between a measurement and a guess.

## A halted processor stops fetching, not counting

Gameplay flashed garbage every third frame and the status panel looked frozen.
The register log said why: normal frames make two splits -- line 1 sets the
panel's page and scroll, line 78 the playfield's -- and every third frame had
only the first, so the whole screen drew in the panel's state.

The interrupt trace, with the cycle numbers printed rather than guessed, gave
the cause: the split was armed for line 75 and the frame *ended at line 52*.
Frame 2602 ran 11,831 cycles where a frame is 59,659.

FRS's main loop is halt-driven, and teaching Interpret to stop on a halt made
the frame stop with it. On hardware a halt stops the processor fetching; it
does not stop the clock, the raster, or the interrupt that is on its way. So
a halted main thread now lets time pass in the four-cycle steps a halt costs,
until the frame's budget runs out or an interrupt arrives -- and taking one
un-halts the processor, which is said in deliver, after the state restore that
would otherwise put the halt back.

Two splits every frame again, the flashing gone, the panel updating. Every
digest holds, and the generated module and msxrun agree byte-for-byte over
thirty frame-digests.

Also settled by this ROM: the ship dies. The original dump's death-starter at
809Dh really is unreachable code -- it was trained.

## Eight sprites to a line

The chip draws eight sprites on any one scanline and no more: the ninth and
beyond are not drawn at all, and the overflow is reported in status register
0. This machine drew every sprite that covered the line.

Space Manbow keeps all thirty-two sprites live and lands as many as
twenty-three of them on a single line, in two attribute tables it alternates
between every other frame -- and the same object sits at a *different slot* in
each table (slots 10 to 13 in one, 18 and 19 in the other). Slot decides
colour, priority, and which sprites survive the limit, so the extras this
machine drew changed all three every time the tables flipped.

The limit is now enforced where the line's sprite list is gathered, which is
also where the hardware decides it. King's Valley, Salamander and the disk are
untouched: they are MSX1 machines and never reach this path.
