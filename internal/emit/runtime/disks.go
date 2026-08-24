package z80

// More than one floppy.
//
// A machine has drives and a player has disks, and the two are not the same
// thing. A three-disk game on a one-drive machine says "insert disk 2" and
// waits for somebody to do it; a two-drive machine has two disks in at once
// and never asks. Both are the same small idea: a set of images, and which
// image is in which drive.
//
// Everything the disk calls do goes through the image in the *selected*
// drive, which is drive A until DOS is told otherwise. Disk holds it, so the
// call sites that had one floppy still read the one they mean.

// AddDisk gives the machine another image, and puts it in drive A if that
// drive is empty. The first image a machine is given is the one it boots
// from; the rest wait to be swapped in.
func (m *M) AddDisk(d *Disk) int {
	m.images = append(m.images, d)
	n := len(m.images) - 1
	if len(m.inDrive) == 0 {
		m.inDrive = []int{-1}
	}
	if m.inDrive[0] < 0 {
		m.Insert(0, n)
	}
	return n
}

// Images is how many floppies the machine was given.
func (m *M) Images() int { return len(m.images) }

// ImageName is what to call image n in a prompt: its volume label if it has
// one, and otherwise its number.
func (m *M) ImageName(n int) string {
	if n < 0 || n >= len(m.images) {
		return "no disk"
	}
	if l := m.images[n].Label(); l != "" {
		return l
	}
	return "disk " + string(rune('1'+n))
}

// Drives is how many drives the machine has: one unless something asked for
// more by inserting into a higher one.
func (m *M) Drives() int {
	if len(m.inDrive) == 0 {
		return 1
	}
	return len(m.inDrive)
}

// Insert puts an image in a drive, which is what swapping a floppy is. It
// reports whether the drive and image both exist.
func (m *M) Insert(drive, image int) bool {
	if drive < 0 || image < -1 || image >= len(m.images) {
		return false
	}
	for len(m.inDrive) <= drive {
		m.inDrive = append(m.inDrive, -1)
	}
	if m.inDrive[drive] != image {
		// Whatever was open on the floppy coming out is finished
		// with: write back anything it changed while it still has a
		// disk to be written to. Its own disk, not the new one.
		m.flushOpenFiles()
		m.diskSwapped = true
	}
	m.inDrive[drive] = image
	m.syncDisk()
	return true
}

// SwapNext puts the next image in a drive and reports which one, which is a
// hotkey's worth of "insert the next disk". With one image it is a no-op.
func (m *M) SwapNext(drive int) int {
	if len(m.images) < 2 || drive < 0 || drive >= len(m.inDrive) {
		return m.Mounted(drive)
	}
	next := (m.inDrive[drive] + 1) % len(m.images)
	m.Insert(drive, next)
	return next
}

// Mounted is the image in a drive, or -1 for an empty one.
func (m *M) Mounted(drive int) int {
	if drive < 0 || drive >= len(m.inDrive) {
		return -1
	}
	return m.inDrive[drive]
}

// DriveDisk is the floppy in a drive, or nil if there is none.
func (m *M) DriveDisk(drive int) *Disk {
	if n := m.Mounted(drive); n >= 0 {
		return m.images[n]
	}
	return nil
}

// SelectDrive is BDOS's "select drive": the one the calls that do not name
// one will use. It reports whether that drive has a floppy in it.
func (m *M) SelectDrive(drive int) bool {
	if drive < 0 || drive >= len(m.inDrive) || m.inDrive[drive] < 0 {
		return false
	}
	m.curDrive = drive
	m.syncDisk()
	return true
}

// CurrentDrive is the drive the disk calls act on, counted from zero.
func (m *M) CurrentDrive() int { return m.curDrive }

// syncDisk keeps Disk pointing at the selected drive's floppy, because that
// is what every disk call means by "the disk".
func (m *M) syncDisk() {
	if d := m.DriveDisk(m.curDrive); d != nil {
		m.Disk = d
	}
}

// fcbDisk is the floppy a file control block names. Its first byte is zero
// for the selected drive, one for A, two for B; a drive with nothing in it
// falls back to the selected one, because a program that asks for a floppy
// that is not there gets an error from the call, not a nil machine.
func (m *M) fcbDisk(fcb uint16) *Disk {
	if n := int(m.Mem[fcb+fcbDrive]); n > 0 {
		if d := m.DriveDisk(n - 1); d != nil {
			return d
		}
	}
	return m.Disk
}

// flushOpenFiles writes back every file the disk calls have open, each to
// the floppy it was opened on, and forgets them. A swap is the end of
// whatever was being read from the disk that is coming out.
func (m *M) flushOpenFiles() {
	for fcb, f := range m.files {
		m.dosFlush(f)
		delete(m.files, fcb)
	}
}
