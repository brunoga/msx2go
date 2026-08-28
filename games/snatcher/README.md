# Snatcher, and where it stops

Konami, 1990, MSX2. The image to hand is a 16 MB Nextor hard disk holding
a fan English translation, and it is a *wrapper*: under SNATCHER\ the
three original 720K floppies sit as SNATCHER.001, .002 and .003, beside
SNATCHER.COM, which is what MSX-DOS runs.

The floppies can be lifted out of it and booted directly, and doing that
is what taught this machine to boot a disk that carries its own code.
None of what follows is specific to this game.

## What it took to get to the game's own code

Each measured on the reference machine first.

- **Boot sectors.** No filesystem, no BASIC loader: the disk ROM reads
  sector zero to C000h and calls C01Eh. It calls it **twice**, and the
  carry flag says which time -- clear first, set second, and only the
  second one is the boot. Call it once and the disk looks dead.
- **The disk ROM's jump table.** DSKIO and its neighbours, for a game
  that reads raw sectors because there is no filesystem to read files
  from.
- **An honest slot scan.** The game reads the two signature bytes at
  4000h and the eight pointers after them, in every slot, to find the
  disk ROM -- and then calls DSKIO in the slot it found. A disk machine
  has no cartridge, so the slot a cartridge would be in answers as the
  disk ROM now instead of as whatever the loader left in RAM.
- **CHPUT**, and the DOS console calls on top of it. No cartridge has
  ever needed it; a disk program prints before it draws.
- **CHGMOD** actually setting the screen, rather than assuming the
  program will set the chip up itself.
- **FAFCh and RAMAD0-3.** Work-area bytes the real BIOS maintains: the
  screen-and-memory byte the game reads before touching the chip at all,
  and the four that say which slot RAM is in. Left at zero, the game
  refuses to run and then asks for RAM from slot zero -- the BIOS -- and
  puts it where its own code should have been.
- **The memory mapper.** Ports FCh-FFh and 128K in eight segments. This
  was the gate: with it, the game reaches SCREEN 7 with the same VDP
  registers the reference machine has.

## The floppy stops on purpose

At 82D1h, on this machine and on the reference machine alike. The
instruction there is `jr $` -- a deliberate hang. Before it the game
prints its banner and a line telling the reader to boot from disk 0.

That is not a machine that failed. The reference machine, with the real
ROMs, does exactly the same thing, and still does with 512K of RAM, so
it is not a memory check either. **The floppy is not a bootable game
disk.** It is the hard-disk version's data disk, and its boot sector is
a guard that says so and stops. The game is started by SNATCHER.COM
under MSX-DOS, which serves those images to it.

So running this image means supporting the hard disk it came on. That is
what the rest of this is.

## The hard disk

MSX-DOS's own boot is not emulated. AUTOEXEC.BAT is four lines and a
vocabulary of three words, so it is interpreted, and the program it
names is loaded where the kernel loads one and started the way the
kernel starts one -- what the machine needs is the state the loader
leaves behind, not the loader. Subdirectories came with it: a hard disk
keeps its files in them, and a search that only ever read the root
answered "no such file" for every file this game keeps beside itself.

Then the hand-over. A program stops being a disk program through the
disk ROM's last jump-table entry, 4022h, which starts the disk system
and goes on into BASIC's initialisation; partway through laying out its
stack BASIC calls the stack-end hook at FEDAh, and that hook is where a
program takes the machine. The loader finds the slot to call by reading
a hook the disk ROM installed and keeping the slot byte out of it.

## The two that took the longest

**One segment can be in two pages.** The disk operating system's way of
putting code in page one is to select a segment in page *two*, load into
it there, and then select the same segment in page one to run it. Both
windows are the same RAM. This machine keeps one flat memory and allowed
a segment into one page at a time, so the second selection copied a
stale store over page one and the game found sixteen kilobytes of zeroes
where its loader should have been. Everything downstream -- the call
into empty RAM, the sweep through two pages of nops, the initialisation
restarting every thirty-two frames, the graphics store repainted flat
before any artwork could survive in it -- was that one rule.

