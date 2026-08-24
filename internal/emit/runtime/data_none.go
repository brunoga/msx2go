//go:build !msxdata

package z80

// dataBlocks is empty in a build without the msxdata tag, and the cartridge's
// data is looked for on disk instead. The generated data_gen.go declares the
// other half of this pair. See Info.Blocks.
var dataBlocks []Block
