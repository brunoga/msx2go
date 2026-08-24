#!/bin/bash
# The screen check: does the translated cartridge show the reference's
# picture? Usage: verify.sh <rom> <gamedir> <harness-name> <ref-frame> [isr guard]
#
# The reference's VRAM and registers at one frame are compared against a
# window of the translation's frames, both rendered by the same rasteriser.
# The window absorbs pace: a machine with no cycle time reaches the same
# scenes a few frames apart from one that drops ticks under load.
set -e
ROM="$1"; DIR="$2"; NAME="$3"; N="$4"
export REF_ISR="${5:-0x4069}" REF_GUARD="${6:-0xe205}"
HERE="$(dirname "$(realpath "$0")")"
rm -f /tmp/refv.txt*
REF_DUMP=$N "$HERE/refrun.sh" "$ROM" /tmp/refv.txt $((N+2)) $N >/dev/null 2>&1
( cd "$DIR" && go build -tags msxdata -o /tmp/refv-game ./cmd/$NAME )
FROM=$((N>60 ? N-60 : 1))
/tmp/refv-game -frames $((N+60)) -vramspan $FROM:$((N+60)):/tmp/refv.span >/dev/null
( cd "$HERE/.." && go run ./cmd/vramcmp /tmp/refv.txt.vram /tmp/refv.span /tmp/refv.png )