**An interrupt missed is not an interrupt owed.** The opening runs
inside the game's own interrupt handler: it enables interrupts, clears a
counter the handler increments, and spins until the counter reads
sixty-four. The clock here kept a debt -- an interrupt that came due
while the processor could not take it advanced the schedule by one frame
rather than to the present -- so a machine that had been busy paid it
back as fast as the check runs, one every few thousand cycles instead of
one every sixty thousand. The counter stepped from sixty-three to
sixty-five between the game's load and its compare, and being an
emulator it did so at the same phase every time: sixty-four went past
seven times in twenty frames and the spin missed it seven times out of
seven.

## How they were found

Not by reading memory. Each of those was chased backwards through data
for a long time and neither gave way: every answer was correct and
produced a new question one level up.

What found them was putting the two machines' instruction streams side
by side from a common point. `-trailfrom` holds a trace back until a
chosen address runs -- the hand-over, here -- and `-trailout` writes it;
the same trace comes out of openMSX as a debugger condition. They diff
cleanly once block instructions are collapsed, because the hardware
trace reports every repeat of an LDIR at one address and this machine
reports the instruction once. Five hundred and eleven lines of a single
address are a copy, not a divergence.

The oracle for all of it is openMSX with `-ext SunriseIDE_Nextor -hda`,
which needs the Nextor interface ROM. Attach the image read-only or work
from a copy: the game creates its save file on first run, and an
emulator given the real image will write to it.

## The sound cartridge

Snatcher shipped with one -- Konami's RA-004, an SCC+ -- and its options
screen has lines for `Audio` and `Slot`. Neither is a choice. The game
probes, and the lines report what it found: with no cartridge, `PSG` and
`-`; with a plain SCC, `SCC` and the slot; with the RA-004, `SCC+` and the
slot. Measured on the reference three times, changing only the extension.

The chip this machine already had was a feature of a *mapper*: bank 3Fh in
page two turns a Konami cartridge's ROM into registers, so the game that
has one is the game that carries it. A disk game carries nothing.
`internal/emit/runtime/sndcart.go` is the other way round -- a cartridge
that is nothing but the chip, in the one slot this machine leaves empty.
`-sndcart scc` or `-sndcart scc+`, on msxrun and on the window alike.

Three things the game's probe insists on, each of which had to be got right
before it would admit the cartridge existed:

- **Only slot two is free.** Zero is the BIOS, one is the disk ROM the game
  finds DSKIO through, three is RAM. A cartridge in slot one does not fail
  loudly; it just stops booting. So that is refused.
- **Banking a window away hides the registers rather than wiping them.** The
  probe writes a byte, banks the window away, writes again where it used to
  be, banks back, and expects the first byte still to be there.
- **The mode register is in neither window.** It sits at the top of the
  cartridge and answers whatever bank is selected -- it has to, since it is
  what opens the SCC-I's window in the first place. The game's very first
  write to the cartridge is `BFFF <- 20`.

The probe never writes port A8h itself. On the reference the `out (A8),a`
happens inside the BIOS's stub in RAM at F380h, reached through WRSLT and
ENASLT, and those are shimmed here -- so it is the shims that had to learn
about the cartridge, not just the port.

## One byte, and a whole screen

The options screen was wrong for a long time in a way that read as
corruption: the logo two pixels to the left, and every character of text
carrying a sliver of its neighbour.

It was not the rasteriser. Rendering the *reference machine's* own video
memory through it gives a clean, correctly placed picture, so what was wrong
was what this machine wrote. And nothing wrote it through the data port --
nor through any BIOS call. The artwork arrives in **two HMMC transfers
before the first frame is over**, and an HMMC's bytes go in through register
44, which is why watching the data port saw nothing, and why a window that
opens at a frame number sees nothing either. `-vwall` and `-regall` count
from power-on for that reason.

