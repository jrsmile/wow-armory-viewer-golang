package db2

import (
	"context"
	"embed"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

//go:embed defs/*.dbd
var embedded embed.FS

// FileDataIDs of the tables used for character assembly (from the WoWDBDefs manifest).
var FileDataIDs = map[string]uint32{
	"CharComponentTextureSections": 1360263,
	"ChrCustomizationChoice":       3450554,
	"ChrCustomizationCondModel":    3460182,
	"ChrCustomizationElement":      3512765,
	"ChrCustomizationGeoset":       3456171,
	"ChrCustomizationMaterial":     3459652,
	"ChrCustomizationOption":       3384247,
	"ChrCustomizationReq":          3450453,
	"ChrCustomizationSkinnedModel": 3460183,
	"ChrModel":                     3384313,
	"ChrModelMaterial":             3566562,
	"ChrModelTextureLayer":         3548976,
	"ChrRaces":                     1305311,
	"ChrRaceXChrModel":             3490304,
	"ComponentModelFileData":       1349053,
	"ComponentTextureFileData":     1278239,
	"CreatureDisplayInfo":          1108759,
	"CreatureModelData":            1365368,
	"HelmetGeosetData":             2821752,
	"Item":                         841626,
	"ItemAppearance":               982462,
	"ItemDisplayInfo":              1266429,
	"ItemDisplayInfoMaterialRes":   1280614,
	"ItemDisplayInfoModelMatRes":   4050937,
	"ItemModifiedAppearance":       982457,
	"ItemVisuals":                  1343605,
	"ModelFileData":                1337833,
	"SpellItemEnchantment":         1362771,
	"TextureFileData":              982459,
	"TransmogIllusion":             3580450,
}

const definitionsURL = "https://raw.githubusercontent.com/wowdev/WoWDBDefs/master/definitions/"

// Source provides raw DB2 bytes by FileDataID.
type Source interface {
	TableFile(ctx context.Context, id uint32) ([]byte, error)
}

// DB loads and caches tables.
type DB struct {
	Source    Source
	CacheDir  string // refreshed DBD files are stored here
	HTTP      *http.Client
	Log       func(string)
	Warn      func(string)
	mu        sync.Mutex
	tables    map[string]*Table
	hotfix    *hotfixFile
	hotfixURL func(uint32) string
}

func New(source Source, cacheDir string) *DB {
	return &DB{
		Source: source, CacheDir: cacheDir, HTTP: &http.Client{Timeout: time.Minute},
		tables: map[string]*Table{}, hotfixURL: raidbotsHotfixURL,
	}
}

// Table loads a table by name.
func (d *DB) Table(ctx context.Context, name string) (*Table, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if t, ok := d.tables[name]; ok {
		return t, nil
	}
	id, ok := FileDataIDs[name]
	if !ok {
		return nil, fmt.Errorf("unknown table %s", name)
	}
	data, err := d.Source.TableFile(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	h, err := ReadHeader(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	fields, err := d.definition(ctx, name, h.Layout())
	if err != nil {
		return nil, err
	}
	t, err := Parse(name, data, fields)
	if err != nil {
		return nil, err
	}
	if d.hotfix != nil {
		if n, err := applyHotfixes(t, fields, d.hotfix.tables[h.TableHash]); err != nil {
			if d.Warn != nil {
				d.Warn(fmt.Sprintf("%s hotfixes skipped: %v", name, err))
			}
		} else if n > 0 && d.Log != nil {
			d.Log(fmt.Sprintf("%s: applied %d hotfix row(s)", name, n))
		}
	}
	d.tables[name] = t
	return t, nil
}

func (d *DB) definition(ctx context.Context, name, layout string) ([]Field, error) {
	var texts []string
	if d.CacheDir != "" {
		if b, err := os.ReadFile(filepath.Join(d.CacheDir, name+".dbd")); err == nil {
			texts = append(texts, string(b))
		}
	}
	if b, err := embedded.ReadFile("defs/" + name + ".dbd"); err == nil {
		texts = append(texts, string(b))
	}
	for _, text := range texts {
		if fields, err := ParseDBD(text, layout); err == nil {
			return fields, nil
		}
	}
	text, err := d.download(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("%s layout %s is not in the bundled definitions and refreshing failed: %w", name, layout, err)
	}
	fields, err := ParseDBD(text, layout)
	if err != nil {
		return nil, fmt.Errorf("%s: %w (WoWDBDefs may not support this build yet)", name, err)
	}
	if d.CacheDir != "" && os.MkdirAll(d.CacheDir, 0o755) == nil {
		_ = os.WriteFile(filepath.Join(d.CacheDir, name+".dbd"), []byte(text), 0o644)
	}
	return fields, nil
}

func (d *DB) download(ctx context.Context, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, definitionsURL+name+".dbd", nil)
	if err != nil {
		return "", err
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b), err
}
