# How long the cartridge's interrupt handler actually takes.
#
# A machine with no cycle counting cannot know, and it is the number that
# decides whether a game whose handler overruns a frame runs at the speed it
# was tuned for. So ask the reference: note the emulated time at each entry to
# the hook, and at each write to the handler's own re-entry guard. Entry to
# guard-cleared is one pass of real work.
proc done {} { set f [open "$::env(REF_OUT).done" "w"] ; close $f }
set out [open $::env(REF_OUT) "w"]
set n 0
set booted 0
set isr 0x4069
if {[info exists ::env(REF_ISR)]} { set isr $::env(REF_ISR) }
set guard 0xe205
if {[info exists ::env(REF_GUARD)]} { set guard $::env(REF_GUARD) }
set from $::env(REF_FROM)
set upto $::env(REF_FRAMES)

debug set_bp $isr {[expr {[lindex [get_selected_slot 1] 0] == 1}]} {
    global out n booted from upto
    if {!$booted} {
        if {[debug read memory $::guard] != 0} { return }
        set booted 1
    }
    incr n
    if {$n >= $from && $n <= $upto} {
        puts $out [format "enter %d %.6f guard=%d" $n [machine_info time] \
            [debug read memory $::guard]]
        flush $out
    }
    if {$n > $upto} { close $out ; done }
}

debug set_watchpoint write_mem $guard {} {
    global out n from upto
    if {$n >= $from && $n <= $upto} {
        puts $out [format "guard %d %.6f <- %d" $n [machine_info time] \
            $::wp_last_value]
        flush $out
    }
}
