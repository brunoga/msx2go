package z80

// Buttons is the six-bit control state the game works in, in the bit order
// the ROM assembles at 45E8h. The joystick and the keyboard produce the same
// layout and are ORed together, so either can drive the game.
type Buttons byte

const (
	Up Buttons = 1 << iota
	Down
	Left
	Right
	TriggerA // SPACE
	TriggerB // SELECT
)

// Keyboard rows and bits the ROM samples, decoded from 45FCh-4620h.
// SNSMAT reports a 0 bit for a pressed key.
const (
	rowFunc   = 7 // bit 6 = SELECT
	rowMain   = 8 // bit 0 = SPACE, 4 = LEFT, 5 = UP, 6 = DOWN, 7 = RIGHT
	bitSelect = 6
	bitSpace  = 0
	bitLeft   = 4
	bitUp     = 5
	bitDown   = 6
	bitRight  = 7
)

// SetInput applies a control state to both the keyboard matrix and the
// joystick port, which is what the ROM reads.
func (m *M) SetInput(b Buttons) {
	main := byte(0xFF)
	fn := byte(0xFF)
	clear := func(v *byte, bit uint) { *v &^= 1 << bit }

	if b&Up != 0 {
		clear(&main, bitUp)
	}
	if b&Down != 0 {
		clear(&main, bitDown)
	}
	if b&Left != 0 {
		clear(&main, bitLeft)
	}
	if b&Right != 0 {
		clear(&main, bitRight)
	}
	if b&TriggerA != 0 {
		clear(&main, bitSpace)
	}
	if b&TriggerB != 0 {
		clear(&fn, bitSelect)
	}
	m.Keys[rowMain] = main
	m.Keys[rowFunc] = fn

	// The joystick reads active-low in the same bit order the game uses.
	m.PSG.PortA = ^byte(b) | 0xC0
}

// The keyboard, in full.
//
// SetInput above covers the six bits a joystick has, which is all an action
// game usually reads. It is not all a cartridge reads: Salamander asks for F5
// to continue after a game over, and plenty of others want ESC, RETURN, STOP
// or the number keys. Those live in the same matrix and cost nothing to
// provide, so the machine describes the whole of it and lets the harness map
// host keys onto it.
//
// This is the standard MSX matrix. Row 8's directions and row 7's SELECT are
// the same positions SetInput writes, deliberately: the two ways in agree.
type MSXKey struct{ Row, Bit uint8 }

var (
	KeyF1     = MSXKey{6, 5}
	KeyF2     = MSXKey{6, 6}
	KeyF3     = MSXKey{6, 7}
	KeyF4     = MSXKey{7, 0}
	KeyF5     = MSXKey{7, 1}
	KeyEsc    = MSXKey{7, 2}
	KeyTab    = MSXKey{7, 3}
	KeyStop   = MSXKey{7, 4}
	KeyBS     = MSXKey{7, 5}
	KeySelect = MSXKey{7, 6}
	KeyReturn = MSXKey{7, 7}
	KeySpace  = MSXKey{8, 0}
	KeyHome   = MSXKey{8, 1}
	KeyIns    = MSXKey{8, 2}
	KeyDel    = MSXKey{8, 3}
	KeyLeft   = MSXKey{8, 4}
	KeyUp     = MSXKey{8, 5}
	KeyDown   = MSXKey{8, 6}
	KeyRight  = MSXKey{8, 7}
	KeyShift  = MSXKey{6, 0}
	KeyCtrl   = MSXKey{6, 1}
	KeyGraph  = MSXKey{6, 2}
	KeyCaps   = MSXKey{6, 3}
	KeyCode   = MSXKey{6, 4}
)

// LetterKey is where a letter sits in the matrix. A is row 2 bit 6 and the
// alphabet runs on from there.
func LetterKey(c byte) (MSXKey, bool) {
	if c >= 'a' && c <= 'z' {
		c -= 'a' - 'A'
	}
	if c < 'A' || c > 'Z' {
		return MSXKey{}, false
	}
	n := int(c-'A') + 2*8 + 6 // A is row 2, bit 6
	return MSXKey{uint8(n / 8), uint8(n % 8)}, true
}

// DigitKey is where a digit sits: 0 is row 0 bit 0, and they run on.
func DigitKey(c byte) (MSXKey, bool) {
	if c < '0' || c > '9' {
		return MSXKey{}, false
	}
	n := int(c - '0')
	return MSXKey{uint8(n / 8), uint8(n % 8)}, true
}

// ClearKeys releases every key. SNSMAT reports a 0 bit for a pressed one, so
// released is all ones.
func (m *M) ClearKeys() {
	for i := range m.Keys {
		m.Keys[i] = 0xFF
	}
}

// PressKey holds one key down until the next ClearKeys.
func (m *M) PressKey(k MSXKey) {
	if int(k.Row) < len(m.Keys) {
		m.Keys[k.Row] &^= 1 << k.Bit
	}
}

// SetJoystick sets the joystick port alone, leaving the keyboard to the
// matrix. See SetInput, which drives both at once.
func (m *M) SetJoystick(b Buttons) { m.PSG.PortA = ^byte(b) | 0xC0 }
