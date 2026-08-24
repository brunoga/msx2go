# Castle Excellent

ASCII, 1986. 32 KB, flat, MSX1. The Japanese original of *Castle
Excellent*; the game loop is INIT itself, so its main thread runs
interpreted and bridges into translated code at every call.

    msx2go -rom "Castle Excellent (ASCII, 1986).rom" -out ./castle

It converted and ran on the first attempt -- the picture, the demo and
the sound were right straight away, which for a title nobody had aimed
at is the machine model earning its keep. What it did expose was a bug
in the interpreter-to-translation bridge, because it is the first
cartridge whose main thread is the game.

## The bridge must not cross during boot

A main-thread cartridge's INIT never returns: the boot runaway watches
it, decides the shape, and asks the interpreter to hand back at an
instruction boundary. That request is a flag the interpreter checks at
the top of its loop -- and translated code has no such loop. So a call
bridged out of INIT entered the game's own loop translated and never
came back: twenty-nine seconds of machine time inside frame one, ending
with the machine run off into RAM.

Two rules now. The bridge does not cross while the machine is booting,
because boot is the interpreter's own dance -- the runaway detector,
the halt promotion, the hand-back -- and none of it can reach inside a
translated routine. And a bridged run honours every reason the
interpreter would have stopped, the hand-back request included, not
just the frame's budget.

## What is exact, and what is not

Given the same starting state, a bridged routine is exact: 160 crossings
sampled across two hundred frames produced byte-identical registers,
memory, video memory *and* cycle count whether run translated or
interpreted.

What differs is where the frame boundary lands. The interpreter stops on
the instruction that reaches the frame's cycle budget; a bridged routine
can only hand back at a `ret`, because its position is Go control flow
rather than a program counter, so it finishes the routine it is in. The
one sampled crossing that differed had been entered with 427 cycles of
budget left and ran 2,636 past it -- the interpreter stopped mid-routine,
the translation finished it. Neither is wrong; they are at different
points.

Across 2,400 frames that granularity is worth about 1.5% -- 182.0
million cycles against the interpreter's 179.3 -- so the game runs very
slightly fast and the demo drifts a few frames out of step. The picture,
the score and the level are the same. Making it exact means paying the
overshoot back out of the next frame's budget, which is a change to the
timing model every main-thread title shares, and it wants the reference
machine as its arbiter rather than internal consistency alone.
