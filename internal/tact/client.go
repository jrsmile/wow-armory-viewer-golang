package tact

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Build struct {
	Version     string `json:"version"`
	BuildConfig string `json:"build_config"`
	CDNConfig   string `json:"cdn_config"`
	CDN         string `json:"cdn"`
}

type Client struct {
	HTTP      *http.Client
	Cache     string
	Build     Build
	Encoding  *Encoding
	Root      Root
	archives  []string
	locations map[Key]Location
	scanned   int
	origins   []string
	Log       func(string)
	mu        sync.Mutex
}

type Location struct {
	Archive string
	Offset  uint64
	Size    uint64
}

func New(cache string) *Client {
	return &Client{Cache: cache, HTTP: &http.Client{Timeout: 5 * time.Minute}, locations: make(map[Key]Location)}
}

func (c *Client) log(message string) {
	if c.Log != nil {
		c.Log(message)
	}
}

func (c *Client) Discover(ctx context.Context, region string) error {
	switch region {
	case "us", "eu", "kr", "tw":
	default:
		return fmt.Errorf("unsupported CDN region %q", region)
	}
	base := "https://us.version.battle.net/v2/products/wow/"
	versions, err := c.fetch(ctx, base+"versions", "", 1<<20)
	if err != nil {
		return err
	}
	row, err := TableRow(versions, "Region", region)
	if err != nil {
		return err
	}
	cdns, err := c.fetch(ctx, base+"cdns", "", 1<<20)
	if err != nil {
		return err
	}
	cdn, err := TableRow(cdns, "Name", region)
	if err != nil {
		return err
	}
	var origins []string
	for _, server := range strings.Fields(cdn["Servers"]) {
		u, err := url.Parse(server)
		if err == nil && u.Scheme == "https" && u.Host != "" {
			u.RawQuery, u.Path = "", "/"+cdn["Path"]
			origins = append(origins, strings.TrimRight(u.String(), "/"))
		}
	}
	if len(origins) == 0 {
		return fmt.Errorf("Blizzard advertises no HTTPS CDN for %s", region)
	}
	c.origins = origins
	c.Build = Build{row["VersionsName"], row["BuildConfig"], row["CDNConfig"], origins[0]}
	return nil
}

func (c *Client) Open(ctx context.Context) error {
	config, err := c.config(ctx, c.Build.BuildConfig)
	if err != nil {
		return err
	}
	enc := config["encoding"]
	if len(enc) != 2 || len(config["root"]) != 1 {
		return fmt.Errorf("build lacks root or encoding keys")
	}
	c.log("Downloading encoding table (first use can require hundreds of MB)")
	key, err := ParseKey(enc[1])
	if err != nil {
		return err
	}
	encoded, err := c.Encoded(ctx, key)
	if err != nil {
		return err
	}
	data, err := DecodeBLTE(encoded)
	if err != nil {
		return err
	}
	contentKey, err := ParseKey(enc[0])
	if err != nil {
		return err
	}
	if md5.Sum(data) != [16]byte(contentKey) {
		return fmt.Errorf("encoding content checksum mismatch")
	}
	c.Encoding, err = ParseEncoding(data)
	if err != nil {
		return err
	}
	rootKey, err := ParseKey(config["root"][0])
	if err != nil {
		return err
	}
	c.log("Downloading root manifest")
	data, err = c.Content(ctx, rootKey)
	if err != nil {
		return fmt.Errorf("root download: %w", err)
	}
	c.Root, err = ParseRoot(data)
	return err
}

func (c *Client) config(ctx context.Context, hash string) (map[string][]string, error) {
	key, err := ParseKey(hash)
	if err != nil {
		return nil, err
	}
	data, err := c.cached(ctx, "config", key, 1<<20)
	if err != nil {
		return nil, err
	}
	if md5.Sum(data) != [16]byte(key) {
		return nil, fmt.Errorf("CDN config checksum mismatch")
	}
	return ParseConfig(data), nil
}

func (c *Client) File(ctx context.Context, id uint32) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok := c.Root[id]
	if !ok {
		return nil, fmt.Errorf("FileDataID %d not in build %s", id, c.Build.Version)
	}
	return c.Content(ctx, key)
}

