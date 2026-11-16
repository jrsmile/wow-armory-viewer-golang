# Armory

A GPU-accelerated World of Warcraft character viewer written in Go. Enter a
Blizzard Armory character URL and the character is assembled from the live
retail game data on Blizzard's CDN and displayed fullscreen, looping its
native **Stand** (idle) animation.

Linux, Go 1.25+, the SDL2 runtime and an OpenGL 3.3 driver are required. The
binary builds with `CGO_ENABLED=0`: Go calls SDL2/OpenGL through purego.
There is no browser, hosted renderer, local WoW install or C++ helper, and no
Blizzard assets are bundled.

## Build and run

```sh
make build
./build/armory open --url https://worldofwarcraft.blizzard.com/en-gb/character/eu/tarren-mill/naowh
./build/armory open --url https://worldofwarcraft.blizzard.com/de-de/worldsoul/eu/armory/character/onyxia/taddie
./build/armory open            # paste a URL (Ctrl+V) and press Enter
./build/armory open --bg backdrop.png --url ...   # PNG behind the character
```

While a character loads, the bottom of the window lists every CDN URL being
downloaded (with size or HTTP status) above a progress bar for the transfer
in flight. Once the character is loaded all UI, including the mouse cursor,
is hidden (Tab brings the overlay back to enter another URL). The first lookup
downloads the CDN manifests (hundreds of MB) and DB2 tables; everything is
cached under `~/.cache/armory/cdn` (`--cache` overrides), so later lookups
take seconds. `-v` logs downloads and assembly warnings.
`--bg FILE.png` draws a PNG behind the character, scaled to cover the window
without distortion (edges are cropped when aspect ratios differ).

### Where the character comes from

The character's *current* look is Blizzard's profile `/appearance` document:
race, sex, class, every customization choice and the transmogrified item
appearance in each slot. It is fetched

