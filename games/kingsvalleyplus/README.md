# King's Valley Plus, on a disk

A 320K single-sided floppy with more levels than the cartridge and a level
editor. It is the first thing msx2go has run that is not a cartridge, and
almost everything it needed is general to disks rather than to this one.

## What a disk boots like

There is no cartridge header and no INIT. Disk BASIC boots, runs AUTOEXEC.BAS,
and that program loads the machine code and jumps to it:

    AUTOEXEC.BAS  POKE-1,... : LOAD"kvalleyp.bas",R
    KVALLEYP.BAS  1 CLS:KEY OFF:COLOR15,1,1:SCREEN2,2,0
                  2 BLOAD"kvalleyp.001",S      16K straight into video memory
                  3 FOR I=0 TO 1500:NEXT I
                 10 FOR T=0 TO 2000: VPOKE T,0: NEXT
                 40 BLOAD"king.usr",R          8000h-CED7h, run at 801Fh

So booting a disk is running that much BASIC and no more. `diskboot.go`
interprets the tokenised program directly -- there is no BASIC ROM here and
there is not going to be one -- and covers the loader vocabulary only. A
statement outside it stops the boot with the statement named, because a loader
that quietly skips a line hands the game a machine set up differently from
what the program asked, and that surfaces later looking like a translation
bug.

From the BLOAD that ends the loader, a disk program is a cartridge that
happens to live in RAM: `runEntry` is shared with Boot, so the same idle-loop
and main-thread machinery decides its shape.

## What a disk program calls

`ld c,1Ah / call F37Dh` is the third instruction King's Valley Plus runs. That
is the CP/M-shaped function call every MSX disk program uses: a function
number in C, a file control block in DE. MSX-DOS puts the entry at 0005h and
Disk BASIC at F37Dh in the work area, and `dos.go` answers both against the
mounted image -- open, close, search, delete, sequential and random read and
write, block read and write, absolute sectors. King's Valley Plus opens DATA1
and DATA2 at startup and pulls its levels out of them with function 27h,
random block read.

Files are read and written whole rather than edited in place: the FAT is
rewritten from the finished contents, which makes a half-updated directory
impossible. That is what the level editor's saving will go through.

## The hook table has to hold a `ret`

The first version got as far as running and drew nothing. The interrupt
handler ran two instructions and jumped to H.TIMI at FD9Fh, and slid through
six hundred nops and out of the work area, once a frame, for ever.

Every one of the BIOS's expansion hooks is five bytes in the work area, and a
real machine fills the whole table with `ret` at power-on so that a hook
nobody has claimed returns at once. msx2go left it as zeros, which no
cartridge had ever noticed because a cartridge installs the hooks it wants and
calls none of the others. Filled with C9h from FD9Ah to FFC9h, the game draws
its first screen.

King's Valley and Salamander keep their VRAM and PSG digests across all of
this; only their RAM digest moves, and that is the hook table itself.

## Converting one

    msx2go -dsk kingsvalleyplus.dsk -out dir

reads the geometry out of the image's BIOS parameter block, lists what is on
it, boots it to find out which shape of program it is, and writes the same
module a cartridge gets: the floppy embedded, the runtime, and both harnesses.
There is no translated code in it yet -- a disk program's code arrives in RAM,
put there by the loader, so there is no image at a fixed address to trace --
and everything runs interpreted. The module and the interpreter agree exactly:
at frame 1500 their video memory, RAM and sound registers all match.

Getting that far turned up a bug that had nothing to do with disks. The
generated `Run` pushes its sentinel and dispatches; when the address is not
translated it hands over to the interpreter through `noLabel`. `Run` in
run_stub.go clears the idle flag before interpreting and the generated one
never did, so the interpreter -- which stops the moment it sees that flag --
was handed the interrupt handler's entry point every frame and returned
without running an instruction of it. It only shows on a module with nothing
translated, which until now could not exist.

## Writing

The floppy is read *and* written. A level editor saves through the same
function calls it loads through, and the harness writes the image back to
`<name>.dsk` beside the snapshot when the program has changed it, leaving the
original alone. `msxrun -dsk x.dsk -dsksave y.dsk` does the same headless.

Files are replaced whole: the chain is freed, new clusters are allocated out
of the free list, the directory entry is rewritten and every copy of the FAT
is kept in step. disk_test.go covers the round trip, replacing a file with a
shorter one, deleting, and writing the same file twenty times over to prove
the freed clusters come back.

## One control block, two overlays

The editor worked from a cold boot and gave a black frozen screen if you had
played a game first. Both overlays are loaded through the *same* file control
block -- GAME.USR and EDIT.USR at DCB9h -- and the program never closes
either, because an overlay that is about to be jumped into has no reason to.

The DOS shim remembered an open file by the address of the block it was opened
with and handed that back on the next open, without looking at the name that
was in the block by then. So choosing EDIT after a game re-read the *game's*
overlay into B92Dh and jumped into it. Now the remembered name is checked
against the one in the block, and a block reused for another file has whatever
it held flushed and forgotten first.

Reproduced before fixing and verified after, on the real game rather than in
principle: `msxrun -dsk kingsvalleyplus.dsk -monkey 1` loads GAME.USR at frame
343 and EDIT.USR at 3943, and the screen at 4100 is blank without the fix and
the editor's CHARACTER MENU with it. disk_test.go's TestFCBReusedForAnotherFile
pins it, and fails without the check.

Getting there needed the input format: `-tape` is twelve bytes a frame, one
per row of the key matrix, and MSX keys are **active low**. A tape of zeros
holds every key on the keyboard down, which walks the menus on its own.