// TableFile loads a DB2, zero-filling encrypted BLTE chunks (their sections are
// marked with a TACT key hash in the DB2 header and are skipped by readers).
func (c *Client) TableFile(ctx context.Context, id uint32) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key, ok := c.Root[id]
	if !ok {
		return nil, fmt.Errorf("FileDataID %d not in build %s", id, c.Build.Version)
	}
	ekeys, err := c.Encoding.Lookup(key)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, ekey := range ekeys {
		raw, err := c.Encoded(ctx, ekey)
		if err == nil {
			var data []byte
			var partial bool
			data, partial, err = DecodeBLTEZeroEncrypted(raw)
			if err == nil && !partial && md5.Sum(data) != [16]byte(key) {
				err = fmt.Errorf("content checksum mismatch for %s", key)
			}
			if err == nil {
				return data, nil
			}
		}
		failures = append(failures, err)
	}
	return nil, errors.Join(failures...)
}

func (c *Client) Content(ctx context.Context, key Key) ([]byte, error) {
	if c.Encoding == nil {
		return nil, fmt.Errorf("encoding table is not loaded")
	}
	ekeys, err := c.Encoding.Lookup(key)
	if err != nil {
		return nil, err
	}
	var failures []error
	for _, ekey := range ekeys {
		raw, err := c.Encoded(ctx, ekey)
		if err == nil {
			var data []byte
			data, err = DecodeBLTE(raw)
			if err == nil && md5.Sum(data) != [16]byte(key) {
				err = fmt.Errorf("content checksum mismatch for %s", key)
			}
			if err == nil {
				return data, nil
			}
		}
		failures = append(failures, err)
	}
	return nil, errors.Join(failures...)
}

// Encoded tries loose objects before scanning archive indexes and issuing ranges.
func (c *Client) Encoded(ctx context.Context, key Key) ([]byte, error) {
	if data, err := c.readCache("data", key.String(), MaxFileSize); err == nil {
		if err := verifyEncoded(data, key); err == nil {
			return data, nil
		}
	}
	data, err := c.fetch(ctx, c.objectURL("data", key.String()), "", MaxFileSize)
	if err == nil {
		if err = verifyEncoded(data, key); err != nil {
			return nil, err
		}
		return data, c.writeCache("data", key.String(), data)
	}
	var status *StatusError
	if !errors.As(err, &status) || (status.Code != http.StatusNotFound && status.Code != http.StatusForbidden) {
		return nil, err
	}
	if c.archives == nil {
		config, err := c.config(ctx, c.Build.CDNConfig)
		if err != nil {
			return nil, err
		}
		c.archives = config["archives"]
	}
	for {
		if location, found := c.locations[key]; found {
			if location.Size == 0 || location.Size > MaxFileSize {
				return nil, fmt.Errorf("invalid archive object size")
			}
			rng := fmt.Sprintf("bytes=%d-%d", location.Offset, location.Offset+location.Size-1)
			data, err := c.fetch(ctx, c.objectURL("data", location.Archive), rng, int64(location.Size))
			if err != nil {
				return nil, err
			}
			if uint64(len(data)) != location.Size {
				return nil, fmt.Errorf("short archive range")
			}
			if err := verifyEncoded(data, key); err != nil {
				return nil, err
			}
			return data, c.writeCache("data", key.String(), data)
		}
		if c.scanned >= len(c.archives) {
			return nil, fmt.Errorf("encoding key %s not in loose objects or archives", key)
		}
		archive := c.archives[c.scanned]
		if _, err := ParseKey(archive); err != nil {
			return nil, err
		}
		if c.scanned == 0 || c.scanned%50 == 0 {
			c.log(fmt.Sprintf("Indexing CDN archive %d/%d", c.scanned+1, len(c.archives)))
		}
		data, err := c.cachedName(ctx, "data", archive+".index", 32<<20)
		if err != nil {
			return nil, err
		}
		entries, err := ParseIndex(data, archive)
		if err != nil {
			return nil, fmt.Errorf("archive %s: %w", archive, err)
		}
		for k, v := range entries {
			c.locations[k] = v
		}
		c.scanned++
	}
}

func verifyEncoded(data []byte, key Key) error {
	if len(data) < 8 || string(data[:4]) != "BLTE" {
		return fmt.Errorf("invalid encoded object")
	}
	size := int(binary.BigEndian.Uint32(data[4:8]))
	if size > len(data) || (size != 0 && size < 12) {
		return fmt.Errorf("invalid BLTE header size")
	}
	hashed := data
	if size != 0 {
		hashed = data[:size]
	}
	if md5.Sum(hashed) != [16]byte(key) {
		return fmt.Errorf("encoding checksum mismatch for %s", key)
	}
	return nil
}

