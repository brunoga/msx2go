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

## Where it gets to

Boots through MSX-DOS, renders its options menu in SCREEN 7, takes the
keypress, hands the machine over, plays the intro with music, prompts
for disk 2 and loads it, and reaches JUNKER HQ with its command menu
waiting for input. The title screen is 99.83% of the visible page
byte-identical to the reference machine's, the same 3741 white bytes to
the byte.