- from the official API when `BLIZZARD_CLIENT_ID` and
  `BLIZZARD_CLIENT_SECRET` are set (create them at
  <https://develop.battle.net>; never commit them), otherwise
- from a public pass-through of that same endpoint
  (`https://marlam.in/we/api.php`). Set `ARMORY_APPEARANCE_PROXY` to use
  another one, or to an empty string to disable it.

“Current” means the profile Blizzard last published (updated on logout).
Private or unknown characters produce an error. A saved document can be
rendered offline with `--appearance-json FILE`.

Only the appearance document comes from a web API. Models, textures,
animations and game tables come straight from Blizzard's CDN.

```sh
./build/armory profile --url URL          # parsed appearance as JSON
./build/armory self-test --url URL --screenshot new.png
./build/armory cdn-info --region eu --json
./build/armory fetch --file FILE_DATA_ID --output new-file
./build/armory open --model FILE_DATA_ID  # a single raw M2, all geosets
```

## How a character is assembled

`internal/character` re-implements the wow.export/wow.tools.local pipeline
against DB2 tables read by `internal/db2`:

1. **Body**: `ChrRaceXChrModel` → `ChrModel` → `CreatureDisplayInfo` →
   `CreatureModelData` gives the HD body M2 (with the race's fallback model
   where needed).
2. **Customizations**: `ChrCustomizationOption/Choice/Element` select
   geosets, skinned extra models (hair, horns, jewelry…) and texture
   materials, honouring related-choice gating and conditional models.
3. **Textures**: customization materials and the worn items'
   `ItemDisplayInfoMaterialRes` section textures are composited on the CPU
   into the body's replaceable skin texture, following `ChrModelTextureLayer`
   order and blend modes (multiply, overlay, screen, alpha) like
   wow.export's `CharMaterialRenderer`.
4. **Geosets**: each slot's `ItemDisplayInfo` geoset groups switch body
   submeshes (sleeves, robes, boots, cloak…) in the game's slot priority,
   hiding hair/ears under helmets per `HelmetGeosetData`.
5. **Item models**: `ModelFileData`/`ComponentModelFileData` pick the
   race/sex variant; helmets, shoulders, weapons and shields are attached to
   M2 attachment points, while collection models (armor pieces) are skinned
   to the body skeleton by bone-name CRC.

The result is a scene of instances. Every frame Go evaluates the body's
Stand pose, shares it with collection models, positions attachments, and the
GPU does vertex skinning, texturing, lighting and M2 blending (opaque,
alpha-key, alpha, additive, modulate).

The format layer supports MD21/MD20 (v264–274), SFID skins, TXID textures,
SKID skeletons, external `.anim` Stand tracks, BLP2 (palette/DXT1/3/5/BGRA),
WDC3/4/5 tables and checksum-verified TACT/BLTE over HTTP ranges.

## Known limitations

- DB2 **hotfixes** are not on the CDN, so items or choices added by hotfix
  after the build may be missing; they are reported as warnings.
- Particles, ribbons, texture animations, enchant visuals, spell effects
  and some special materials (eye glow, “runes”) are not rendered;
  unresolved replaceable textures are skipped rather than drawn.
- A few cosmetic details differ from the game (e.g. legacy textures at robe
  hems, simplified lighting/shader effects).
- Linux only; encrypted CDN content fails explicitly (no keys are obtained).

## Keyboard

| Shortcut | Action |
| --- | --- |
| Ctrl+V / Ctrl+A | Paste / clear URL |
| Enter | Look up and display the character |
| Tab | Hide/show the input overlay |
| F11 | Toggle fullscreen |
| Left/Right, Up/Down | Orbit, zoom (overlay hidden) |
| Space | Pause/resume Stand (overlay hidden) |
| ? | Shortcut overlay |
| Esc / Q (overlay hidden) | Quit |

## Verification

```sh
make test     # unit tests (synthetic TACT, DB2, M2, compositing, scenes)
make check    # go vet + gofmt
make build
./build/armory self-test --url URL --screenshot /tmp/new-shot.png
```

`self-test` assembles the character, renders it in a window for half a
second, checks OpenGL errors, optionally saves the app's framebuffer and
exits. Verified against retail build `12.1.5.70783` with a Blood Elf female
priest and a Mag'har Orc male hunter.

## wow.tools source research

The original <https://wow.tools> retired in May 2025. Its own landing page
points to wow.tools.local and wago.tools; its former viewer URL is not a
usable runtime dependency.

Primary sources inspected:

- [wow.tools `mv/modelviewer.js`](https://github.com/Marlamin/wow.tools/blob/master/mv/modelviewer.js):
  `createscene` calls `_createWebJsScene` with `/casc/file/fname?...` and
  `/casc/extract/<build>/` URL prefixes; `loadModel` chooses the M2/WMO/ADT
  scene, and display lookups join DB2 tables. The browser is not simply
  downloading a ready-made character mesh from an Armory URL.
- [WebWowViewerCpp](https://github.com/Deamon87/WebWowViewerCpp):
  C++ model/map renderer built for both native and WebAssembly/WebGL
  frontends. M2/SKIN geometry, animation, texture, and rendering subsystems
  are separate. This project researches the pipeline, **not** copies or
  bundles that renderer.
- [CASCToolHost `CASC.cs`](https://github.com/Marlamin/CASCToolHost/blob/master/CASCToolHost/CASC/CASC.cs),
  [`NGDP.cs`](https://github.com/Marlamin/CASCToolHost/blob/master/CASCToolHost/CASC/NGDP.cs),
  [`BLTE.cs`](https://github.com/Marlamin/CASCToolHost/blob/master/CASCToolHost/CASC/BLTE.cs):
  build config -> encoding manifest -> root FileDataID/content key ->
  encoding key -> archive index/range -> BLTE decoding. That extraction
  service supplies the renderer's byte requests.
- [CascLib WoW root parser](https://github.com/ladislav-zezula/CascLib/blob/master/src/CascRootFile_WoW.cpp):
  the modern version-2 root uses a 17-byte group header, unlike the retired
  wow.tools-era layout. Historical code alone is not sufficient for retail.
- Format references: [TACT](https://wowdev.wiki/TACT),
  [BLTE](https://wowdev.wiki/BLTE), [M2](https://wowdev.wiki/M2),
  [SKIN](https://wowdev.wiki/M2/.skin), [SKEL](https://wowdev.wiki/M2/.skel),
  [BLP](https://wowdev.wiki/BLP).
- [wow.export](https://github.com/Kruithne/wow.export) (`tab_characters.js`,
  `character-appearance.js`, `CharMaterialRenderer.js`, `DBItem*`/`DBComponent*`
  caches) and [wow.tools.local](https://github.com/Marlamin/wow.tools.local):
  the current, maintained descendants of the wow.tools character viewer. Their
  algorithms for customization choices, texture-layer composition, item
  geoset groups, component-model race/sex selection and attachment points
  were followed (re-implemented in Go, not copied).
- [WoWDBDefs](https://github.com/wowdev/WoWDBDefs): build-matched DB2 layouts,
  embedded for the tables used here.
- Character identity/appearance is supplied by the official
  [Blizzard developer API](https://develop.battle.net/documentation/world-of-warcraft/profile-apis),
  independently of CDN assets. Numeric display IDs/customization choice IDs
  are not mesh FileDataIDs.

Pipeline implemented here:

```text
Blizzard patch metadata -> build/CDN configs
  -> encoding manifest -> root FileDataID -> content key -> encoding key
  -> loose CDN object OR archive index + HTTP Range
  -> checksum-verified BLTE -> M2 / SKIN / SKEL / ANIM / BLP
  -> native Go loaders -> OpenGL vertex skinning + rendering

Armory URL -> /appearance JSON (race, sex, customization choices, transmogs)
  -> DB2 tables (WDC5 + WoWDBDefs) -> body M2, customization models,
     geosets, composited skin/armor textures, item models + attachments
  -> scene of skinned instances sharing the body skeleton
  -> native renderer, looping the body's Stand animation
```

World of Warcraft and game assets belong to Blizzard Entertainment.
Use only assets you are authorized to access, subject to Blizzard's terms.
