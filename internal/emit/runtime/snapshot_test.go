package z80

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
)

// TestSnapshotRoundTrip fills a machine with values, writes it down, reads it
// back into a fresh one and checks they agree everywhere the snapshot claims
// to cover.
func TestSnapshotRoundTrip(t *testing.T) {
	a := New(make([]byte, 0x8000), Flat(0x4000, 0x8000))
	r := rand.New(rand.NewSource(1))
	for i := range a.Mem {
		a.Mem[i] = byte(r.Intn(256))
	}
	for i := range a.VDP.VRAM {
		a.VDP.VRAM[i] = byte(r.Intn(256))
	}
	for i := range a.VDP.Reg {
		a.VDP.Reg[i] = byte(r.Intn(256))
	}
	for i := range a.Keys {
		a.Keys[i] = byte(r.Intn(256))
	}
	for i := range a.PSG.Reg {
		a.PSG.Reg[i] = byte(r.Intn(256))
	}
	a.A, a.B, a.C, a.D, a.E, a.H, a.L = 1, 2, 3, 4, 5, 6, 7
	a.A2, a.B2, a.C2, a.D2, a.E2, a.H2, a.L2 = 8, 9, 10, 11, 12, 13, 14
	a.Fs, a.Fz, a.Fh, a.Fp, a.Fn, a.Fc = true, false, true, false, true, false
	a.Fs2, a.Fz2, a.Fh2, a.Fp2, a.Fn2, a.Fc2 = false, true, false, true, false, true
	a.IX, a.IY, a.SP, a.PC = 0x1234, 0x5678, 0x9ABC, 0xDEF0
	a.IFF, a.halted, a.idle, a.booting, a.inISR = true, true, false, true, false
	a.IM, a.frames, a.bootIRQs = 2, 4242, 11
	a.Cyc, a.lastIRQ, a.credit, a.irqTaken = 999999, 888888, -1234, 3
	a.rReg, a.PrimarySlot, a.ppiC = 0x55, 0xAA, 0x0F
	a.PSG.Latch, a.PSG.PortA = 7, 0xBE
	a.VDP.addr, a.VDP.first, a.VDP.status = 0x2000, 0x40, 0x80
	a.VDP.readAhead, a.VDP.latched, a.VDP.Frame = 0x33, true, 4242
	a.VDP.BootVblank, a.SCC.Active, a.SCC.Enable = true, true, 0x1F
	for i := range a.SCC.Wave {
		for j := range a.SCC.Wave[i] {
			a.SCC.Wave[i][j] = int8(r.Intn(256) - 128)
		}
	}
	for i := range a.SCC.Freq {
		a.SCC.Freq[i], a.SCC.Vol[i], a.SCC.pos[i] = r.Intn(4096), byte(i), i
	}

	var buf bytes.Buffer
	if err := a.SaveState(&buf, "cartridge-id"); err != nil {
		t.Fatal(err)
	}

	b := New(make([]byte, 0x8000), Flat(0x4000, 0x8000))
	if err := b.LoadState(bytes.NewReader(buf.Bytes()), "cartridge-id"); err != nil {
		t.Fatal(err)
	}

	// Compare through walk itself, so the test covers exactly what the
	// snapshot claims to and cannot drift from it.
	var wantBuf, gotBuf bytes.Buffer
	if err := a.SaveState(&wantBuf, "x"); err != nil {
		t.Fatal(err)
	}
	if err := b.SaveState(&gotBuf, "x"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wantBuf.Bytes(), gotBuf.Bytes()) {
		t.Error("a machine restored from a snapshot does not save the same " +
			"snapshot back")
	}
}

// TestSnapshotRejectsAnotherCartridge: a snapshot carries the SHA-1 of the
// image it was taken from, because restoring one game's state into another is
// a machine in a state its code cannot account for.
func TestSnapshotRejectsAnotherCartridge(t *testing.T) {
	a := New(make([]byte, 0x8000), Flat(0x4000, 0x8000))
	var buf bytes.Buffer
	if err := a.SaveState(&buf, "one"); err != nil {
		t.Fatal(err)
	}
	if err := a.LoadState(bytes.NewReader(buf.Bytes()), "two"); err == nil {
		t.Error("a snapshot of another cartridge was accepted")
	}
}

// TestSnapshotCoversTheMachine fails when a field is added to M, because the
// most likely snapshot bug by far is a new piece of state nobody remembered to
// write down -- and it would show up as a game that resumes *almost* right,
// which is the worst way for it to show up.
//
// If this fails, decide which kind the new field is and then update the count.
// M.walk saves everything that survives a frame and carries the game's state.
// What it deliberately leaves out:
//
//   - the image, the mapper and the pruning holes, which are the cartridge
//     rather than the machine and are the same on both sides of a restore;
//   - the hooks (Trace, Observe, OnBank, BiosTrace, WatchWrites, OnRunaway),
//     which belong to whoever is watching, not to the game, and LearnSites
//     with its learned map, which are that watching's notebook;
//   - the discovery bookkeeping, which is a build's notes about itself;
//   - Hz, CPUScale and Fussy, which are settings the person running it chose,
//     so that a snapshot can be resumed at a different speed on purpose;
//   - nest, frameStart, cycLimit, bootStop and interpDepth, which are zero
//     between frames by construction;
//     lastDeliver, fhDue, fhHeld, fDue, fHeld and frameOrigin, which only
//     anything while a frame or INIT is mid-flight; and
//     Executed, which is a diagnostic counter; and
//   - Disk, which is the image the machine booted from, not state it
//     changed. Its written sectors are saved beside it, not in a snapshot,
//     and dma, files, searchFor and searchAt go with it -- and so does
//     images, the floppies the machine was handed. Which of them is in
//     which drive *is* state, so inDrive and curDrive are saved.
func TestSnapshotCoversTheMachine(t *testing.T) {
	const known = 101
	if n := reflect.TypeOf(M{}).NumField(); n != known {
		t.Errorf("M has %d fields, this test was written against %d. "+
			"A new one may need adding to walk in snapshot.go.", n, known)
	}
}
