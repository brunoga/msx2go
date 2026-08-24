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

## And the music was missing a voice

Reported by ear, found by counting. Channel A is this driver's
envelope-driven voice, and over three thousand frames it contributed
33,464 audible samples where B contributed 3.1 million.

The synthesiser is fed writes rather than the register file, because a
write is an event: register 13 is the envelope shape and writing it
restarts the envelope, so pushing the file every frame would retrigger
it sixty times a second. But it has to *start* somewhere, and the driver
sets the envelope period once during initialisation and never again --
so a synthesiser built after boot never learned it, read the period as
one, and decayed every note to silence in two milliseconds. It now
adopts the chip's registers at first sound, and again whenever a
snapshot is restored, since a restored machine has register state and no
write history at all.

Underneath that, the envelope ran sixteen times too slow: its step clock
is half the tone generator's, so a step is EP*16 clocks rather than the
EP*256 this synthesiser was counting. The bass held at full volume
instead of decaying. openMSX's AY8910 says the same and doubles its
period register for the same reason.

Neither shows up in a digest, which hashes the register file -- the
synthesiser reads that file rather than writing it. Audio has to be
measured on its own terms: audible samples per channel, and the write
counts that reveal a register the driver only ever sets at boot.
