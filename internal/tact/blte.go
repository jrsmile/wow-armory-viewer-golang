package tact

import (
	"bytes"
	"compress/zlib"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"io"
)

const MaxFileSize = 512 << 20

// DecodeBLTE rejects encrypted chunks instead of returning invented zero data.
func DecodeBLTE(data []byte) ([]byte, error) {
	out, _, err := decodeBLTE(data, false)
	return out, err
}

// DecodeBLTEZeroEncrypted replaces encrypted chunks with zeros of their declared
// size. It is only meant for DB2 tables, whose encrypted sections are skipped.
func DecodeBLTEZeroEncrypted(data []byte) ([]byte, bool, error) {
	return decodeBLTE(data, true)
}

func decodeBLTE(data []byte, zeroEncrypted bool) ([]byte, bool, error) {
	partial := false
	if len(data) < 9 || string(data[:4]) != "BLTE" {
		return nil, false, fmt.Errorf("invalid BLTE signature or truncated file")
	}
	header := int(binary.BigEndian.Uint32(data[4:8]))
	if header == 0 {
		out, err := decodeChunk(data[8:], MaxFileSize)
		return out, false, err
	}
	if header < 12 || header > len(data) || data[8] != 0x0f {
		return nil, false, fmt.Errorf("invalid BLTE header")
	}
	count := int(data[9])<<16 | int(data[10])<<8 | int(data[11])
	if count == 0 || header != 12+24*count {
		return nil, false, fmt.Errorf("invalid BLTE chunk count")
	}
	var result bytes.Buffer
	offset := header
	for i := 0; i < count; i++ {
		entry := data[12+24*i : 12+24*(i+1)]
		compressed := int(binary.BigEndian.Uint32(entry))
		size := int(binary.BigEndian.Uint32(entry[4:]))
		if compressed < 1 || compressed > len(data)-offset || size > MaxFileSize-result.Len() {
			return nil, false, fmt.Errorf("BLTE chunk %d exceeds file bounds", i)
		}
		chunk := data[offset : offset+compressed]
		sum := md5.Sum(chunk)
		if !bytes.Equal(sum[:], entry[8:24]) {
			return nil, false, fmt.Errorf("BLTE chunk %d checksum mismatch", i)
		}
		var decoded []byte
		var err error
		if zeroEncrypted && chunk[0] == 'E' {
			decoded, partial = make([]byte, size), true
		} else if decoded, err = decodeChunk(chunk, size); err != nil {
			return nil, false, fmt.Errorf("BLTE chunk %d: %w", i, err)
		}
		if len(decoded) != size {
			return nil, false, fmt.Errorf("BLTE chunk %d size mismatch", i)
		}
		result.Write(decoded)
		offset += compressed
	}
	if offset != len(data) {
		return nil, false, fmt.Errorf("trailing BLTE data")
	}
	return result.Bytes(), partial, nil
}

func decodeChunk(chunk []byte, limit int) ([]byte, error) {
	if len(chunk) == 0 {
		return nil, fmt.Errorf("empty BLTE chunk")
	}
	switch chunk[0] {
	case 'N':
		if len(chunk)-1 > limit {
			return nil, fmt.Errorf("uncompressed chunk exceeds limit")
		}
		return bytes.Clone(chunk[1:]), nil
	case 'Z':
		reader, err := zlib.NewReader(bytes.NewReader(chunk[1:]))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		result, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
		if len(result) > limit {
			return nil, fmt.Errorf("decompressed chunk exceeds limit")
		}
		return result, err
	case 'E':
		return nil, fmt.Errorf("encrypted asset: a Blizzard TACT key is required (not implemented)")
	default:
		return nil, fmt.Errorf("unsupported BLTE compression mode %q", chunk[0])
	}
}
