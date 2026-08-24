package z80

// Booting a hard disk.
//
// A floppy game starts one of two ways: a BASIC loader, or its own boot
// sector. A hard disk starts a third way, because there is an operating
// system on it -- MSX-DOS loads, runs its command interpreter, and the
// interpreter runs AUTOEXEC.BAT, which ends by running a program.
//
// None of that is emulated. The same argument that applies to the BASIC
// loader applies here: what the machine needs is the *state the loader
// leaves behind*, not the loader itself. So the batch file is interpreted
// -- it is four lines and a vocabulary of three words -- and the program it
// names is loaded where MSX-DOS loads a program and started the way MSX-DOS
// starts one. The disk function calls it then makes are the ones already
// shimmed at F37Dh, which is where MSX-DOS's own entry at 0005h lands.
//
// What is supported is that vocabulary and nothing beyond it, for the same
// reason the BASIC loader supports only its own: a line quietly skipped is a
// machine set up differently from the way the disk asked, and the failure
// surfaces later looking like something else.

import (
	"fmt"
	"strings"
)

// comLoad is where MSX-DOS loads a program, and comStack where it leaves
// the stack: the top of the transient program area, below the kernel's own
// data.
const (
	comLoad  = 0x0100
	comStack = 0xF380
	comDMA   = 0x0080
)

// bootHardDisk starts a disk that has an operating system on it.
func (m *M) bootHardDisk(d *Disk) error {
	m.cwd = rootDir
	bat, ok := d.ReadAt(rootDir, "AUTOEXEC.BAT")
	if !ok {
		return fmt.Errorf("z80: this hard disk has no AUTOEXEC.BAT, so " +
			"there is nothing that says what to start")
	}
	for _, line := range strings.Split(string(bat), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "@") {
			line = strings.TrimPrefix(line, "@")
		}
		if line == "" {
			continue
		}
		word, rest := split(line)
		switch strings.ToUpper(word) {
		case "REM", "MODE", "PAUSE", "CLS", "SET", "PATH", "PROMPT":
			// Settings and decoration: nothing a game depends on.
		case "ECHO":
			for _, c := range []byte(rest) {
				m.chPut(c)
			}
			m.chPut('\r')
			m.chPut('\n')
		case "CD", "CHDIR":
			e, ok := d.Resolve(m.cwd, rest)
			if !ok || !e.isDir {
				return fmt.Errorf("z80: AUTOEXEC.BAT changes to %q, "+
					"which is not a directory on this disk", rest)
			}
			m.cwd = e.clus
		default:
			// Anything else is a program to run, and a program that
			// starts a game does not come back.
			return m.runCOM(d, word, rest)
		}
	}
	return fmt.Errorf("z80: AUTOEXEC.BAT finished without starting anything")
}

// split takes the first word off a line.
func split(s string) (word, rest string) {
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		return s[:i], strings.TrimSpace(s[i+1:])
	}
	return s, ""
}

// runCOM loads a program where MSX-DOS loads one and starts it.
//
// The machine it starts on is the one the kernel hands over: the transient
// program area from 0100h, the stack at the top of it, the default transfer
// address at 0080h with the command tail written there, and the two file
// control blocks at 005Ch and 006Ch that the kernel fills in from the
// command line. A program reads all four without being told.
func (m *M) runCOM(d *Disk, name, args string) error {
	prog, ok := d.ReadAt(m.cwd, name)
	if !ok {
		if prog, ok = d.ReadAt(m.cwd, name+".COM"); !ok {
			return fmt.Errorf("z80: %s is not on this disk", name)
		}
	}
	if len(prog) == 0 {
		return fmt.Errorf("z80: %s is empty", name)
	}
	for i, v := range prog {
		if comLoad+i > 0xFFFF {
			break
		}
		m.Mem[comLoad+i] = v
	}
	// The command tail, counted and terminated the way the kernel writes
	// it, and the two control blocks left empty because nothing here
	// passes a file name on the line.
	m.Mem[comDMA] = byte(len(args))
	for i := 0; i < len(args) && i < 127; i++ {
		m.Mem[comDMA+1+i] = args[i]
	}
	m.Mem[comDMA+1+len(args)] = 0x0D
	for a := 0x005C; a < 0x0080; a++ {
		m.Mem[a] = 0
	}
	m.Mem[0x005C], m.Mem[0x006C] = 0, 0
	// The kernel's own entry point, which it writes into page zero for
	// the program to call: a jump to the function-call handler. With
	// page zero as RAM there is no shim answering at 0005h, and a
	// program calling it walked into the system bytes and executed
	// them.
	m.Mem[0x0005] = 0xC3
	m.Mem[0x0006] = byte(dosBDOS & 0xFF)
	m.Mem[0x0007] = byte(dosBDOS >> 8)
	m.dma = comDMA
	m.SP = comStack
	// Under MSX-DOS every page is RAM: the kernel and the program share
	// one address space and the BIOS is not in it. That matters to more
	// than the memory map -- page zero's shims answer only while the
	// BIOS's slot is selected there, and a program whose own code lives
	// at 0C96h needs that code, not a shim.
	m.PrimarySlot = 0xFF
	m.dosProgram = true
	// What the kernel knows about memory, where MSX-DOS 2 keeps it: how
	// many mapper segments there are. A program that asks the kernel
	// rather than counting them itself reads this one byte.
	m.initRAMMapper()
	m.Mem[0xF2CF] = ramSegments
	m.IFF = true
	return m.runEntry(comLoad, "the disk's program")
}
