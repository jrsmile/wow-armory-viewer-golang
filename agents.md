# Agent handoff: Armory WoW model viewer

This document is a restart handoff for work in progress. Read it before
changing code. The repository may contain uncommitted changes from the previous
session; preserve and inspect them rather than reverting them.

## Project and user goal

Repository: `/home/user/Work/armory`  
Module: `github.com/jrsmile/armory`

The user is building a Go, GPU-accelerated World of Warcraft character viewer.
It accepts a Blizzard Armory character URL, assembles the current appearance
from Blizzard CDN assets, and displays the animated character fullscreen in
idle. The user has repeatedly stressed preserving existing behavior while
fixing visual issues.

The current task in progress is the prior request to improve M2 lighting,
shaders, and gear effects based on research into wow.tools.local and the
Wowhead character viewer. The current uncommitted work adds model material
tracks from external `.anim` files, particle simulation/rendering, and weapon
enchant visuals. Continue and verify this work before considering the shader
and effects request finished.

## Project architecture

- `cmd/armory`: CLI, HTTP/CDN session wiring, self-test and screenshot paths.
- `internal/blizzard`: Armory URL parsing and Retail/Forever appearance
  retrieval.
- `internal/character`: appearance JSON parsing and DB2-driven character,
  armor, weapon, and geoset assembly.
- `internal/db2`: WDC DB2 reader and per-table DBD definitions.
- `internal/tact`: Blizzard CDN, encoding/root lookup, and BLTE extraction.
- `internal/model`: M2/SKIN/skeleton parsing, animation, materials, scene
  assembly, and texture loading.
- `internal/viewer/native_linux.go`: native SDL2/OpenGL viewer, mesh shaders,
  draw ordering, and now the particle rendering path.

This is a Linux-only viewer. Rendering uses SDL2/OpenGL through purego. Build
with CGO disabled. Do not introduce a dependency on an external renderer.

## Completed product work from earlier requests

The codebase already supports:

- Retail Armory appearance URLs and the newer `/forever/` URL format.
- Direct Blizzard CDN asset loading, TACT/BLTE extraction, WDC DB2 parsing,
  M2/SKIN/skeleton assembly, and composited character textures.
- Fullscreen standing idle animation with idle-fidget rotation/cross-fades.
- CDN download progress and URL display while loading; UI hides after a
  successful character load.
- `--bg` PNG backgrounds.
- Prior fixes for cape visibility, extra geosets/equipment, underwear, and belt
  geometry. Avoid regressing these.

Regression profiles used previously:

- Retail Taddie:
  `https://worldofwarcraft.blizzard.com/de-de/worldsoul/eu/armory/character/onyxia/taddie`
- Retail Idéle:
  `https://worldofwarcraft.blizzard.com/de-de/worldsoul/eu/armory/character/onyxia/id%C3%A9le`
- Theradras Taddie (previously tested using an appearance JSON from the
  public proxy):
  `https://worldofwarcraft.blizzard.com/de-de/worldsoul/eu/armory/character/theradras/taddie`
- WoW Forever:
  `https://worldofwarcraft.blizzard.com/de-de/forever/eu/armory/character/taddie-eule`
- Naowh (used to verify an enchant visual; fetch appearance JSON through the
  existing proxy/session flow if needed).

## Current uncommitted work

The session was interrupted during implementation. Changes are **not committed**.
The last observed `git status --short` included:

- Modified: `README.md`, `internal/character/appearance.go`,
  `internal/character/build.go`, `internal/character/character_test.go`,
  `internal/db2/db.go`, `internal/model/load.go`, `internal/model/m2.go`,
  `internal/model/model_test.go`, `internal/model/scene.go`,
  `internal/model/scene_test.go`, `internal/viewer/native_linux.go`.
- New: `internal/model/material.go`, `internal/model/particle.go`,
  `internal/model/particle_test.go`,
  `internal/db2/defs/ItemVisuals.dbd`,
  `internal/db2/defs/SpellItemEnchantment.dbd`,
  `internal/db2/defs/TransmogIllusion.dbd`.
- Temporary probes to inspect/remove before finalizing:
  `cmd/armory/zz_ench_test.go`, `cmd/armory/zz_part_test.go`,
  `cmd/armory/zz_scan_test.go`.

The previous tool session showed no running shell sessions after interruption.
An online `zz_scan_test.go` which attempted to find real models with external
material animation data ran for a long time and timed out/interrupted. Do not
re-run its broad CDN scan. The temporary probe tests are not production tests
and must not be left in the final tree.

### Shader and lighting work

The OpenGL renderer in `internal/viewer/native_linux.go` already contains the
earlier shader/lighting implementation:

- M2 shader ID resolution and up to four texture combiners, including
  environment mapping and UV transforms.
- Animated mesh colors, transparency weights, and texture transforms.
- Material flag behavior, transparent batch sorting, and Wowhead-like studio
  lighting (ambient, key, and fill lights).

The README describes these features. Preserve them while completing particle
rendering.

### External material `.anim` tracks

`internal/model/material.go` was newly created/refactored in this work. It
selects the model's own Stand sequence, separates M2 track descriptors from
external animation values, supports global loops, and exposes
`Model.SetEffectAnimation`. `internal/model/load.go` fetches the referenced
animation and records a warning if it cannot be loaded.

