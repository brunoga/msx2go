# Salamander

Konami, 1987. 128 KB, konami-scc, MSX1.

    msx2go -rom salamander.rom -out /tmp/sal8 -sweep 40000 -monkeys 6

No configuration, no arguments, four and a half minutes: 12,857 addresses
observed executing, 32,855 instructions translated, 82.4% of the image kept as
data. The module runs 24,000 frames with no fall-back to the interpreter, and
its title screen is pixel-identical to openMSX with C-BIOS (`ref/verify.sh
salamander.rom /tmp/sal8 salamander 1600`).

`sites.txt` is that sweep's answer, kept so the module can be rebuilt without
running one. It is not an input anyone has to write -- `-sweep` produces it.

## Fixed: the title screen drew with the wrong graphics

Two bugs, both in the BIOS shims, both invisible to King's Valley because it
calls none of the routines involved.

**The block routines were in the wrong order.** FILVRM is 0056h and LDIRVM
005Ch, and this package had them swapped, with CHGMOD where LDIRMV belongs.
Salamander asks 0056h to clear all 16K of video RAM before drawing its title
and got a copy from address zero instead, so the clear never happened.

**LDIRVM left the wrong thing in HL.** C-BIOS's implementation at 0297h ends
with `ex de,hl`, so it returns with HL still the *destination* it was given and
DE past the source. Salamander's font uploader at 4CC0h depends on exactly
that: it calls LDIRVM once per Graphics 2 third and steps to the next with `add
hl,0800h` on the way round. Advancing HL the way an LDIR would sent the second
and third passes to C261h -- 0261h once the VDP masks it to fourteen bits --
which is the colour table, so every glyph on the title screen came out wrong.
With it right the three destinations are 2080h, 2880h, 3080h.

The lesson is about coverage rather than the BIOS: a shim no cartridge in the
test set calls is a shim nobody has checked. What settled both was reading
C-BIOS at 026Dh, 0281h and 0297h rather than reasoning about what the routines
ought to do.

## Finding it: a frame-accurate comparison

`ref/vframes.sh` writes one digest per interrupt on the reference machine, and
`msxrun -vcrc` writes the same on ours, so the first frame at which the two
part company is a lookup rather than a screenshot hunt. Two things had to be
right for it to mean anything:

- **Count interrupts by slot, not by signature.** `ref.tcl` counts an interrupt
  only when 4000h reads `AB`; Salamander pages banks into page 1, so interrupts
  arriving under another bank went uncounted and the ordinals ran behind real
  frames. `ref/vframes.tcl` conditions on `get_selected_slot 1` instead, which
  no amount of bank switching changes.
- **Digest the name table, not all of video memory.** Two machines can show the
  same picture with different bytes in the parts of VRAM nothing displays --
  unused tile patterns, sprite entries past the last active one. Hashing all of
  it reported a difference on 644 of 650 checkpoints while the screens were
  identical; hashing what is on screen found the real split immediately.


## Speed, measured

The port ticks at the same rate as the hardware, so far as four independent
measurements can tell:

| | |
|---|---|
| window's actual tick rate | 59.93-60.18 per second |
| reference's hook runs per emulated second | 59.92, and the same in every segment of 13,000 frames -- it never drops one |
| the game's own frame counter at E23Ah after 3,000 interrupts | ours C3h, reference C1h |
| scene progression over 13,000 frames | about 1% ahead |

So neither machine loses an interrupt to its own workload, and the cartridge
advances the same amount per interrupt on both.

If it still feels fast, the thing to try is `-hz 50`. A European MSX runs at
50.16 Hz, which is a sixth slower than the Japanese machine this cartridge was
made for; a memory of one is a real reason for 60 Hz to feel quick. The flag
sets the tick rate and the locale byte at 002Bh together, because a game that
asks -- Salamander asks thirteen times -- must be told the same thing the
interrupts are actually doing.

## Speed: the handler overruns its frame, and that is the point

Salamander's gameplay ran three times too fast, and `-hz 50` barely helped
because the interrupt rate was never the problem.

The reference settles it. Breakpoint the hook and watch the cartridge's own
re-entry guard at E205h (`ref/isrcost.sh`):

    enter 11001 186.104579 guard=0     <- full pass starts
    guard 11001 186.108204 <- 1
    enter 11002 186.121271 guard=1     <- arrives mid-pass, bails
    enter 11003 186.137958 guard=1     <- bails
    guard 11003 186.153583 <- 0        <- pass ends, 2.9 frames later
    enter 11004 186.154646 guard=0     <- next full pass

The handler takes 2.9 to 3.3 frames of work, the interrupts that arrive while
it runs hit the guard and return, and the game advances once every three or
four frames. That is not the game misbehaving, it is what it was tuned around.
A machine with no cycle counting finishes every handler instantly, never
overruns, never trips the guard, and runs the game at the full frame rate.

So the machine counts cycles now (`runtime/cycles.go`), and every translated
instruction charges what it costs through `m.Tick`.

The part that took two tries: it is not enough to *skip* a frame whose budget
is spent. The sound driver sits at the top of the handler, above the guard, so
on the hardware it keeps running once a frame while the game's logic advances
once per three. Skipping the handler slows the music down with it -- which is
exactly what it sounded like. The interrupt has to be delivered *into* the
running handler, which is what `m.Tick` is for: it is the one place a
translated program can notice that time has passed.

With that right, and with no scaling factor at all:

| | rate against the reference (1.00 = correct) |
|---|---|
| title and text screens | 1.00 |
| gameplay | 0.91 |

Against 0.30 before. The cycle table was never the problem -- the handler's
prologue measured 11,682-15,180 cycles here against the reference's 12,975.
The model was.

### The knobs

`-cpu` scales the cycles a frame is allowed: 1 is a stock MSX, higher stops the
handler overrunning at all (smooth, and faster), negative turns the accounting
off. `-speed` scales the wall clock. `-hz` picks 50 or 60 and tells the
cartridge the same thing at 002Bh.

### Open

King's Valley's output changes when the accounting is on: nine of its first
3,000 frames go over budget, the heaviest by 24x, which are level loads. That
is probably more faithful than the old behaviour and it is *not verified*,
which matters because the whole King's Valley engine was checked against a
machine that had no clock. It needs a screen check against the reference
before the accounting can be trusted for it.

## Input: the keyboard, not just the joystick

"PRESS F5 TO CONTINUE" did nothing, because the harness only ever offered six
bits -- four directions and two triggers -- which is all King's Valley reads.

Salamander reads row 7 of the keyboard matrix at 15F9h and rotates bit 1 into
carry, and bit 1 of row 7 is F5. So the machine now describes the whole matrix
(`runtime/input.go`) and the harness maps host keys onto it: the function keys,
ESC, RETURN, TAB, STOP, HOME, INS, DEL, and the letters and digits.

The other half of the same gap: the PPI at A8h-ABh was not implemented at all,
so a cartridge that reads the keyboard directly rather than through SNSMAT --
Space Manbow reads port A9h -- saw "no key pressed" forever. Port A is the slot
register, port B reads the row port C selected, and ABh is the 8255's bit
set/reset, which is how a row gets selected one bit at a time.
