# The M1 wait state

An MSX does not run a Z80 at Z80 speed. The VDP and the CPU share the memory
bus, and the machine inserts **one wait state into every M1 cycle** — every
opcode fetch. A real MSX Z80 is therefore about 15% slower than the timings in
a Z80 data sheet. msx2go charges it, as of the commit this document was
rewritten in; what follows is the measurement it rests on and what it moved.

## Measured, not assumed

Snatcher's hottest loop during its intro, at 5798h:

```
5798  7E        ld a,(hl)             7 T,  1 M1
5799  CB DC     set 3,h               8 T,  2 M1
579B  B6        or (hl)               7 T,  1 M1
579C  C4 ED 57  call nz,57ED         10 T,  1 M1   (untaken)
579F  CB 9C     res 3,h               8 T,  2 M1
57A1  23        inc hl                6 T,  1 M1
57A2  7D        ld a,l                4 T,  1 M1
57A3  E6 1F     and 1Fh               7 T,  1 M1
57A5  FE 1F     cp 1Fh                7 T,  1 M1
57A7  38 EF     jr c,5798            12 T,  1 M1   (taken)
                                     ----  ----
                                     76 T, 12 M1
```

Priced on the reference machine — a breakpoint at 5798h, a hundred hits,
`machine_info time` either side — one iteration costs **90.85 cycles**.

- Raw Z80: 76.
- Raw Z80 + one wait per M1: **88**.

The remaining 2.85 is the `call nz` being taken on some passes, which costs 7
more. So the model is exactly **+1 T-state per M1 cycle**, and nothing else is
missing from the instruction timings.

Confirmed a second time, on a different instruction and by a different
method. The last byte of an `otir` writing to the VDP data port -- Snatcher
draws its text with one, at CC4Ch -- costs **18** cycles on the reference
where the raw Z80 figure is 16. `otir` is `ED`-prefixed, so two M1 cycles,
and 16 + 2 is 18.

That measurement also rules something out worth writing down: there is **no
extra VDP access-slot stall** on the data port beyond the M1 wait. A byte
written to port 98h in SCREEN 7 with the display on costs what the
instruction costs, and no more.

## How it is charged

The wait belongs to the *fetch*, so an instruction pays one for its opcode and
one more for each prefix byte:

| form | fetches |
|---|---|
| unprefixed | 1 |
| `CB`, `ED`, `DD`, `FD` | 2 |
| `DD CB`, `FD CB` | 3 |

Two places charge for an instruction, and they must agree exactly or a
translated build stops matching its interpreted twin -- the check this whole
project rests on. So neither of them adds the wait: it is folded into the
tables both read, by an `init` over `cycBase` and into `cycPrefix`, `cycED` and
`cycCB`. `cycCB` is new; the CB costs used to be written out twice, once in the
interpreter and once in `CycleCost`, and folding a wait into two copies of the
same numbers is exactly how the two drift apart.

A repeating block instruction is fetched again for every pass, so
`cycBlockRepeat` carries two waits as well.

`cycHalt` does not. A halted Z80 does keep running M1 cycles, so it probably
should -- but it was not measured, and the halt loop only pads out to the end
of a frame, so the number does not reach anything. It is left alone and said
so rather than guessed at.

## What it moved

Nothing was regenerated: no generated module is checked in, so every build
picks the new costs up.

All eight battery titles moved, which was expected -- every instruction got
dearer. None of them broke: the rendered frame at 600 is identical for King's
Valley, Space Manbow and King's Valley Plus, and 96-100% identical for the
rest, the differences being where an animated title screen had got to.

Against the reference, on the one game with a live comparison, the effect is
mixed and worth writing down honestly. Measured on Snatcher's own disk reads:

| | opening | its longest scene |
|---|---|---|
| before | 51.5s (0.85x) | 42.1s (1.08x) |
| with the M1 wait | 54.7s (0.90x) | 45.2s (1.16x) |
| and measured shims | 54.3s (0.90x) | 44.8s (1.15x) |
| *reference* | *60.6s* | *39.1s* |

The opening gets closer and the scene gets further away. The scene is paced by
interrupts, and 22% of its frames are now skipped because the work no longer
fits in one -- so making instructions dearer stretches it. That says this
machine over-charges Snatcher somewhere by about a quarter, and the M1 wait is
not the place to fix it: the wait is measured, twice, and confirmed a third
time by SNSMAT costing exactly what its nine instructions cost with the waits
added. The next thread is the VDP and screen shims, which is what a SCREEN 7
game leans on hardest.

Space Manbow is the only battery title saturated enough for instructions per
frame to mean anything, and there the wait moved it from 7918 to 7113 against
the reference's 7524 -- from 5% over to 5% under. The 5% under is the size of
the documented untaken-branch approximation, which is the other known gap in
this model.

## Why it was not Snatcher's four times

Snatcher's opening once ran about four times too fast, and this was never the
cause. The cause was `mainThreadFrame` marking the interrupt clock after the
handler returned rather than before it ran, so the handler's own `ei` let a
second interrupt land on top of the first; it ran its handler four to six times
a frame. See `internal/emit/runtime/frame_test.go`.

**An earlier version of this document argued that point from a `-cpu` table,
and that table was wrong.** It was recorded from a run with no keyboard input,
which leaves Snatcher sitting on its options screen: the numbers described a
static menu, not the opening. Any conclusion drawn from them, including "a
slower processor makes the intro shorter", is withdrawn. Measuring this game
means pressing 0 first; `-tape` does it.
