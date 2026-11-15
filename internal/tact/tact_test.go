package tact

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func chunked(payload []byte, compressed bool) ([]byte, Key) {
	chunk := append([]byte{'N'}, payload...)
	if compressed {
		var b bytes.Buffer
		b.WriteByte('Z')
		w := zlib.NewWriter(&b)
		w.Write(payload)
		w.Close()
		chunk = b.Bytes()
	}
	data := make([]byte, 36)
	copy(data, "BLTE")
	binary.BigEndian.PutUint32(data[4:], 36)
	data[8], data[11] = 0x0f, 1
	binary.BigEndian.PutUint32(data[12:], uint32(len(chunk)))
	binary.BigEndian.PutUint32(data[16:], uint32(len(payload)))
	sum := md5.Sum(chunk)
	copy(data[20:], sum[:])
	key := Key(md5.Sum(data))
	return append(data, chunk...), key
}

func TestBLTEAndChecksums(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		data, key := chunked([]byte("native CDN data"), compressed)
		if err := verifyEncoded(data, key); err != nil {
			t.Fatal(err)
		}
		decoded, err := DecodeBLTE(data)
		if err != nil || string(decoded) != "native CDN data" {
			t.Fatalf("%q %v", decoded, err)
		}
		corrupt := bytes.Clone(data)
		corrupt[len(corrupt)-1] ^= 1
		if _, err := DecodeBLTE(corrupt); err == nil {
			t.Fatal("corrupt BLTE accepted")
		}
		for n := 0; n < len(data); n++ {
			if _, err := DecodeBLTE(data[:n]); err == nil {
				t.Fatalf("accepted truncation at %d", n)
			}
		}
	}
	encrypted := append([]byte("BLTE\x00\x00\x00\x00"), 'E', 1)
	if _, err := DecodeBLTE(encrypted); err == nil {
		t.Fatal("encrypted BLTE should fail")
	}
	loose := append([]byte("BLTE\x00\x00\x00\x00N"), []byte("test")...)
	if out, err := DecodeBLTE(loose); err != nil || string(out) != "test" {
		t.Fatal(err)
	}
}

func TestEncodingLookup(t *testing.T) {
	data := make([]byte, 22+32+1024)
	copy(data, "EN")
	data[2], data[3], data[4] = 1, 16, 16
	binary.BigEndian.PutUint16(data[5:], 1)
	binary.BigEndian.PutUint32(data[9:], 1)
	content, encoded := Key{1}, Key{2}
	copy(data[22:], content[:])
	page := data[54:]
	page[0] = 1
	copy(page[6:], content[:])
	copy(page[22:], encoded[:])
	sum := md5.Sum(page)
	copy(data[38:54], sum[:])
	e, err := ParseEncoding(data)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := e.Lookup(content)
	if err != nil || len(keys) != 1 || keys[0] != encoded {
		t.Fatal(keys, err)
	}
	if _, err := e.Lookup(Key{3}); err == nil {
		t.Fatal("accepted absent content key")
	}
	data[54] ^= 1
	if _, err := e.Lookup(content); err == nil {
		t.Fatal("accepted encoding page corruption")
	}
}

func rootFixture(version int) []byte {
	header, block := 0, 12
	if version == 1 {
		header = 12
	}
	if version == 2 {
		header, block = 20, 17
	}
	stride := 28
	if version != 0 {
		stride = 20
	}
	data := make([]byte, header+block+2*stride)
	if header != 0 {
		copy(data, "TSFM")
		if version == 2 {
			binary.LittleEndian.PutUint32(data[4:], 20)
			binary.LittleEndian.PutUint32(data[8:], 2)
			binary.LittleEndian.PutUint32(data[12:], 2)
		} else {
			binary.LittleEndian.PutUint32(data[4:], 2)
		}
	}
	pos := header
	binary.LittleEndian.PutUint32(data[pos:], 2)
	if version == 2 {
		binary.LittleEndian.PutUint32(data[pos+4:], 2)
		binary.LittleEndian.PutUint32(data[pos+8:], 0x10000000)
	} else {
		binary.LittleEndian.PutUint32(data[pos+8:], 2)
		if version == 1 {
			binary.LittleEndian.PutUint32(data[pos+4:], 0x10000000)
		}
	}
	pos += block
	binary.LittleEndian.PutUint32(data[pos:], 100)
	binary.LittleEndian.PutUint32(data[pos+4:], 4)
	data[pos+8] = 1
	second := pos + 24
	if version == 0 {
		second = pos + 32
	}
	data[second] = 2
	return data
}

