//go:build msxdata

package z80

// dataBlocks is the cartridge's data, compiled in.
//
// The real one is the generated data_gen.go: the runs of bytes no translated
// instruction covers, as named Go slices. This stand-in is what lets the
// msxdata build compile inside msx2go, where there is no cartridge to have
// pruned anything from -- and, like run_stub.go, it is a file the emitter does
// not copy into the output. See emit.runtimeFiles.
var dataBlocks []Block
