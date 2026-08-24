package dis

// Rom is a flat image and the read interface the decoder wants. A banked
// image decodes one bank at a time through the same type, with base set to
// the page the bank is mapped into.
type Rom struct {
	Data []byte
	Base uint16
}

// Readable reports whether n bytes from addr are inside the image.
func (r Rom) Readable(addr uint16, n int) bool {
	return int(addr) >= int(r.Base) &&
		int(addr)+n <= int(r.Base)+len(r.Data)
}

// Byte reads one byte.
func (r Rom) Byte(addr uint16) byte { return r.Data[int(addr)-int(r.Base)] }

// Word reads a little-endian sixteen-bit value.
func (r Rom) Word(addr uint16) uint16 {
	i := int(addr) - int(r.Base)
	return uint16(r.Data[i]) | uint16(r.Data[i+1])<<8
}
