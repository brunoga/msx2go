# The BIOS this machine provides

A cartridge calls the BIOS through a jump table in page zero. This machine
has no BIOS ROM in it: the entry points are implemented here, in Go, against
the published register conventions. Nothing is translated or copied from a
ROM -- the real ROMs are useful as an *oracle*, to read a value off a running
machine when the documentation does not say what a cartridge will find, and
that is all they are used for.

## The table

The jump table is a three-byte grid from 0000h. Counted from real ROMs, it
runs to:

| machine | entries | last entry |
|---------|---------|------------|
| MSX1    | 86      | 0159h      |
| MSX2    | 96      | 0177h      |
| MSX2+   | 98      | 017Dh      |

An address on that grid that this machine has not implemented is a routine
still to write. An address *off* the grid -- D1h, say -- is not an entry point
at all, and a program that reaches one has run off into data. The two are
reported differently, because the fix is different.

That grid is also how a real bug was found: GTSTCK and GTTRIG were a slot
low, so a cartridge asking the cursor keys which way to walk was told whether
the trigger was down.

## What is implemented

- **Chip and video memory**: WRTVDP, RDVRM, WRTVRM, SETRD, SETWRT, FILVRM,
  LDIRMV, LDIRVM, CHGMOD, CHGCLR, DISSCR, ENASCR, and the MSX2 additions that
  carry a full 128K address -- BIGFIL, NSETRD, NSTWRT, NRDVRM, NWRVRM.
- **Screens**: INITXT, INIT32, INIGRP, INIMLT, SETTXT, SETT32, SETGRP,
  SETMLT, CLS, POSIT, TOTEXT.
- **Sprites**: CLRSPR, CALPAT, CALATR, GSPSIZ.
- **Input**: GTSTCK, GTTRIG, GTPAD, GTPDL, SNSMAT, CHSNS, CHGET, KILBUF,
  BREAKX, ISCNTC, CKCNTC.
- **Sound**: GICINI, WRTPSG, RDPSG, BEEP, STRTMS, INITIO.
- **Slots**: RDSLT, WRSLT, ENASLT, CALSLT.
- **The rest**: DCOMPR, ISFLIO, OUTDLP, FORMAT, and the cassette routines,
  which report failure rather than pretending there is a tape.

## How the screen routines work

The real BIOS does not hold the table addresses as constants. It keeps them
in the work area -- one block of five words per screen mode, at F3B3h, F3BDh,
F3C7h and F3D1h -- and its setup routines program the chip from whichever
block the mode calls for. A cartridge that moves a table and then asks for the
screen gets its own layout back.

This machine does the same, and seeds those words at boot with what a real
MSX2 BIOS holds when a cartridge's INIT is reached, read off one at a
breakpoint there.

## What happens to an entry nobody has written

With `-fussy`, it stops and says so: while a new cartridge is being taught,
that report is worth more than a game that limps. Otherwise it says so once
and returns a failure the caller can read -- carry set, A zero -- so the game
runs with something missing rather than not at all.

## The command engine

The V9938 draws through registers 32 to 46: a rectangle, an operation, and a
write to register 46 to start it. All of it is implemented -- the fills and
copies (LMMV, LMMM, HMMV, HMMM, YMMM), the single-pixel ones (PSET, POINT,
LINE), the search (SRCH), the abort (STOP), and the three that move a
rectangle between the processor and video memory a byte at a time (LMMC,
HMMC, LMCM).

Those last three are how a game pushes graphics out of its ROM and into video
memory, and they do not run all at once: the command sets the rectangle up,
and each write to register 44 -- or each read of status register 7 -- moves it
on by one unit until the rectangle is full. A machine that answered "busy"
while one was running would stall the very loop that feeds it.

## How a cartridge takes over

There are four shapes, not one, and this machine now runs all four:

1. **INIT settles into an idle loop.** It set the machine up, installed an
   interrupt hook, and parks. Everything after that happens in the handler.
   King's Valley and Salamander.
2. **INIT returns.** Same arrangement, different ending -- the BIOS it returns
   to would idle. Requiring the idle loop turned this into "may not be a game".
3. **INIT never finishes: the game loop *is* INIT**, with the handler only
   ticking counters underneath. Space Manbow.
4. **INIT installs the hook at FEDAh and returns.** That hook is the BIOS's
   own, called once it has finished setting the machine up, so a cartridge
   that wants to start *after* the BIOS rather than in the middle of it puts a
   restart, its slot and its address there and gets out of the way. King's
   Valley II.

