# One digest line per ISR entry, indexed by ISR ordinal -- the game's own
# counter resets per scene, but the ordinal is monotonic on any machine.
#
# The digest covers the work RAM at c000-efff and the bank shadows at f0f1
# onward. The stack page f000-f0f0 is left out: the return addresses on it
# depend on who called the interrupt hook, which is C-BIOS there and a
# sentinel here, and both are outside the game.
proc done {} {
    set f [open "$::env(REF_OUT).done" "w"]
    close $f
}
set out [open $::env(REF_OUT) "w"]
set n 0
set booted 0
# Only the cartridge's own ISR: C-BIOS is a 32K ROM whose internals also
# cross PC=4069, so the breakpoint is conditioned on the cart -- its AB
# header -- being what page 1 shows.
set isr 0x4069
if {[info exists ::env(REF_ISR)]} { set isr $::env(REF_ISR) }
set guard 0xe205
if {[info exists ::env(REF_GUARD)]} { set guard $::env(REF_GUARD) }
# Only when the cartridge is what page 1 shows: its AB signature. C-BIOS is
# a 32K ROM whose own code crosses these addresses, and its 4000h is not AB.
debug set_bp $isr {[expr {[debug read memory 0x4000] == 0x41 && [debug read memory 0x4001] == 0x42}]} {
    global out n booted
    # The first interrupts land mid-INIT: the real BIOS enables interrupts
    # inside its own calls, and the game's e205 guard exists exactly so
    # those early ISRs skip the slow path. Frame 1 is the first ISR that
    # arrives with INIT finished.
    if {!$booted} {
        if {[debug read memory $guard] != 0} { return }
        set booted 1
        set initisrs [debug read memory 0xe23a]
        puts $out [format "# mid-init isrs: %d" $initisrs]
    }
    incr n
    set ram [debug read_block memory 0xc000 0x223a]
    append ram [debug read_block memory 0xe23b 0xdc5]
    append ram [debug read_block memory 0xf0f1 6]
    puts $out [format "%d %08x" $n [zlib crc32 $ram]]
    if {[info exists ::env(REF_DUMP)] && $n == $::env(REF_DUMP)} {
        set f [open "$::env(REF_OUT).ram" "wb"]
        fconfigure $f -translation binary
        puts -nonewline $f [debug read_block memory 0xc000 0x4000]
        close $f
        set f [open "$::env(REF_OUT).vram" "wb"]
        fconfigure $f -translation binary
        puts -nonewline $f [debug read_block VRAM 0 16384]
        set regs ""
        for {set r 0} {$r < 8} {incr r} {
            append regs [binary format c [debug read "VDP regs" $r]]
        }
        puts -nonewline $f $regs
        close $f
    }
    if {$n >= $::env(REF_FRAMES)} { close $out ; done }
}
