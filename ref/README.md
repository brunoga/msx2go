# The reference machine

openMSX with C-BIOS, driven headless over its stdio control channel, so a
translated cartridge can be compared against a real emulation of the same
image -- which is how model bugs are found rather than guessed at.

    ./setup.sh                                # once: extracts to ~/omsx, no root
    ./refrun.sh game.rom /tmp/ref.txt 2000    # one digest line per game ISR

The Tcl script breakpoints the cartridge's own interrupt handler -- found by
address, conditioned on the cartridge's header being visible in page 1,
because C-BIOS is a 32K ROM whose internals cross the same addresses -- and
digests the work RAM at every entry. Frame 1 is the first ISR that arrives
with the game's init finished; the earlier ones land mid-INIT, which is real
(the BIOS enables interrupts inside its own calls) and is the behaviour
msx2go's Boot now reproduces at its shim yield points.

What it settled for Salamander, and the reason this exists: the generated
machine's first post-boot frame now matches the reference byte for byte over
the whole work RAM except the ISR counters and the state-machine pace -- the
reference's slow path overruns real frames and drops ticks, which is why the
game guards it; a machine with no cycle time never drops any. That pace
difference is legal by the game's own design, and it is the honest boundary
of a translation with no cycle counting.
