# Breaker, on a disk

Radarsoft, 1987. A 720K double-sided floppy, MSX2, SCREEN 8 — the first
bitmap-mode disk game, and the title that forced most of the machine's MSX2
story into existence. Convert it with:

    msx2go -dsk breaker.dsk -run BREAKER.BAS -out ./breaker

`-run` matters: the disk carries two BASIC programs and no AUTOEXEC.BAS.

## What it needed, in the order it demanded it

Each of these was measured on the reference machine (openMSX with the real
Philips VG-8235 ROMs) before it was implemented; the details live in the
commit messages.

- **The BASIC loader runs `BLOAD ...,R` inline.** Breaker's loader BLOADs a
  title program mid-script that returns, then loads two VRAM pages, then
  BLOADs the game, which never returns. Deferring the `,R` to the end of
  the program runs the wrong one, last.
- **`SET PAGE` and ACPAGE.** `BLOAD ...,S` loads into the active page, and
  every BIOS video routine offsets by it.
- **RDABS counts sectors in H** (drive in L). Read from B, the register
  holds garbage: 196 sectors land on a 64K address space and erase the
  game behind its own loader.
- **Screens 7 and 8 interleave the VRAM banks.** Bit 0 of the logical
  address picks the 64K bank, decided at access time. Breaker loads its
  sheets under screen 5's linear view and reads them back under screen 8's
  interleaved one; without the mapping the title builds perfect geometry
  out of garbage pixels.
- **The whole-address video entries (NSETRD/NSTWRT and friends)** carry a
  full 16-bit address plus the active page into register 14; the 16K-era
  entries do not.
- **A character set at CGTABL.** The game renders all of its text by
  reading the BIOS font through the pointer at 0004h itself. The machine
  provides C-BIOS's glyphs (BSD-licensed) at 1BBFh.
- **Sub-ROM graphics entries**: 0191h (rectangle copy), 0195h (copy from a
  RAM block — it streams the whole rectangle its source names; the block's
  second word is *not* a byte count), 019Dh (`COPY "file" TO`), NVBXLN and
  NVBXFL (boxes; the menu cursor is invisible without them), CHGMOD for
  the bitmap screens with the measured register-0 mode bits, the 212-line
  clear to colour zero, and the palette's shadow copy in VRAM.
- **Screen 8 has no palette anywhere.** A pixel byte is its own colour;
  register 7's border is a colour byte, not an index; sprites use the
  V9938's fixed sixteen (GRB nibbles — the paddle is cyan, colour 13).
- **CHGET delivers a press once.** The real BIOS buffers a keypress as one
  character; handing the held key back on every poll races the menu.

## Verification

The boot-to-game VRAM state matches the reference byte-for-byte except the
boot logo's debris below line 212, which a machine without the logo does
not have. The attract sequence, demo, menus and gameplay were compared
screen-by-screen against openMSX with the real ROMs.

The floppy conversion also carries the disk-translation proof: three learn
rounds translate ~5,400 instructions of the interrupt-driven half, and the
translated build's frame digests are identical to the interpreter-only
twin across 3,000 frames.
