#!/bin/bash
# Headless reference run: a cartridge under C-BIOS in openMSX, one work-RAM
# digest per ISR entry, written to $2. Usage: refrun.sh rom out frames [dump]
#
# openMSX is not assumed installed: setup.sh extracts the Debian packages
# into ~/omsx without root. C-BIOS is a free BIOS and runs Konami cartridges.
set -e
ROM="$(realpath "$1")"; export REF_OUT="$2"; export REF_FRAMES="$3"
[ -n "$4" ] && export REF_DUMP="$4"
export LD_LIBRARY_PATH=$HOME/omsx/root/usr/lib/x86_64-linux-gnu
HERE="$(dirname "$(realpath "$0")")"
cd $HOME/omsx
( printf '<openmsx-control>\n'
  printf '<command>set throttle off</command>\n'
  printf "<command>source $HERE/ref.tcl</command>\n"
  printf '<command>set power on</command>\n'
  while [ ! -f "$REF_OUT.done" ]; do sleep 1; done
  printf '<command>exit</command>\n'; sleep 1 ) | \
timeout 900 local/bin/openmsx -control stdio -machine REF_MSX1 -carta "$ROM" 2>&1 | grep -v '^<' || true