`internal/model/m2.go` adds `EffectAnimID` and emitter storage. Existing
skeleton/Stand animation handling is separate; do not confuse the skeleton's
AFID/Stand with the M2's own effect-animation file.

There is a synthetic test in `internal/model/model_test.go` for the external
track path. It passed with `go test ./internal/model/`. Real model data with an
external material animation has not yet been confirmed; the broad search timed
out. Keep that uncertainty explicit unless a bounded real fixture is found.

### Particles

`internal/model/particle.go` parses M2 particle emitter records (stride 492),
tracks, age curves and compressed gravity, then simulates particles and builds
textured quads. `internal/viewer/native_linux.go` adds a particle shader and
streaming VBO path; particles are depth-tested and drawn after transparent
meshes. Particle-only models are permitted by `ParseM2`/`LoadWith`.

An enchant model was confirmed from CDN as FileDataID `166033`: it has no
vertices, one plane emitter, blend mode 4, texture FileDataID `145515`.
Synthetic particle tests in `internal/model/particle_test.go` cover gravity,
spawning/aging, curves, quads, atlas dimensions, and disabled emitters.

A visual smoke comparison was made for Naowh with and without enchant `213`;
the enchant version showed the expected bright weapon glow. Four regression
screenshots (Taddie, Idéle, Theradras Taddie, Forever Taddie-Eule) were also
rendered successfully after the viewer changes. These were smoke checks, not
proof that all particle modes match the game.

Known current approximations/limits, also reflected in the README:

- Spline emitters are approximated as plane emitters; bone emitters are
  skipped.
- Ribbons, particle tails, spell/aura effects, and particle-spawned models are
  not implemented.
- Particle-specific replaceable color tracks and all legacy emitter variants
  may still need review.
- The particle simulation and external animation parser need a careful code
  review and complete test pass before declaring them done.

### Enchant visuals

`internal/character/appearance.go` parses the Armory `enchant` field into
`Item.Enchant`. `internal/character/build.go` optionally loads the
`SpellItemEnchantment` and `ItemVisuals` DB2 tables, resolves an enchant ID to
up to five ItemVisual model IDs, loads those assets, and attaches each effect
to the corresponding weapon attachment point. `Instance.Parent` in
`internal/model/scene.go` lets the effect follow its weapon.

Research findings:

- The appearance API may report an ID that is a SpellItemEnchantment ID; its
  `ItemVisual` field then resolves to an ItemVisuals row.
- If no SpellItemEnchantment row exists, the code tries the ID directly as an
  ItemVisuals ID. This is needed for the observed Naowh `enchant: 213` case;
  ItemVisuals 213 points at model 166033.
- Some valid stat enchants have no visual and correctly display no effect.
- WoW Forever gear data does not currently include enchant information.
- `TransmogIllusion.dbd`/FileDataID was added during investigation but is not
  currently required by the resolution path; verify whether to keep it.

## Verification state at interruption

Observed successful commands:

- `go build ./...`
- `go vet ./...`
- `go test ./internal/...`
- `make build`
- `go test ./internal/model/`
- Native self-test screenshots for the four regression profiles listed above.

The run that added particle tests initially found and fixed a test scheduling
assumption; the subsequent `go test ./internal/model/` passed. No final
`make test`, `make check`, and `make build` were run after every last edit.
Re-run the repository's complete standard checks after reviewing/fixing the
current code. The intended project checks are:

```sh
make test
make check
make build
```

`make test` uses the full Go test suite (including race tests); `make check`
includes formatting and vetting. If commands fail, fix only issues introduced
by this work and preserve unrelated user changes.

The normal self-test invocation is:

```sh
./build/armory self-test --url 'ARMORY_URL' --screenshot /tmp/new-shot.png
```

The output screenshot path must not already exist. For a saved appearance JSON,
use `--appearance-json path/to/appearance.json`; add `--region eu` when
appropriate. The CDN cache is under `~/.cache/armory/cdn`.

## Suggested restart sequence

1. Read this file, then inspect `git status --short` and the complete diff.
   Preserve all existing changes.
2. Read the implementations in `internal/model/material.go`,
   `internal/model/particle.go`, `internal/model/load.go`,
   `internal/model/m2.go`, `internal/model/scene.go`,
   `internal/character/build.go`, and `internal/viewer/native_linux.go`.
3. Check bounds/offset validation, track-sequence selection, timing and
   pre-warm behavior, particle blend/alpha behavior, sprite atlas UVs, and
   parent-instance attachment transforms. Fix any concrete correctness issues
   and add focused tests in the existing model test files.
4. Confirm README claims match actual implemented behavior, especially the
   approximations and unsupported modes listed above.
5. Remove the three `cmd/armory/zz_*_test.go` probe files using targeted edits;
   do not remove other files.
6. Run `gofmt` on changed Go files and run `make test`, `make check`, and
   `make build`. Then rerender at least the Naowh enchant comparison and
   existing Retail/Forever regression profiles if feasible.
7. Report verified functionality and unresolved limits concisely. Do not
   commit unless asked.

## Earlier feature requests to preserve

The conversation included requests to fix new Armory URL parsing, cape texture
selection, a bottom CDN progress bar showing each URL, missing/extra geosets
and equipment (feet/head, wristwatch, underwear), a `--bg` PNG option, belt
ends, rotating idle variations, WoW Forever URL support, and now more accurate
shaders/lighting/gear effects. Keep these as compatibility constraints when
making changes.