The fourth needs two other things to work: the restart at 0030h, CALLF, which
takes the slot and address inline after the call; and a restart that pushes
its return address before the shim runs, because CALLF reads its arguments
from that address and steps it past them.

## An interrupt happens whether the cartridge wants it or not

The BIOS has an interrupt handler of its own, and it runs whether a cartridge
has hooked it or not: it reads status register 0 -- which clears the
vertical-blank flag -- and gets on with the keyboard and the clock. A machine
that only delivers interrupts when a cartridge hook exists is saving them up.

Saving them up has a sharp edge. A hook appears the instant a cartridge writes
the jump byte, and the address it jumps to is written *one instruction later*.
King's Valley II does exactly that, and the vertical blank that had been
waiting through the whole of its INIT went straight into a jump whose
destination was still the hook table's filler. It walked through cleared RAM
until it hit a restart.

So an interrupt with no cartridge hook is now taken by the machine and thrown
away, rather than held. King's Valley, Salamander, the disk and Space Manbow
keep their digests through the change; King's Valley II boots.

## Slots

Every page is mapped at once here, so the slots are a fiction. It has to be a
consistent one: a cartridge that goes looking for itself -- reading a
signature byte out of each slot in turn -- must find itself once, in the slot
the machine claims it is in, and find nothing in the others. Told that every
slot holds the cartridge, King's Valley II cannot decide which is its own.

RDSLT answers FFh for an address a slot does not hold, and ENASLT records the
page's slot in the register, because page zero's BIOS is told from page
zero's RAM by reading it back.

## The sub-ROM

An MSX2 keeps the screens above 3 in a second ROM. A program reaches them
through EXTROM at 015Fh or SUBROM at 015Ch, with the routine's address in IX.
Implemented so far: CHGMOD at 00D1h, which sets up screens 0 to 8, and the
palette routines INIPLT at 0141h and SETPLT at 014Dh.

The bitmap screens are set from the table addresses the BIOS uses for page 0 --
the picture at the bottom of memory, the sprite attributes and patterns above
it, and for screens 7 and 8 those tables in the second 64K, which is what
register 11 is for. Answering "not implemented" instead leaves the chip in
whatever mode it was in while the program fills memory with a picture, which
is a garbled screen.

Its entries are **four** bytes each -- an EI and a jump -- not the three the
main ROM uses, so they fall at 0115h, 0119h, ... 013Dh, 0141h and so on.
Getting that wrong puts every identification one slot out.

Each was identified from the real sub-ROM by decoding what its entry jumps to
and seeing what the routine touches, rather than from memory: 013Dh reads the
screen mode, points at a table and calls a block writer, which is the palette
being set to its defaults; 0115h reads BAKCLR, which is the screen colours
being taken from the work area. Implemented so far: CHGMOD (00D1h), CHGCLR
(0115h), INIPLT (013Dh), RSTPLT (0141h), GETPLT (0145h) and SETPLT (0149h).

Anything else the sub-ROM is asked for says so once, naming the address in IX,
and returns failure.

## The sub-ROM's whole table

78 entries, four bytes apart, from 0089h to 01F9h. Each was surveyed by
decoding what its entry jumps to and recording which ports and work-area
variables the routine touches — evidence rather than recollection. The groups
that fall out:

| entries | touches | what that makes them |
|---------|---------|----------------------|
| 0091h–00C1h | F92Ah, F92Ch | the maths pack: one pair of variables, no chip |
| 00D9h–00F1h | port 98h, F3BDh–F3D9h | the screen-table setup, one block per mode |
| 0109h–010Dh, 0129h | port 98h | video-memory access |
| 0131h, 013Dh, 0145h–014Dh | ports 99h/9Ah | the palette |
| 0115h, 0119h | F3EAh, FCAFh | the screen colours |
| 018Dh–01A1h | F55Eh–F575h, FCAFh, port 99h | the graphics primitives |

That last group is where Breaker's remaining call lives. It asks for **019Dh**
with a pointer to F562h, which is inside the graphics coordinate block that
018Dh, 0191h and 01A1h also work through — so it is one of the drawing
routines, not a palette or screen call.

It is deliberately not implemented. A probe cartridge that calls it on a real
machine, in screen 5, with the same registers Breaker passes, **does not
return** — so the routine needs state that probe does not set up, and writing
a version from a guess would be a wrong answer with no symptom. The next step
is to watch the real thing: openMSX with a real BIOS *and a disk drive*, which
these ROMs are one file short of assembling.

## The principle

Implement all of it. Every game that fails does so on the one entry point
nobody wrote, and the only way to stop meeting them one at a time is to stop
waiting for a game to ask.
