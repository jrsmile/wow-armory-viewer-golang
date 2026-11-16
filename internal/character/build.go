package character

import (
	"context"
	"fmt"
	"image"
	"sort"

	"github.com/jrsmile/armory/internal/db2"
	"github.com/jrsmile/armory/internal/model"
)

// Builder assembles a character the way wow.export's character tab does:
// base ChrModel, customization geosets/materials/skinned models, composited
// skin textures, equipment geosets, body textures and attached item models.
type Builder struct {
	Files model.Files
	DB    *db2.DB
	Cache *model.TextureCache
	Log   func(string)
}

var tableNames = []string{
	"ChrRaceXChrModel", "ChrModel", "CreatureDisplayInfo", "CreatureModelData",
	"ChrCustomizationOption", "ChrCustomizationChoice", "ChrCustomizationElement",
	"ChrCustomizationGeoset", "ChrCustomizationMaterial", "ChrCustomizationSkinnedModel",
	"ChrCustomizationCondModel", "ChrModelTextureLayer", "ChrModelMaterial",
	"CharComponentTextureSections", "TextureFileData", "ModelFileData",
	"ItemModifiedAppearance", "ItemAppearance", "ItemDisplayInfo", "ItemDisplayInfoMaterialRes",
	"ItemDisplayInfoModelMatRes", "ComponentModelFileData", "ComponentTextureFileData",
	"HelmetGeosetData", "Item",
}

type build struct {
	*Builder
	ctx      context.Context
	t        map[string]*db2.Table
	app      Appearance
	race     int64
	sex      int64
	layout   int64
	fallback int64 // race whose item models are used when ours has none
	scene    *model.Scene
}

func (b *build) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	b.scene.Warnings = append(b.scene.Warnings, msg)
	if b.Log != nil {
		b.Log(msg)
	}
}

// get returns a column of the row with the given ID.
func (b *build) get(table string, id int64, column string) (int64, bool) {
	t := b.t[table]
	row := t.Lookup(uint32(id))
	if id <= 0 || row < 0 {
		return 0, false
	}
	return t.Int(row, column), true
}

// Build assembles the character described by app.
func (bl *Builder) Build(ctx context.Context, app Appearance) (*model.Scene, error) {
	b := &build{Builder: bl, ctx: ctx, t: map[string]*db2.Table{}, app: app, scene: &model.Scene{Title: app.Name}}
	if b.Cache == nil {
		b.Cache = &model.TextureCache{}
	}
	for _, name := range tableNames {
		t, err := b.DB.Table(ctx, name)
		if err != nil {
			return nil, err
		}
		b.t[name] = t
	}
	b.race = int64(app.Race)
	switch b.race {
	case 25, 26: // Pandaren factions share the neutral model
		b.race = 24
	case 70: // Dracthyr visage forms use the Dracthyr ChrModel rows
		b.race = 52
	}
	if app.Female {
		b.sex = 1
	}
	chrModel := int64(-1)
	rx := b.t["ChrRaceXChrModel"]
	for _, r := range rx.Where("ChrRacesID", b.race) {
		if rx.Int(r, "Sex") == b.sex {
			chrModel = rx.Int(r, "ChrModelID")
		}
	}
	if chrModel < 0 {
		return nil, fmt.Errorf("no ChrModel for race %d sex %d", app.Race, b.sex)
	}
	display, ok := b.get("ChrModel", chrModel, "DisplayID")
	b.layout, _ = b.get("ChrModel", chrModel, "CharComponentTextureLayoutID")
	if fb, ok := b.get("ChrModel", chrModel, "ModelFallbackChrModelID"); ok && fb > 0 {
		for _, r := range rx.Where("ChrModelID", fb) {
			b.fallback = rx.Int(r, "ChrRacesID")
		}
	}
	creatureModel, ok2 := b.get("CreatureDisplayInfo", display, "ModelID")
	bodyFile, ok3 := b.get("CreatureModelData", creatureModel, "FileDataID")
	if !ok || !ok2 || !ok3 || bodyFile == 0 {
		return nil, fmt.Errorf("cannot resolve body model for ChrModel %d", chrModel)
	}

	cust := b.customizations(chrModel)
	if cust.condModel != 0 {
		if f, ok := b.get("CreatureModelData", cust.condModel, "FileDataID"); ok && f != 0 {
			bodyFile = f
		}
	}
	items := b.resolveItems()
	composites, raw := b.composite(cust, items)

	bodyFiles := map[uint32]uint32{}
	if cape, ok := items[SlotBack]; ok {
		// Cloaks are body geosets (group 15) textured with replaceable type 2.
		if fs := b.textureFiles(b.t["ItemDisplayInfo"].At(cape.row, "ModelMaterialResourcesID", 0)); len(fs) > 0 {
			bodyFiles[2] = uint32(b.bestTexture(fs))
		}
	}
	body, err := model.LoadWith(ctx, b.Files, uint32(bodyFile), model.Options{TypeImages: composites, TypeFiles: bodyFiles, Cache: b.Cache})
	if err != nil {
		return nil, fmt.Errorf("body model %d: %w", bodyFile, err)
	}
	b.scene.Warnings = append(b.scene.Warnings, body.Warnings...)
	root := &model.Instance{Name: "body", Asset: body, Geosets: b.bodyGeosets(body.Model, cust, items)}
	b.scene.Instances = append(b.scene.Instances, root)

	skinnedImages := map[uint32]*image.NRGBA{}
	for k, v := range composites {
		if _, ok := raw[k]; !ok {
			skinnedImages[k] = v
		}
	}
	for _, fdid := range sortedKeys(cust.skinned) {
		a, err := model.LoadWith(ctx, b.Files, uint32(fdid), model.Options{TypeImages: skinnedImages, TypeFiles: raw, Cache: b.Cache})
		if err != nil {
			b.warn("customization model %d: %v", fdid, err)
			continue
		}
		geosets := map[uint16]bool{}
		for g := range cust.skinned[fdid] {
			geosets[g] = true
		}
		b.scene.Instances = append(b.scene.Instances, &model.Instance{Name: fmt.Sprintf("customization %d", fdid),
			Asset: a, Geosets: geosets, BoneMap: model.MapBones(a.Model, body.Model)})
	}
	b.itemModels(items, body.Model, composites)
	return b.scene, nil
}

