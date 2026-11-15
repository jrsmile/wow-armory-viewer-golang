package character

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Item is one visible equipment piece, keyed by inventory slot (1 = head).
type Item struct {
	Slot     int
	ItemID   int
	Modifier int // ItemAppearanceModifierID; -1 = default
}

// Appearance is everything needed to assemble a character model.
type Appearance struct {
	Name    string
	Race    int
	Class   int
	Female  bool
	Choices map[int]int // ChrCustomizationOption -> ChrCustomizationChoice
	Items   []Item
}

// Slot IDs used by the assembler. 30 is the synthetic right shoulder.
const (
	SlotHead          = 1
	SlotShoulder      = 3
	SlotShirt         = 4
	SlotChest         = 5
	SlotWaist         = 6
	SlotLegs          = 7
	SlotFeet          = 8
	SlotWrist         = 9
	SlotHands         = 10
	SlotBack          = 15
	SlotMainHand      = 16
	SlotOffHand       = 17
	SlotTabard        = 19
	SlotRightShoulder = 30
)

// armoryJSON is the Blizzard profile API /appearance document.
type armoryJSON struct {
	Character struct {
		Name string `json:"name"`
	} `json:"character"`
	Race struct {
		ID int `json:"id"`
	} `json:"playable_race"`
	Class struct {
		ID int `json:"id"`
	} `json:"playable_class"`
	Gender struct {
		Type string `json:"type"`
	} `json:"gender"`
	Customizations []struct {
		Option struct {
			ID int `json:"id"`
		} `json:"option"`
		Choice struct {
			ID int `json:"id"`
		} `json:"choice"`
	} `json:"customizations"`
	Items []struct {
		ID                int  `json:"id"`
		InternalSlot      int  `json:"internal_slot_id"`
		Modifier          int  `json:"item_appearance_modifier_id"`
		SecondaryID       int  `json:"secondary_id"`
		SecondaryModifier *int `json:"secondary_item_appearance_modifier_id"`
	} `json:"items"`
}

// ParseArmory converts a Blizzard /appearance JSON document.
func ParseArmory(data []byte) (Appearance, error) {
	var j armoryJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return Appearance{}, fmt.Errorf("appearance JSON: %w", err)
	}
	if j.Race.ID == 0 || j.Gender.Type == "" {
		return Appearance{}, fmt.Errorf("appearance JSON lacks playable_race or gender")
	}
	a := Appearance{Name: j.Character.Name, Race: j.Race.ID, Class: j.Class.ID,
		Female: strings.EqualFold(j.Gender.Type, "FEMALE"), Choices: map[int]int{}}
	for _, c := range j.Customizations {
		a.Choices[c.Option.ID] = c.Choice.ID
	}
	shoulderRight := false
	for _, it := range j.Items {
		slot := it.InternalSlot + 1
		a.Items = append(a.Items, Item{Slot: slot, ItemID: it.ID, Modifier: it.Modifier})
		if slot == SlotShoulder && it.SecondaryID != 0 {
			mod := it.Modifier
			if it.SecondaryModifier != nil {
				mod = *it.SecondaryModifier
			}
			a.Items = append(a.Items, Item{Slot: SlotRightShoulder, ItemID: it.SecondaryID, Modifier: mod})
			shoulderRight = true
		}
	}
	if !shoulderRight {
		for _, it := range a.Items {
			if it.Slot == SlotShoulder {
				a.Items = append(a.Items, Item{Slot: SlotRightShoulder, ItemID: it.ItemID, Modifier: it.Modifier})
				break
			}
		}
	}
	return a, nil
}
