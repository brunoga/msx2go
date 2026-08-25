# The M1 wait state

An MSX does not run a Z80 at Z80 speed. The VDP and the CPU share the memory
bus, and the machine inserts **one wait state into every M1 cycle** — every
opcode fetch. A real MSX Z80 is therefore about 15% slower than the timings in
a Z80 data sheet, and msx2go charges the data-sheet numbers.

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

## What has to change

M1 cycles per instruction are not in any table today; they follow from the
prefix, so no new table is needed:

| form | M1 cycles |
|---|---|
| unprefixed | 1 |
| `CB`, `ED`, `DD`, `FD` | 2 |
| `DD CB`, `FD CB` | 3 |

Two places charge for an instruction, and both must agree or the translated
build stops matching the interpreted one — which is the check the whole
project rests on:

- `internal/emit/runtime/interp.go` — `m.tick(uint32(cycBase[op]))` at the top
  of `step`, `m.tick(cycPrefix)` for a prefix byte, and `m.tick(uint32(cycED(op)))`
  in the ED path.
- `internal/emit/runtime/cycles.go` — `CycleCost(op, sub)`, which the emitter
  bakes into generated code (`internal/emit/file.go`, `banked.go`, `chunked.go`).

The tidiest change is to fold the wait into `cycBase`, `cycED` and `cycPrefix`
themselves rather than adding it at every call site: one wait for the base
opcode, one more for a prefix. Then neither charge point changes at all, and
there is no way for the two to drift apart.

**A generated module carries the old costs.** `CycleCost` is evaluated at
build time and written into the generated Go, so every existing module keeps
whatever it was built with until it is regenerated. Any comparison of a
translated build against its interpreted twin has to use a module built after
the change.

## What it will disturb

Every game's frame digests, because every instruction gets dearer. That is the
point — but it means the battery cannot be rebaselined by simply accepting the
new numbers.

Each of the eight titles has to be checked **against the reference**, not
against its own previous digest:

- `breaker`, `castleexcellent`, `kingsvalleyplus`, `salamander`,
  `spacemanbow`, `spacemanbow-frs`, `kingsvalley`, `kv2`.

The games with a documented cycle-sensitive behaviour are the ones to look at
first, because they are the ones a 15% change can visibly break:

- **Salamander** — its handler is meant to overrun and eat the next interrupt;
  its gameplay speed is the tell.
- **Space Manbow** — its ISR polls S#1 for the raster and its splits are timed.
- **Breaker** — the interrupt-driven half, whose translated and interpreted
  digests must stay identical to each other.

Expect handlers that currently fit inside a frame to stop fitting. That is
the hardware's behaviour, and several of these games were tuned for it, so
some digests should move *toward* the reference rather than away.

## Why it is not the four times

Snatcher's intro runs about four times too fast, and this is not the cause:
15% is not 300%. It is worth fixing on its own terms — it is a real property
of the machine, it is measured, and it makes every cycle comparison against
the reference honest — but it should not be expected to fix that, and it
should not be committed as though it had.
