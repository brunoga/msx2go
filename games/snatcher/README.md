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

## Where it stops, and why that is the end of this road

At 82D1h, on this machine and on the reference machine alike. The
instruction there is `jr $` -- a deliberate hang. Before it the game
prints its banner and a line telling the reader to boot from disk 0.

That is not a machine that failed. The reference machine, with the real
ROMs, does exactly the same thing, and still does with 512K of RAM
instead of 128K, so it is not a memory check either. **The floppy is not
a bootable game disk.** It is the hard-disk version's data disk, and its
boot sector is a guard that says so and stops. The game is started by
SNATCHER.COM under MSX-DOS, which serves those images to it.

So running this image means supporting the hard disk it came on:
MSX-DOS 2 or Nextor, subdirectories, and a .COM program -- not more BIOS
shims. That is a different and much larger piece of work than anything
here, and it is the honest boundary. A pristine floppy dump of the
Japanese original would be a different question and might well run.