The rule is that a transfer into video memory takes its **first** byte from
register 44, which the program loaded *before* it wrote the command. This
machine waited for the next write instead, and so ran one byte behind for
the whole rectangle. The text was mangled rather than merely shifted because
the game blits it as 8x10 cells out of a font strip that was itself two
pixels out of register: every cell picked up two columns of the next glyph
and lost two of its own.

Two counts say the fix is right and not merely better. Snatcher feeds
exactly 10800 bytes, which is 142x40 and 256x20 -- both rectangles to the
byte. Breaker, which uses LMMC rather than HMMC, writes register 44 with no
transfer running exactly as many times as it issues other commands, so it
has no surplus byte either. Breaker's frame digests move, because its load
path changes; that is the fix rather than a regression.

## Where it gets to

Boots through MSX-DOS, renders its options menu in SCREEN 7, takes the
keypress, hands the machine over, plays the intro with music, prompts
for disk 2 and loads it, and reaches JUNKER HQ with its command menu
waiting for input. The title screen is 99.83% of the visible page
byte-identical to the reference machine's, the same 3741 white bytes to
the byte.

Run it as the 50Hz machine the reference is -- `-hz 50`, which is also what
stops the game running a fifth too fast -- and **the whole visible screen is
byte-identical to the reference's**: eighty-nine lines of picture, not one of
them differing. What is left over sits below line 212, off the screen.

Three of the registers behind that were the BIOS's rather than the game's,
and each was wrong here in a way nothing showed until a screen was compared
whole:

- **A bitmap screen is 212 lines.** Register 9's LN bit, which the reference
  sets the moment the game asks for SCREEN 7 and this machine never set. A
  192-line window on a 212-line picture loses twenty lines off the bottom.
- **Register 7 is the border colour**, taken from BDRCLR -- not the
  foreground-and-background pair it is in a text mode. This machine wrote
  zero and got a black border where the reference has blue.
- **Registers 3 and 4 are a colour table a bitmap screen has not got**, and
  the real BIOS leaves them exactly where the previous screen put them. They
  are the one thing still differing, because the console before them is 80
  columns on the reference and is not here; nothing reads them in SCREEN 7.

The frequency bit in register 9 is the machine's own and is left alone, which
is what `-hz 50` sets. Breaker gains by the same three rules: nine of its ten
low registers now match the reference where seven did.

## The opening, and one interrupt a frame

The opening ran about four times too fast: the typing, the music and the
scene changes all together. It was not a cycle cost, and it was not this
game's -- **the handler was being entered four to six times a frame, nested
three deep.**

`mainThreadFrame` marked the interrupt clock after the handler returned rather
than before it ran. So for the whole of the handler's run, the mark still held
the previous frame's value -- by then a full budget in the past, because a
handler that costs more than a frame is ordinary. The handler's own `ei` was
all `dueIRQ` needed to deliver a second interrupt on top of the first, and
another every budget after that. `runFrame`, the frame engine for cartridges,
has always set the mark before running the handler; this is the same rule for
the shape a disk game boots into.

Measured against the reference by the game's own disk reads, which are
landmarks nothing else can fake -- thirty-eight of them, identical in content
and order on both machines:

| | ours before | ours after | reference |
|---|---|---|---|
| longest scene | 7.8s | 37.5s | 39.1s |

Within 4%, at `-hz 50`. What is left is the reads themselves: a block read
costs this machine almost nothing and costs a real SunriseIDE a quarter of a
second, so every loading pause is still shorter here than on the reference.

**How not to measure this game.** With no keyboard input it stops on the
options screen and stays there for ever, playing the menu's music -- which
looks enough like the opening to be mistaken for it. A whole round of
measurements was taken that way and every one of them was of a static menu.
Press 0 first: `-tape` will.
