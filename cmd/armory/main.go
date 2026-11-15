package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jrsmile/armory/internal/blizzard"
	"github.com/jrsmile/armory/internal/character"
	"github.com/jrsmile/armory/internal/db2"
	"github.com/jrsmile/armory/internal/model"
	"github.com/jrsmile/armory/internal/tact"
	"github.com/jrsmile/armory/internal/viewer"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "armory:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	command := "open"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		command, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("armory "+command, flag.ContinueOnError)
	cacheBase, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	cache := fs.String("cache", filepath.Join(cacheBase, "armory", "cdn"), "content-addressed cache directory")
	region := fs.String("region", "us", "Blizzard region: us, eu, kr, tw")
	url := fs.String("url", "", "retail Armory URL")
	id := fs.Uint("model", 0, "M2 FileDataID to view a single raw model instead of a character")
	appearanceFile := fs.String("appearance-json", "", "local Blizzard /appearance JSON document to assemble instead of fetching --url")
	fileID := fs.Uint("file", 0, "FileDataID to extract")
	ekey := fs.String("ekey", "", "encoded CDN key to extract without root/encoding manifests")
	textureID := fs.Uint("texture", 0, "BLP FileDataID for replacement texture type 1")
	output := fs.String("output", "", "new output path; existing files are never overwritten")
	windowed := fs.Bool("windowed", command == "self-test", "start in a window instead of fullscreen")
	screenshot := fs.String("screenshot", "", "save self-test framebuffer PNG to a new file")
	verbose := fs.Bool("v", false, "log CDN downloads and assembly warnings")
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments; use --url for Armory URLs")
	}
	if uint64(*id) > 0xffffffff || uint64(*fileID) > 0xffffffff || uint64(*textureID) > 0xffffffff {
		return fmt.Errorf("FileDataID exceeds uint32")
	}
	api := blizzard.New(os.Getenv("BLIZZARD_CLIENT_ID"), os.Getenv("BLIZZARD_CLIENT_SECRET"))
	if proxy, ok := os.LookupEnv("ARMORY_APPEARANCE_PROXY"); ok {
		api.ProxyURL = proxy
	}
	logf := func(string) {}
	if *verbose {
		logf = func(s string) { fmt.Fprintln(os.Stderr, s) }
	}
	sess := &session{cache: *cache, region: *region, log: logf}
	switch command {
	case "help":
		fmt.Println(`armory open [--url URL | --appearance-json FILE | --model FileDataID [--texture FileDataID]] [--windowed] [-v]
armory profile --url URL
armory cdn-info [--region eu] [--json]
armory fetch --file FileDataID --output new-file
armory fetch --ekey HEX --output new-file
armory self-test [--url URL | --appearance-json FILE | --model FileDataID] [--screenshot new-file.png]

The character's current appearance is read from the Blizzard profile API when
BLIZZARD_CLIENT_ID/BLIZZARD_CLIENT_SECRET are set, otherwise from a public
pass-through (ARMORY_APPEARANCE_PROXY overrides it; empty disables it).
All models, textures and DB2 tables come straight from the Blizzard CDN.`)
		return nil
	case "profile":
		data, err := fetchAppearance(ctx, *url, api)
		if err != nil {
			return err
		}
		app, err := character.ParseArmory(data)
		if err != nil {
			return err
		}
		return printJSON(app)
	case "cdn-info":
		client := tact.New(*cache)
		if err := client.Discover(ctx, *region); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(client.Build)
		}
		fmt.Printf("%s\nBuild config: %s\nCDN config: %s\nCDN: %s\n", client.Build.Version, client.Build.BuildConfig, client.Build.CDNConfig, client.Build.CDN)
		return nil
	case "fetch":
		if *output == "" || (*fileID == 0) == (*ekey == "") {
			return fmt.Errorf("fetch requires --output and exactly one of --file or --ekey")
		}
		if _, err := os.Lstat(*output); err == nil || !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("output already exists or is inaccessible")
		}
		client := tact.New(*cache)
		client.Log = func(s string) { fmt.Fprintln(os.Stderr, s) }
		if err := client.Discover(ctx, *region); err != nil {
			return err
		}
		var data []byte
		if *ekey != "" {
			key, err := tact.ParseKey(*ekey)
			if err != nil {
				return err
			}
			data, err = client.Encoded(ctx, key)
			if err == nil {
				data, err = tact.DecodeBLTE(data)
			}
			if err != nil {
				return err
			}
		} else {
			if err := client.Open(ctx); err != nil {
				return err
			}
			data, err = client.File(ctx, uint32(*fileID))
			if err != nil {
				return err
			}
		}
		if err := writeNew(*output, data); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(map[string]any{"path": *output, "bytes": len(data), "build": client.Build.Version})
		}
		fmt.Printf("Extracted %d bytes from Blizzard CDN into %s\n", len(data), *output)
		return nil
	case "self-test":
		scene := model.SingleScene(testAsset(), "test cube")
		status := "GPU smoke test: generated test cube, NOT a WoW character."
		switch {
		case *id != 0:
			if scene, err = sess.model(ctx, uint32(*id), uint32(*textureID)); err != nil {
				return err
			}
			status = scene.Title
		case *url != "" || *appearanceFile != "":
			data, err := appearanceSource(ctx, *url, *appearanceFile, api)
			if err != nil {
				return err
			}
			if scene, err = sess.character(ctx, data); err != nil {
				return err
			}
			status = scene.Title
		}
		printWarnings(scene)
		return viewer.Run(viewer.Config{Scene: scene, Fullscreen: !*windowed, SelfTest: true, Screenshot: *screenshot, Status: status})
	case "open":
		config := viewer.Config{Fullscreen: !*windowed, URL: *url, Progress: &viewer.Progress{}}
		sess.progress = config.Progress.Report
		config.Lookup = func(raw string) (*model.Scene, error) {
			data, err := fetchAppearance(ctx, raw, api)
			if err != nil {
				return nil, err
			}
			scene, err := sess.character(ctx, data)
			if err == nil {
				printWarnings(scene)
			}
			return scene, err
		}
		switch {
		case *id != 0:
			if config.Scene, err = sess.model(ctx, uint32(*id), uint32(*textureID)); err != nil {
				return err
			}
			config.URL = ""
		case *appearanceFile != "":
			data, err := os.ReadFile(*appearanceFile)
			if err != nil {
				return err
			}
			if config.Scene, err = sess.character(ctx, data); err != nil {
				return err
			}
			printWarnings(config.Scene)
			config.URL = ""
		}
		return viewer.Run(config)
	default:
		return fmt.Errorf("unknown command %q; run armory help", command)
	}
}

