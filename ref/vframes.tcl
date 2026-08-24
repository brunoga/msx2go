# One video-memory digest every REF_EVERY interrupts, for REF_FRAMES of them.
#
# ref.tcl counts an interrupt only when 4000h reads `AB`, so that C-BIOS's own
# code crossing the hook address is not mistaken for the cartridge's. That
# condition is wrong for a mapper that pages banks into page 1: Salamander
# does, so interrupts arriving under another bank go uncounted and its ordinals
# run behind real frames, which makes "our frame N against reference frame N"
# meaningless.
#
# The honest test is which slot page 1 reads from, which no amount of bank
# switching changes. (`after frame` would be better still, but this rig runs
# with no renderer, so there are no frames for it to fire on.)
proc done {} { set f [open "$::env(REF_OUT).done" "w"] ; close $f }
set out [open $::env(REF_OUT) "w"]
set n 0
set booted 0
set isr 0x4069
if {[info exists ::env(REF_ISR)]} { set isr $::env(REF_ISR) }
set guard 0xe205
if {[info exists ::env(REF_GUARD)]} { set guard $::env(REF_GUARD) }

debug set_bp $isr {[expr {[lindex [get_selected_slot 1] 0] == 1}]} {
    global out n booted
    # The first interrupts land mid-INIT: the real BIOS enables interrupts
    # inside its own calls, and the game's guard byte exists so those early
    # ISRs skip the slow path. Frame 1 is the first with INIT finished.
    if {!$booted} {
        if {[debug read memory $::guard] != 0} { return }
        set booted 1
    }
    incr n
    if {$n % $::env(REF_EVERY) == 0} {
        # The name table, not all of video memory. Two machines can show the
        # same picture with different bytes in the parts of VRAM nothing
        # displays -- unused tile patterns, sprite entries past the last
        # active one -- and hashing those reports a difference every frame
        # while the screens are identical. What is on screen is the name
        # table, and its base is wherever R2 says.
        set base [expr {([debug read "VDP regs" 2] & 0x0F) << 10}]
        set regs ""
        for {set r 0} {$r < 8} {incr r} {
            append regs [binary format c [debug read "VDP regs" $r]]
        }
        # The emulated clock as well as the digest: interrupts per emulated
        # second says whether the cartridge's hook really runs at the frame
        # rate, or whether its own work is costing it interrupts. A machine
        # with no cycle counting cannot lose one, so this is the number to
        # check when a port feels too fast.
        puts $out [format "%d %08x %.4f" $n \
            [zlib crc32 [debug read_block VRAM $base 768]$regs] \
            [machine_info time]]
        flush $out
    }
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
