#!/bin/bash
# Extract openMSX and C-BIOS into ~/omsx without root, and prepare the
# zero-RAM reference machine. Run once.
set -e
mkdir -p ~/omsx && cd ~/omsx
apt-get download openmsx openmsx-data cbios libglew2.2 libsdl2-ttf-2.0-0
for d in *.deb; do dpkg -x "$d" root; done
mkdir -p local/bin
ln -sf ~/omsx/root/usr/bin/openmsx local/bin/openmsx
mkdir -p ~/.openMSX && rm -rf ~/.openMSX/share
ln -sfn ~/omsx/root/usr/share/openmsx ~/.openMSX/share
python3 - <<'PY'
s=open('root/usr/share/openmsx/machines/C-BIOS_MSX1_JP.xml').read()
s=s.replace('''      <RAM id="Main RAM">
        <mem base="0x0000" size="0x10000"/>
      </RAM>''','''      <RAM id="Main RAM">
        <initialContent encoding="hex">00</initialContent>
        <mem base="0x0000" size="0x10000"/>
      </RAM>''')
open('root/usr/share/openmsx/machines/REF_MSX1.xml','w').write(s)
PY
echo "ok: ~/omsx ready"