func sortedKeys[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

type customization struct {
	show, hide map[uint16]bool
	materials  []int64                   // ChrCustomizationMaterial IDs, element order
	skinned    map[int64]map[uint16]bool // collection FileDataID -> visible geosets
	skinGroups map[int]bool              // base geoset groups replaced by skinned models
	condModel  int64
}

func (b *build) geosetID(id int64) (uint16, bool) {
	t, ok := b.get("ChrCustomizationGeoset", id, "GeosetType")
	g, ok2 := b.get("ChrCustomizationGeoset", id, "GeosetID")
	return uint16(t*100 + g), ok && ok2
}

// customizations resolves chosen choices into geosets, materials and models.
func (b *build) customizations(chrModel int64) customization {
	c := customization{show: map[uint16]bool{}, hide: map[uint16]bool{}, skinned: map[int64]map[uint16]bool{}, skinGroups: map[int]bool{}}
	opts, choices, el := b.t["ChrCustomizationOption"], b.t["ChrCustomizationChoice"], b.t["ChrCustomizationElement"]
	type option struct {
		id      int64
		choices []int64
	}
	var options []option
	active := map[int64]bool{}
	for _, r := range opts.Where("ChrModelID", chrModel) {
		o := option{id: int64(opts.ID(r))}
		rows := choices.Where("ChrCustomizationOptionID", o.id)
		sort.SliceStable(rows, func(i, j int) bool {
			return choices.Int(rows[i], "OrderIndex") < choices.Int(rows[j], "OrderIndex")
		})
		chosen := int64(-1)
		want, has := b.app.Choices[int(o.id)]
		for _, cr := range rows {
			id := int64(choices.ID(cr))
			o.choices = append(o.choices, id)
			if has && id == int64(want) {
				chosen = id
			}
		}
		if chosen < 0 && len(o.choices) > 0 {
			chosen = o.choices[0]
			if has {
				b.warn("customization option %d: choice %d unknown in this build; using default", o.id, want)
			}
		}
		if chosen >= 0 {
			active[chosen] = true
		}
		options = append(options, o)
	}
	sort.Slice(options, func(i, j int) bool { return options[i].id < options[j].id })
	for _, o := range options {
		for _, choice := range o.choices {
			for _, r := range el.Where("ChrCustomizationChoiceID", choice) {
				related := el.Int(r, "RelatedChrCustomizationChoiceID")
				on := active[choice] && (related == 0 || active[related])
				if g := el.Int(r, "ChrCustomizationGeosetID"); g != 0 {
					if id, ok := b.geosetID(g); ok {
						if on {
							c.show[id] = true
						} else {
							c.hide[id] = true
						}
					}
				}
				if !on {
					continue
				}
				if m := el.Int(r, "ChrCustomizationMaterialID"); m != 0 {
					c.materials = append(c.materials, m)
				}
				if s := el.Int(r, "ChrCustomizationSkinnedModelID"); s != 0 {
					f, ok := b.get("ChrCustomizationSkinnedModel", s, "CollectionsFileDataID")
					gt, _ := b.get("ChrCustomizationSkinnedModel", s, "GeosetType")
					gi, _ := b.get("ChrCustomizationSkinnedModel", s, "GeosetID")
					if ok && f != 0 {
						if c.skinned[f] == nil {
							c.skinned[f] = map[uint16]bool{}
						}
						c.skinned[f][uint16(gt*100+gi)] = true
						c.skinGroups[int(gt)] = true
					}
				}
				if m := el.Int(r, "ChrCustomizationCondModelID"); m != 0 {
					if cm, ok := b.get("ChrCustomizationCondModel", m, "CreatureModelDataID"); ok {
						c.condModel = cm
					}
				}
			}
		}
	}
	return c
}

// equipped is one resolved item.
type equipped struct {
	Item
	display int64 // ItemDisplayInfoID
	row     int   // ItemDisplayInfo row
}

// hiddenAppearances are the transmog "Hidden ..." items. The client treats
// them as an empty slot; some (e.g. Hidden Cloak) still carry a placeholder
// ItemDisplayInfo that would otherwise draw a default cape.
var hiddenAppearances = map[int]bool{
	134110: true, // Hidden Helm
	134111: true, // Hidden Cloak
	134112: true, // Hidden Shoulder
	142503: true, // Hidden Shirt
	142504: true, // Hidden Tabard
	143539: true, // Hidden Belt
	158329: true, // Hidden Gloves
	168659: true, // Hidden Chestpiece
	168664: true, // Hidden Boots
	168665: true, // Hidden Bracers
	216696: true, // Hidden Pants
}

func (b *build) resolveItems() map[int]equipped {
	out := map[int]equipped{}
	ima, idi := b.t["ItemModifiedAppearance"], b.t["ItemDisplayInfo"]
	for _, it := range b.app.Items {
		if hiddenAppearances[int(it.ItemID)] {
			continue
		}
		rows := ima.Where("ItemID", int64(it.ItemID))
		best, bestMod := -1, int64(1<<62)
		for _, r := range rows {
			mod := ima.Int(r, "ItemAppearanceModifierID")
			if mod == int64(it.Modifier) {
				best, bestMod = r, -1
				break
			}
			if mod < bestMod {
				best, bestMod = r, mod
			}
		}
		if best < 0 {
			b.warn("slot %d: item %d not in ItemModifiedAppearance (newer than this build or hotfix-only)", it.Slot, it.ItemID)
			continue
		}
		display, ok := b.get("ItemAppearance", ima.Int(best, "ItemAppearanceID"), "ItemDisplayInfoID")
		row := idi.Lookup(uint32(display))
		if !ok || row < 0 {
			if display != 0 {
				b.warn("slot %d: item %d display %d unavailable", it.Slot, it.ItemID, display)
			}
			continue
		}
		out[it.Slot] = equipped{Item: it, display: display, row: row}
	}
	return out
}

// slotGeosets maps equipment slots to (GeosetGroup index, character geoset group).
var slotGeosets = map[int][][2]int{
	SlotHead:          {{0, 27}, {1, 21}},
	SlotShoulder:      {{0, 26}},
	SlotRightShoulder: {{0, 26}},
	SlotShirt:         {{0, 8}, {1, 10}},
	SlotChest:         {{0, 8}, {1, 10}, {2, 13}, {3, 22}, {4, 28}},
	SlotWaist:         {{0, 18}},
	SlotLegs:          {{0, 11}, {1, 9}, {2, 13}},
	SlotFeet:          {{0, 5}, {1, 20}},
	SlotHands:         {{0, 4}, {1, 23}},
	SlotBack:          {{0, 15}},
	SlotTabard:        {{0, 12}},
}

// geosetSlotOrder resolves conflicts: earlier slots win a geoset group
// (gloves > chest > shirt for sleeves, chest > legs for robes, ...).
var geosetSlotOrder = []int{SlotHead, SlotShoulder, SlotRightShoulder, SlotHands, SlotChest, SlotShirt, SlotLegs, SlotWaist, SlotFeet, SlotBack, SlotTabard}

func (b *build) bodyGeosets(m *model.Model, c customization, items map[int]equipped) map[uint16]bool {
	vis := map[uint16]bool{}
	for _, batch := range m.Batches {
		id := batch.Geoset
		g := id / 100
		vis[id] = (id == 0 || (id%100 == 1 && id >= 100) || g == 32) && g != 17 && g != 35
	}
	for id := range c.hide {
		if _, ok := vis[id]; ok && id != 0 {
			vis[id] = false
		}
	}
	for id := range c.show {
		if _, ok := vis[id]; ok && id != 0 {
			vis[id] = true
		}
	}
	hideGroup := func(g int) {
		for id := range vis {
			if int(id)/100 == g && id%100 != 0 {
				vis[id] = false
			}
		}
	}
	for g := range c.skinGroups {
		hideGroup(g)
	}
	idi := b.t["ItemDisplayInfo"]
	set := map[int]bool{}
	for _, slot := range geosetSlotOrder {
		it, ok := items[slot]
		if !ok {
			continue
		}
		for _, m := range slotGeosets[slot] {
			idx, group := m[0], m[1]
			raw := int(idi.At(it.row, "GeosetGroup", idx))
			if set[group] || (raw == 0 && group != 20 && b.laterSlotSets(slot, group, items)) {
				continue
			}
			value := 1 + raw
			if slot == SlotFeet && idx == 1 {
				value = raw
				if raw == 0 {
					value = 2
				}
			}
			set[group] = true
			hideGroup(group)
			// A missing x01 variant means "nothing" for that group, except
			// cloaks: HD bodies dropped 1501, so the default cape is 1502.
			target := uint16(group*100 + value)
			if _, ok := vis[target]; !ok && group == 15 && value == 1 {
				target = 1502
			}
			if _, ok := vis[target]; ok {
				vis[target] = true
			}
		}
	}
	// Group 14 is the underwear loincloth (default or customization-chosen);
	// worn trousers always cover it.
	if _, ok := items[SlotLegs]; ok {
		hideGroup(14)
	}
	if it, ok := items[SlotHead]; ok {
		vis2 := idi.At(it.row, "HelmetGeosetVis", int(b.sex))
		hgd := b.t["HelmetGeosetData"]
		for _, r := range hgd.Where("HelmetGeosetVisDataID", vis2) {
			if vis2 != 0 && hgd.Int(r, "RaceID") == b.race {
				hideGroup(int(hgd.Int(r, "HideGeosetGroup")))
			}
		}
	}
	return vis
}

// laterSlotSets reports whether a lower-priority slot sets a non-zero value
// for group, so a zero (default) value does not mask e.g. chest sleeves.
func (b *build) laterSlotSets(slot, group int, items map[int]equipped) bool {
	idi := b.t["ItemDisplayInfo"]
	after := false
	for _, s := range geosetSlotOrder {
		if s == slot {
			after = true
			continue
		}
		it, ok := items[s]
		if !after || !ok {
			continue
		}
		for _, m := range slotGeosets[s] {
			if m[1] == group && idi.At(it.row, "GeosetGroup", m[0]) != 0 {
				return true
			}
		}
	}
	return false
}

// slotLayers orders equipment textures on the body (higher draws later).
var slotLayers = map[int]int{SlotShirt: 10, SlotLegs: 10, SlotHead: 11, SlotFeet: 11, SlotShoulder: 13, SlotRightShoulder: 13,
	SlotChest: 13, SlotTabard: 17, SlotWaist: 18, SlotWrist: 19, SlotHands: 20, SlotMainHand: 21, SlotOffHand: 22, SlotBack: 23}

// textureFile returns the first UsageType 0 texture of a MaterialResources set.
func (b *build) textureFiles(materialResources int64) []int64 {
	if materialResources == 0 {
		return nil
	}
	tfd := b.t["TextureFileData"]
	var out []int64
	for _, r := range tfd.Where("MaterialResourcesID", materialResources) {
		if tfd.Int(r, "UsageType") == 0 {
			out = append(out, int64(tfd.ID(r)))
		}
	}
	return out
}

// bestTexture mirrors DBComponentTextureFileData.getTextureForRaceGender.
func (b *build) bestTexture(files []int64) int64 {
	if len(files) <= 1 {
		if len(files) == 1 {
			return files[0]
		}
		return 0
	}
	ctfd := b.t["ComponentTextureFileData"]
	type cand struct {
		fdid           int64
		g, class, race int64
	}
	var tagged []cand
	var untagged int64
	for _, f := range files {
		r := ctfd.Lookup(uint32(f))
		if r < 0 {
			if untagged == 0 {
				untagged = f
			}
			continue
		}
		c := cand{f, ctfd.Int(r, "GenderIndex"), ctfd.Int(r, "ClassID"), ctfd.Int(r, "RaceID")}
		if (c.g == b.sex || c.g == 3) && (c.class == 0 || c.class == int64(b.app.Class)) {
			tagged = append(tagged, c)
		}
	}
	if len(tagged) == 0 {
		return untagged
	}
	rank := func(c cand) int {
		r := 0
		if c.g != b.sex {
			r += 4
		}
		if c.class != int64(b.app.Class) {
			r += 2
		}
		if c.race != b.race {
			r++
		}
		return r
	}
	sort.SliceStable(tagged, func(i, j int) bool { return rank(tagged[i]) < rank(tagged[j]) })
	return tagged[0].fdid
}

func (b *build) image(fdid int64) *image.NRGBA {
	img, err := b.Cache.Get(b.ctx, b.Files, uint32(fdid))
	if err != nil {
		b.warn("texture %d: %v", fdid, err)
		return nil
	}
	return img
}

// composite builds one image per character texture type. raw returns the
// unbaked customization file for non-skin types (bound directly on skinned
// customization models, as wow.export does).
func (b *build) composite(c customization, items map[int]equipped) (map[uint32]*image.NRGBA, map[uint32]uint32) {
	layerT, matT, secT := b.t["ChrModelTextureLayer"], b.t["ChrModelMaterial"], b.t["CharComponentTextureSections"]
	layers := map[int64]int{}
	var layerRows []int
	for _, r := range layerT.Where("CharComponentTextureLayoutsID", b.layout) {
		layers[layerT.At(r, "ChrModelTextureTargetID", 0)] = r
		layerRows = append(layerRows, r)
	}
	type size struct{ w, h int }
	materials := map[int64]size{}
	for _, r := range matT.Where("CharComponentTextureLayoutsID", b.layout) {
		materials[matT.Int(r, "TextureType")] = size{int(matT.Int(r, "Width")), int(matT.Int(r, "Height"))}
	}
	secRows := secT.Where("CharComponentTextureLayoutID", b.layout)
	sectionRect := func(r int) Rect {
		return Rect{int(secT.Int(r, "X")), int(secT.Int(r, "Y")), int(secT.Int(r, "Width")), int(secT.Int(r, "Height"))}
	}
	canvases := map[int64]*Canvas{}
	canvas := func(texType int64) *Canvas {
		if cv := canvases[texType]; cv != nil {
			return cv
		}
		s, ok := materials[texType]
		if !ok || s.w <= 0 || s.h <= 0 {
			return nil
		}
		canvases[texType] = NewCanvas(s.w, s.h)
		return canvases[texType]
	}
	raw := map[uint32]uint32{}
	for _, m := range c.materials {
		target, ok := b.get("ChrCustomizationMaterial", m, "ChrModelTextureTargetID")
		res, _ := b.get("ChrCustomizationMaterial", m, "MaterialResourcesID")
		files := b.textureFiles(res)
		lr, ok2 := layers[target]
		if !ok || !ok2 || len(files) == 0 {
			continue
		}
		texType := layerT.Int(lr, "TextureType")
		cv := canvas(texType)
		if cv == nil {
			continue
		}
		mask := layerT.Int(lr, "TextureSectionTypeBitMask")
		rect, found := Rect{0, 0, cv.Width, cv.Height}, mask == -1
		for _, sr := range secRows {
			if !found && (int64(1)<<secT.Int(sr, "SectionType"))&mask != 0 {
				rect, found = sectionRect(sr), true
			}
		}
		if !found {
			continue
		}
		if texType != 1 && texType != 8 {
			raw[uint32(texType)] = uint32(files[0])
		}
		if img := b.image(files[0]); img != nil {
			cv.Set(Layer{Target: int(target), Image: img, Rect: rect, Blend: int(layerT.Int(lr, "BlendMode"))})
		}
	}

	// Equipment textures painted onto the body (ItemDisplayInfoMaterialRes).
	sectionByType := map[int64]int{}
	for _, r := range secRows {
		sectionByType[secT.Int(r, "SectionType")] = r
	}
	layerBySection := map[int64]int{}
	base := -1
	for _, r := range layerRows {
		mask := layerT.Int(r, "TextureSectionTypeBitMask")
		if mask == -1 {
			if base < 0 && layerT.Int(r, "TextureType") == 1 {
				base = r
			}
			continue
		}
		for s := int64(0); s < 9; s++ {
			if _, ok := layerBySection[s]; !ok && (int64(1)<<s)&mask != 0 {
				layerBySection[s] = r
			}
		}
	}
	if base >= 0 {
		for s := int64(0); s < 9; s++ {
			if _, ok := layerBySection[s]; !ok {
				layerBySection[s] = base
			}
		}
	}
	idmr := b.t["ItemDisplayInfoMaterialRes"]
	for _, slot := range sortedKeys(intKeys(items)) {
		it := items[int(slot)]
		slotLayer, ok := slotLayers[it.Slot]
		if !ok {
			continue
		}
		for _, r := range idmr.Where("ItemDisplayInfoID", it.display) {
			section := idmr.Int(r, "ComponentSection")
			fdid := b.bestTexture(b.textureFiles(idmr.Int(r, "MaterialResourcesID")))
			sr, ok := sectionByType[section]
			lr, ok2 := layerBySection[section]
			if fdid == 0 || !ok || !ok2 {
				continue
			}
			cv := canvas(layerT.Int(lr, "TextureType"))
			if cv == nil {
				continue
			}
			blend := int(layerT.Int(lr, "BlendMode"))
			if blend == 0 || blend == 1 {
				blend = 15
			}
			if img := b.image(fdid); img != nil {
				cv.Set(Layer{Target: slotLayer*100 + int(section), Image: img, Rect: sectionRect(sr), Blend: blend})
			}
		}
	}
	out := map[uint32]*image.NRGBA{}
	for t, cv := range canvases {
		out[uint32(t)] = cv.Render()
	}
	return out, raw
}

func intKeys[V any](m map[int]V) map[int64]bool {
	out := map[int64]bool{}
	for k := range m {
		out[int64(k)] = true
	}
	return out
}

// attachmentsBySlot lists M2 attachment IDs for item models of each slot.
var attachmentsBySlot = map[int][]uint32{
	SlotHead: {11}, SlotShoulder: {6}, SlotRightShoulder: {5}, SlotBack: {12},
	SlotMainHand: {1}, SlotOffHand: {2},
}

// modelChoice mirrors DBComponentModelFileData.getModelForRaceGender.
func (b *build) modelChoice(files []int64, position int64) int64 {
	cmfd := b.t["ComponentModelFileData"]
	type cand struct{ fdid, race, g, pos int64 }
	var cs []cand
	for _, f := range files {
		r := cmfd.Lookup(uint32(f))
		c := cand{fdid: f, race: -1, g: -1, pos: -1}
		if r >= 0 {
			c.race, c.g, c.pos = cmfd.Int(r, "RaceID"), cmfd.Int(r, "GenderIndex"), cmfd.Int(r, "PositionIndex")
		}
		if position < 0 || c.pos == position {
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return 0
	}
	if len(cs) == 1 {
		return cs[0].fdid
	}
	for _, race := range []int64{b.race, b.fallback} {
		if race == 0 {
			continue
		}
		for _, g := range []int64{b.sex, 2} {
			for _, c := range cs {
				if c.race == race && c.g == g {
					return c.fdid
				}
			}
		}
	}
	for _, c := range cs {
		if c.race == 0 {
			return c.fdid
		}
	}
	return cs[0].fdid
}

func (b *build) itemModels(items map[int]equipped, body *model.Model, composites map[uint32]*image.NRGBA) {
	idi, mfd, mmr := b.t["ItemDisplayInfo"], b.t["ModelFileData"], b.t["ItemDisplayInfoModelMatRes"]
	modelFiles := func(res int64) []int64 {
		var out []int64
		for _, r := range mfd.Where("ModelResourcesID", res) {
			out = append(out, int64(mfd.ID(r)))
		}
		return out
	}
	for _, slot := range sortedKeys(intKeys(items)) {
		it := items[int(slot)]
		res := [2]int64{idi.At(it.row, "ModelResourcesID", 0), idi.At(it.row, "ModelResourcesID", 1)}
		type part struct{ fdid, index int64 }
		var parts []part
		shoulder := it.Slot == SlotShoulder || it.Slot == SlotRightShoulder
		switch {
		case shoulder && res[0] != 0 && res[0] == res[1]:
			pos := int64(0)
			if it.Slot == SlotRightShoulder {
				pos = 1
			}
			if f := b.modelChoice(modelFiles(res[0]), pos); f != 0 {
				parts = append(parts, part{f, pos})
			}
		case shoulder:
			i := int64(0)
			if it.Slot == SlotRightShoulder {
				i = 1
			}
			if f := b.modelChoice(modelFiles(res[i]), -1); res[i] != 0 && f != 0 {
				parts = append(parts, part{f, i})
			}
		default:
			for i, r := range res {
				if f := b.modelChoice(modelFiles(r), -1); r != 0 && f != 0 {
					parts = append(parts, part{f, int64(i)})
				}
			}
		}
		// Gloves with cuffs cover wrist models (e.g. Golden Mobile Timepiece).
		if hands, ok := items[SlotHands]; ok && it.Slot == SlotWrist && idi.At(hands.row, "GeosetGroup", 0) != 0 {
			continue
		}
		attach := attachmentsBySlot[it.Slot]
		if it.Slot == SlotOffHand || it.Slot == SlotMainHand {
			class, _ := b.get("Item", int64(it.ItemID), "ClassID")
			sub, _ := b.get("Item", int64(it.ItemID), "SubclassID")
			inv, _ := b.get("Item", int64(it.ItemID), "InventoryType")
			switch {
			case inv == 14: // shield
				attach = []uint32{0}
			case class == 2 && sub == 2: // bows are held in the left hand
				attach = []uint32{2}
			}
		}
		for _, p := range parts {
			files := map[uint32]uint32{}
			for _, r := range mmr.Where("ItemDisplayInfoID", it.display) {
				if mmr.Int(r, "ModelIndex") != p.index {
					continue
				}
				t := mmr.Int(r, "TextureType")
				if t < 2 {
					t += 2 // stored as 0-based index into item texture types 2..4
				}
				if fs := b.textureFiles(mmr.Int(r, "MaterialResourcesID")); len(fs) > 0 {
					if _, ok := files[uint32(t)]; !ok {
						files[uint32(t)] = uint32(b.bestTexture(fs))
					}
				}
			}
			if _, ok := files[2]; !ok {
				if fs := b.textureFiles(idi.At(it.row, "ModelMaterialResourcesID", int(p.index))); len(fs) > 0 {
					files[2] = uint32(b.bestTexture(fs))
				}
			}
			skin := map[uint32]*image.NRGBA{1: composites[1], 8: composites[8]}
			a, err := model.LoadWith(b.ctx, b.Files, uint32(p.fdid), model.Options{TypeImages: skin, TypeFiles: files, Cache: b.Cache})
			if err != nil {
				b.warn("slot %d: item %d model %d: %v", it.Slot, it.ItemID, p.fdid, err)
				continue
			}
			b.scene.Warnings = append(b.scene.Warnings, a.Warnings...)
			in := &model.Instance{Name: fmt.Sprintf("slot %d item %d", it.Slot, it.ItemID), Asset: a}
			// ModelResourcesID[i] attaches to attachment i; models without an
			// attachment (e.g. a hood's index 1) are collection models skinned
			// to the body. Each shoulder slot has a single attachment.
			ai := int(p.index)
			if shoulder {
				ai = 0
			}
			if ai < len(attach) {
				at, ok := body.FindAttachment(attach[ai])
				if !ok {
					b.warn("slot %d: body has no attachment %d", it.Slot, attach[ai])
					continue
				}
				in.Attached, in.AttachBone, in.AttachPos = true, at.Bone, at.Position
			} else {
				in.BoneMap = model.MapBones(a.Model, body)
				if mapping, ok := slotGeosets[it.Slot]; ok {
					in.Geosets = map[uint16]bool{}
					for _, m := range mapping {
						in.Geosets[uint16(m[1]*100+1+int(idi.At(it.row, "AttachmentGeosetGroup", m[0])))] = true
					}
				}
			}
			b.scene.Instances = append(b.scene.Instances, in)
		}
	}
}
