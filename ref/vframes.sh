#!/bin/bash
# Video-memory digests on a real frame clock.
# Usage: vframes.sh <rom> <out> <frames> [every]
set -e
ROM="$(realpath "$1")"; export REF_OUT="$(realpath -m "$2")"
export REF_FRAMES="$3"; export REF_EVERY="${4:-20}"
[ -n "$5" ] && export REF_DUMP="$5"
export LD_LIBRARY_PATH=$HOME/omsx/root/usr/lib/x86_64-linux-gnu
HERE="$(dirname "$(realpath "$0")")"
rm -f "$REF_OUT" "$REF_OUT.done"
cd $HOME/omsx
( printf '<openmsx-control>\n'
  printf '<command>set throttle off</command>\n'
  printf "<command>source $HERE/vframes.tcl</command>\n"
  printf '<command>set power on</command>\n'
  for i in $(seq 1 1800); do [ -f "$REF_OUT.done" ] && break; sleep 1; done
  printf '<command>exit</command>\n'; sleep 1 ) | \
timeout 2400 local/bin/openmsx -control stdio -machine REF_MSX1 -carta "$ROM" 2>&1 | grep -v '^<' || true
