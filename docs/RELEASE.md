# Releasing msx2go

What has to be true before this tree is public, in the order it bites.

## 1. The repository must not carry what msx2go must not ship

msx2go lives inside a workspace whose git history tracks Microsoft/ASCII
BIOS ROMs, Philips disk ROMs and commercial game images (`MSX2.ROM`,
`Breaker (1987)(Philips).dsk`, `kingsvalleyplus.dsk`, the game ROMs the
sibling projects use). Removing the files now does not remove them from
history. The release therefore starts as a **fresh repository containing
only `msx2go/`** — the module is self-contained (`GOWORK=off go build
./...` passes) and nothing in it references the parent tree. Do not
publish the workspace repo, and do not rewrite its history in place;
export the subtree.

Checklist for the export:

- `git ls-files` in the new repo shows no `.rom`, `.dsk`, `.dat`, or
  anything under `games/` beyond the READMEs.
- `grep -ri` for home paths, e-mail addresses and machine names.
- Done: the module path is `github.com/brunoga/msx2go`.

## 2. Licensing

- **Done: Apache License 2.0**, in LICENSE.
- **Done: the C-BIOS character set's BSD notice is in NOTICE**, with its
  copyright holders. It is the only third-party content in the tree. The
  emitter writes a NOTICE into every generated module, carrying the C-BIOS
  license and naming the game bytes as the input image's.
- **The output is derivative of the input.** README's Legal section says
  so; keep it saying so.

## 3. What is verified, stays verified

The regression battery is manual today. Before tagging:

- `go test ./...` — the machine model.
- `gofmt -l .` empty, `go vet ./...` clean. `staticcheck` reports a
  handful of cross-build false positives (symbols used only by generated
  modules or under the `msxcheck` tag); the genuine findings are fixed.
- The digest battery: every verified title, `-frames 600 -digest 600`
  under `msxrun`, compared against the recorded values. The images cannot
  live in CI, so this is a local gate; the expected digests are in the
  table below. VRAM and PSG digests must be identical; the RAM digest
  moves when work-area bytes are deliberately added (CGPNT did this) and
  a change there needs explaining, not just accepting.

| title | vram | psg |
|---|---|---|
| kingsvalley.rom | `04b7ec925d54` | `54a9869efb07` |
| salamander.rom | `30f206a5aa53` | `052996a8096e` |
| kingsvalleyplus.dsk | `a44ce1a05d9d` | `54a9869efb07` |
| King's Valley II .rom | `88d50676f457` | `ed0ab4273234` |
| spacemanbow.rom | `fd9959db410a` | `052996a8096e` |
| spacemanbow-frs.rom | `146a72685157` | `052996a8096e` |

- For a disk title with a translation, the `-interpret` twin comparison
  (see README, Verification) over at least 3000 frames.

## 4. CI that can run without the images

- build the three commands on linux/mac/windows (the play harness needs
  Ebitengine's platform deps; headless needs none),
- `go test ./...`, `gofmt`, `go vet`,
- generate-and-build a module from a **synthetic** cartridge: a few
  hundred bytes of hand-assembled Z80 with an AB header committed as a
  test fixture would let CI exercise trace → emit → build end to end
  without any copyrighted bytes. This fixture does not exist yet and is
  the single most valuable piece of missing test infrastructure.

## 5. Versioning and tags

- Tag `v0.x.y`; the module path already matches. Nothing promises API
  stability — `internal/` is internal, and the two public surfaces are
  the CLI flags and the generated module's shape. Say in the README that
  the generated-module layout may change between minor versions and a
  regenerate is the upgrade path.
- A `-version` flag on the commands, stamped from the tag via
  `-ldflags -X`, so bug reports can name a build.

## 6. Known gaps a release note should own

- Floppy main threads run interpreted (the interpreter→translation bridge
  is designed but not built; the translation currently enters through the
  interrupt path).
- No V9958 (MSX2+ YJK screens), cassette, MSX-DOS 2, mouse, Kanji.
- The BIOS/sub-ROM surface is exactly what the verified titles exercise;
  unimplemented entries fail loudly by design.
- `msxrun`'s flag set is a workbench, not an interface; flags may come
  and go.

## 7. Nice-to-have, explicitly not blocking

- `-romout`: emit a bootable cartridge image from a floppy conversion
  (the snapshot, a loader-replay stub, patched disk calls). All the
  ingredients exist; see the Breaker notes.
- WASM build of the play harness (Ebitengine supports it; the data loader
  already refuses politely on `js` without `msxdata`).
- The interpreter→translation bridge, for fully-generated floppy titles.
