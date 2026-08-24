//go:build !msxcheck

package z80

// hole is declared in both builds so that the machine's fields do not have to
// be. The checking build gives it meaning; here it is an empty shape that no
// code ever fills. See read_check.go.
type hole struct{ lo, hi int }