func TestRootVersionsAndTruncation(t *testing.T) {
	for version := 0; version <= 2; version++ {
		data := rootFixture(version)
		root, err := ParseRoot(data)
		if err != nil {
			t.Fatal(version, err)
		}
		if root[100] != (Key{1}) || root[105] != (Key{2}) {
			t.Fatal(version, root)
		}
		if _, err := ParseRoot(data[:len(data)-1]); err == nil {
			t.Fatal("accepted truncated root")
		}
	}
}

func indexFixture(key Key, size, offset uint32) []byte {
	data := make([]byte, 4096+16+28)
	copy(data, key[:])
	binary.BigEndian.PutUint32(data[16:], size)
	binary.BigEndian.PutUint32(data[20:], offset)
	f := data[len(data)-28:]
	f[8], f[11], f[12], f[13], f[14], f[15] = 1, 4, 4, 4, 16, 8
	binary.LittleEndian.PutUint32(f[16:], 1)
	check := make([]byte, 20)
	copy(check, f[8:20])
	sum := md5.Sum(check)
	copy(f[20:], sum[:8])
	return data
}

func TestArchiveRangeAndCache(t *testing.T) {
	data, key := chunked([]byte("archived content"), true)
	archive := Key{9}.String()
	config := []byte("archives = " + archive + "\n")
	configKey := Key(md5.Sum(config))
	rangeCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case key.String():
			http.NotFound(w, r)
		case configKey.String():
			w.Write(config)
		case archive + ".index":
			w.Write(indexFixture(key, uint32(len(data)), 25))
		case archive:
			if r.Header.Get("Range") != fmt.Sprintf("bytes=25-%d", 24+len(data)) {
				t.Error("wrong archive range")
			}
			rangeCalls++
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 25-%d/10000", 24+len(data)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := New(t.TempDir())
	c.Build = Build{CDN: server.URL, CDNConfig: configKey.String()}
	for range 2 {
		out, err := c.Encoded(context.Background(), key)
		if err != nil || !bytes.Equal(out, data) {
			t.Fatal(err)
		}
	}
	if rangeCalls != 1 {
		t.Fatalf("downloaded cached archive %d times", rangeCalls)
	}
	cached, err := os.ReadFile(filepath.Join(c.Cache, "data", key.String()))
	if err != nil || !bytes.Equal(cached, data) {
		t.Fatal("missing persistent cache", err)
	}
}

func TestRejectIgnoredAndWrongRanges(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusPartialContent} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Range", "bytes 1-2/10")
			w.WriteHeader(status)
			w.Write([]byte("ab"))
		}))
		c := New(t.TempDir())
		_, err := c.fetch(context.Background(), server.URL, "bytes=0-1", 2)
		server.Close()
		if err == nil {
			t.Fatal("accepted ignored or incorrect range")
		}

	}
}

func TestAdvertisedCDNFallback(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "available")
	}))
	defer second.Close()
	c := New(t.TempDir())
	c.Build.CDN = first.URL
	c.origins = []string{first.URL, second.URL}
	result, err := c.fetch(context.Background(), first.URL+"/data/asset", "", 100)
	if err != nil || string(result) != "available" {
		t.Fatal("did not try alternate advertised CDN", string(result), err)
	}
}

func FuzzBLTE(f *testing.F) {
	f.Add([]byte("BLTE\x00\x00\x00\x00Ntest"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) <= 1<<20 {
			_, _ = DecodeBLTE(data)
		}
	})
}
