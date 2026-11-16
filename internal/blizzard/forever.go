package blizzard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
)

// DefaultWebBase is the Armory website, which serves each Forever profile as
// public JSON at <profile URL>/model.json.
const DefaultWebBase = "https://worldofwarcraft.blizzard.com"

// foreverSlots maps Armory gear slot types to the 0-based internal slot IDs
// of the /appearance document (only slots with visible models).
var foreverSlots = map[string]int{
	"HEAD": 0, "SHOULDER": 2, "SHIRT": 3, "CHEST": 4, "WAIST": 5, "LEGS": 6, "FEET": 7,
	"WRIST": 8, "HANDS": 9, "BACK": 14, "MAIN_HAND": 15, "OFF_HAND": 16, "TABARD": 18,
}

type foreverProfile struct {
	Character struct {
		Name    string `json:"name"`
		Surname string `json:"surname"`
		Race    struct {
			ID int `json:"id"`
		} `json:"race"`
		Class struct {
			ID int `json:"id"`
		} `json:"class"`
		Gender struct {
			Enum string `json:"enum"`
		} `json:"gender"`
		Gear map[string]struct {
			ID   int `json:"id"`
			Slot struct {
				Type string `json:"type"`
			} `json:"slot"`
			Transmog *struct {
				Item struct {
					ID int `json:"id"`
				} `json:"item"`
			} `json:"transmog"`
		} `json:"gear"`
	} `json:"character"`
}

type appearanceItem struct {
	ID       int `json:"id"`
	Slot     int `json:"internal_slot_id"`
	Modifier int `json:"item_appearance_modifier_id"`
}

type idRef struct {
	ID int `json:"id"`
}

// foreverAppearance reads a World of Warcraft: Forever profile and converts it
// into the /appearance document shape (tagged "game": "forever"). Forever
// profiles publish race, gender and transmogrified gear but no
// customization choices, so the body uses the race's default look.
func (c *Client) foreverAppearance(ctx context.Context, character CharacterURL) ([]byte, error) {
	base := c.WebBase
	if base == "" {
		base = DefaultWebBase
	}
	u := base + "/en-us/" + GameForever + "/" + url.PathEscape(character.Region) + "/armory/character/" + url.PathEscape(character.Name) + "/model.json"
	data, status, err := c.get(ctx, u, "", 8<<20)
	if err != nil {
		return nil, fmt.Errorf("Forever profile request failed: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("Forever profile HTTP %d for %s (check the name-surname slug and region)", status, character.Name)
	}
	var p foreverProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("invalid Forever profile response")
	}
	ch := p.Character
	if ch.Race.ID == 0 || ch.Gender.Enum == "" {
		return nil, fmt.Errorf("Forever profile for %s lacks race or gender", character.Name)
	}
	name := ch.Name
	if ch.Surname != "" {
		name += " " + ch.Surname
	}
	items := []appearanceItem{}
	for _, g := range ch.Gear {
		slot, ok := foreverSlots[g.Slot.Type]
		if !ok || g.ID == 0 {
			continue
		}
		id := g.ID
		if g.Transmog != nil && g.Transmog.Item.ID != 0 {
			id = g.Transmog.Item.ID
		}
		items = append(items, appearanceItem{ID: id, Slot: slot})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Slot < items[j].Slot })
	return json.Marshal(map[string]any{
		"game":           GameForever,
		"character":      map[string]string{"name": name},
		"playable_race":  idRef{ch.Race.ID},
		"playable_class": idRef{ch.Class.ID},
		"gender":         map[string]string{"type": ch.Gender.Enum},
		"customizations": []any{},
		"items":          items,
	})
}
