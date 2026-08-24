# What the BIOS's own interrupt routine costs before the cartridge's hook runs,
# and what a bailed-out hook costs. Both are cycles this machine charges
# nothing for.
proc done {} { set f [open "$::env(REF_OUT).done" "w"] ; close $f }
set out [open $::env(REF_OUT) "w"]
set n 0
set booted 0
set from $::env(REF_FROM)
set upto $::env(REF_FRAMES)
debug set_bp 0x0038 {} {
    global out n from upto
    if {$n >= $from && $n <= $upto} {
        puts $out [format "irq %d %.6f" $n [machine_info time]] ; flush $out
    }
}
debug set_bp 0x4069 {[expr {[lindex [get_selected_slot 1] 0] == 1}]} {
    global out n booted from upto
    if {!$booted} {
        if {[debug read memory 0xe205] != 0} { return }
        set booted 1
    }
    incr n
    if {$n >= $from && $n <= $upto} {
        puts $out [format "hook %d %.6f guard=%d" $n [machine_info time] \
            [debug read memory 0xe205]] ; flush $out
    }
    if {$n > $upto} { close $out ; done }
}