func (c *Client) objectURL(kind, key string) string {
	return c.Build.CDN + "/" + kind + "/" + key[:2] + "/" + key[2:4] + "/" + key
}

func (c *Client) cached(ctx context.Context, kind string, key Key, limit int64) ([]byte, error) {
	return c.cachedName(ctx, kind, key.String(), limit)
}

func (c *Client) cachedName(ctx context.Context, kind, name string, limit int64) ([]byte, error) {
	if data, err := c.readCache(kind, name, limit); err == nil {
		return data, nil
	}
	data, err := c.fetch(ctx, c.objectURL(kind, name), "", limit)
	if err != nil {
		return nil, err
	}
	return data, c.writeCache(kind, name, data)
}

func (c *Client) readCache(kind, name string, limit int64) ([]byte, error) {
	f, err := os.Open(filepath.Join(c.Cache, kind, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readLimited(f, limit)
}

func (c *Client) writeCache(kind, name string, data []byte) error {
	dir := filepath.Join(c.Cache, kind)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".download-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := io.Copy(f, bytes.NewReader(data)); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, name))
}

type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("CDN HTTP %d", e.Code) }

func (c *Client) fetch(ctx context.Context, target, rng string, limit int64) ([]byte, error) {
	targets := []string{target}
	if c.Build.CDN != "" && strings.HasPrefix(target, c.Build.CDN+"/") {
		for _, origin := range c.origins {
			if origin != c.Build.CDN {
				targets = append(targets, origin+strings.TrimPrefix(target, c.Build.CDN))
			}
		}
	}
	var last error
	for _, target := range targets {
		data, err := c.fetchOnce(ctx, target, rng, limit)
		if err == nil {
			return data, nil
		}
		last = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, last
}

func (c *Client) fetchOnce(ctx context.Context, target, rng string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "armory-native/0.1")
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	expected := http.StatusOK
	if rng != "" {
		expected = http.StatusPartialContent
	}
	if resp.StatusCode != expected {
		return nil, &StatusError{resp.StatusCode}
	}
	if rng != "" {
		var first, last, total uint64
		if _, err := fmt.Sscanf(resp.Header.Get("Content-Range"), "bytes %d-%d/%d", &first, &last, &total); err != nil ||
			fmt.Sprintf("bytes=%d-%d", first, last) != rng || total <= last {
			return nil, fmt.Errorf("CDN returned a mismatched Content-Range")
		}
	}
	return readLimited(resp.Body, limit)
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return data, err
}

func ParseIndex(data []byte, archive string) (map[Key]Location, error) {
	// TACT archive index v1 has a 28-byte footer with an 8-byte truncated MD5.
	if len(data) < 28 {
		return nil, fmt.Errorf("truncated archive index")
	}
	f := data[len(data)-28:]
	if f[8] != 1 || f[9] != 0 || f[10] != 0 || f[15] != 8 || f[14] != 16 {
		return nil, fmt.Errorf("unsupported archive index footer")
	}
	pageSize, offsetBytes, sizeBytes := int(f[11])*1024, int(f[12]), int(f[13])
	if pageSize == 0 || offsetBytes < 1 || offsetBytes > 8 || sizeBytes < 1 || sizeBytes > 8 {
		return nil, fmt.Errorf("invalid archive index dimensions")
	}
	check := make([]byte, 20)
	copy(check, f[8:20])
	sum := md5.Sum(check)
	if !bytes.Equal(sum[:8], f[20:28]) {
		return nil, fmt.Errorf("archive footer checksum mismatch")
	}
	count := int(binary.LittleEndian.Uint32(f[16:20]))
	stride := 16 + offsetBytes + sizeBytes
	perPage := pageSize / stride
	if perPage == 0 || count > (len(data)-28)/stride {
		return nil, fmt.Errorf("archive index entry count exceeds bounds")
	}
	result := make(map[Key]Location, count)
	for i := 0; i < count; i++ {
		pos := i/perPage*pageSize + i%perPage*stride
		if pos+stride > len(data)-28 {
			return nil, fmt.Errorf("truncated archive page")
		}
		var key Key
		copy(key[:], data[pos:pos+16])
		result[key] = Location{archive, readBE(data[pos+16+sizeBytes : pos+stride]), readBE(data[pos+16 : pos+16+sizeBytes])}
	}
	return result, nil
}

func readBE(data []byte) uint64 {
	var result uint64
	for _, b := range data {
		result = result<<8 | uint64(b)
	}
	return result
}