// session lazily opens the CDN and DB2 tables once and shares them (and the
// texture cache) between character lookups.
type session struct {
	cache, region string
	log           func(string)
	progress      func(url string, done, total int64, err error)
	once          sync.Once
	err           error
	client        *tact.Client
	db            *db2.DB
	textures      model.TextureCache
	mu            sync.Mutex
}

func (s *session) open(ctx context.Context) error {
	s.once.Do(func() {
		s.client = tact.New(s.cache)
		s.client.Log = s.log
		s.client.Progress = s.progress
		if s.err = s.client.Discover(ctx, s.region); s.err != nil {
			return
		}
		if s.err = s.client.Open(ctx); s.err != nil {
			return
		}
		s.db = db2.New(s.client, filepath.Join(s.cache, "db2"))
	})
	return s.err
}

func (s *session) model(ctx context.Context, id, texture uint32) (*model.Scene, error) {
	if err := s.open(ctx); err != nil {
		return nil, err
	}
	asset, err := model.Load(ctx, s.client, id, texture)
	if err != nil {
		return nil, err
	}
	return model.SingleScene(asset, fmt.Sprintf("FileDataID %d / build %s", id, s.client.Build.Version)), nil
}

func (s *session) character(ctx context.Context, appearance []byte) (*model.Scene, error) {
	app, err := character.ParseArmory(appearance)
	if err != nil {
		return nil, err
	}
	if err := s.open(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := &character.Builder{Files: s.client, DB: s.db, Cache: &s.textures, Log: s.log}
	fmt.Fprintf(os.Stderr, "Assembling %s from build %s...\n", app.Name, s.client.Build.Version)
	return b.Build(ctx, app)
}

func fetchAppearance(ctx context.Context, raw string, api *blizzard.Client) ([]byte, error) {
	ref, err := blizzard.ParseArmoryURL(raw)
	if err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return api.AppearanceJSON(requestContext, ref)
}

func appearanceSource(ctx context.Context, url, file string, api *blizzard.Client) ([]byte, error) {
	if file != "" {
		return os.ReadFile(file)
	}
	return fetchAppearance(ctx, url, api)
}

func printWarnings(scene *model.Scene) {
	for _, w := range scene.Warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeNew(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".armory-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// A hard link publishes atomically without overwriting a concurrent creator.
	return os.Link(f.Name(), path)
}

func testAsset() *model.Asset {
	m := &model.Model{Stand: -1, Min: model.Vec3{-1, -1, -1}, Max: model.Vec3{1, 1, 1}, Textures: []model.Texture{{}}}
	faces := [][4]model.Vec3{
		{{-1, -1, 1}, {1, -1, 1}, {1, 1, 1}, {-1, 1, 1}},
		{{1, -1, -1}, {-1, -1, -1}, {-1, 1, -1}, {1, 1, -1}},
		{{1, -1, 1}, {1, -1, -1}, {1, 1, -1}, {1, 1, 1}},
		{{-1, -1, -1}, {-1, -1, 1}, {-1, 1, 1}, {-1, 1, -1}},
		{{-1, 1, 1}, {1, 1, 1}, {1, 1, -1}, {-1, 1, -1}},
		{{-1, -1, -1}, {1, -1, -1}, {1, -1, 1}, {-1, -1, 1}},
	}
	normals := []model.Vec3{{0, 0, 1}, {0, 0, -1}, {1, 0, 0}, {-1, 0, 0}, {0, 1, 0}, {0, -1, 0}}
	for face, vertices := range faces {
		base := uint32(len(m.Vertices))
		for i, p := range vertices {
			m.Vertices = append(m.Vertices, model.Vertex{Position: p, Normal: normals[face], UV: [2]float32{float32(i & 1), float32(i >> 1)}})
		}
		m.Indices = append(m.Indices, base, base+1, base+2, base, base+2, base+3)
	}
	m.Batches = []model.Batch{{Start: 0, Count: len(m.Indices), Texture: 0}}
	texture := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			texture.SetNRGBA(x, y, color.NRGBA{30, 140, 210, 255})
		}
	}
	return &model.Asset{Model: m, Textures: []*image.NRGBA{texture}}
}
