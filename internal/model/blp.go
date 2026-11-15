package model

import (
	"fmt"
	"image"
	"image/color"
)

func DecodeBLP(data []byte) (*image.NRGBA, error) {
	if len(data) < 148 || string(data[:4]) != "BLP2" || le.Uint32(data[4:8]) != 1 {
		return nil, fmt.Errorf("only BLP2 textures are supported")
	}
	width, height := int(le.Uint32(data[12:16])), int(le.Uint32(data[16:20]))
	if width < 1 || height < 1 || width > 8192 || height > 8192 || width*height > 16<<20 {
		return nil, fmt.Errorf("BLP dimensions exceed limits")
	}
	offset, size := int(le.Uint32(data[20:24])), int(le.Uint32(data[84:88]))
	if offset > len(data) || size > len(data)-offset {
		return nil, fmt.Errorf("BLP mipmap exceeds bounds")
	}
	mip := data[offset : offset+size]
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	switch data[8] {
	case 1:
		if len(data) < 1172 || len(mip) < width*height {
			return nil, fmt.Errorf("truncated BLP palette or pixels")
		}
		alphaBits := int(data[9])
		if alphaBits != 0 && alphaBits != 1 && alphaBits != 4 && alphaBits != 8 {
			return nil, fmt.Errorf("unsupported palette alpha depth")
		}
		alpha := mip[width*height:]
		if len(alpha) < (width*height*alphaBits+7)/8 {
			return nil, fmt.Errorf("truncated BLP alpha")
		}
		for i := 0; i < width*height; i++ {
			p := 148 + int(mip[i])*4
			a := byte(255)
			if alphaBits > 0 {
				value := (alpha[i*alphaBits/8] >> uint(i*alphaBits%8)) & byte((1<<alphaBits)-1)
				a = byte(int(value) * 255 / ((1 << alphaBits) - 1))
			}
			copy(img.Pix[i*4:i*4+4], []byte{data[p+2], data[p+1], data[p], a})
		}
	case 2:
		mode := int(data[10])
		if mode != 0 && mode != 1 && mode != 7 {
			return nil, fmt.Errorf("unsupported DXT alpha encoding %d", mode)
		}
		blockSize := 8
		if mode != 0 {
			blockSize = 16
		}
		cols, rows := (width+3)/4, (height+3)/4
		if len(mip) < cols*rows*blockSize {
			return nil, fmt.Errorf("truncated DXT blocks")
		}
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				block := mip[(y*cols+x)*blockSize : (y*cols+x+1)*blockSize]
				decodeDXT(img, x*4, y*4, block, mode, data[9] != 0)
			}
		}
	case 3:
		if len(mip) < width*height*4 {
			return nil, fmt.Errorf("truncated BGRA texture")
		}
		for i := 0; i < width*height; i++ {
			p := i * 4
			copy(img.Pix[p:p+4], []byte{mip[p+2], mip[p+1], mip[p], mip[p+3]})
		}
	default:
		return nil, fmt.Errorf("unsupported BLP encoding %d", data[8])
	}
	return img, nil
}

func rgb565(v uint16) color.NRGBA {
	return color.NRGBA{byte(uint32(v>>11) * 255 / 31), byte(uint32((v>>5)&63) * 255 / 63), byte(uint32(v&31) * 255 / 31), 255}
}
func mix(a, b color.NRGBA, wa, wb, div int) color.NRGBA {
	return color.NRGBA{byte((int(a.R)*wa + int(b.R)*wb) / div), byte((int(a.G)*wa + int(b.G)*wb) / div), byte((int(a.B)*wa + int(b.B)*wb) / div), 255}
}
func decodeDXT(img *image.NRGBA, x, y int, block []byte, mode int, hasAlpha bool) {
	colors := block
	if mode != 0 {
		colors = block[8:]
	}
	c0, c1 := le.Uint16(colors), le.Uint16(colors[2:])
	palette := [4]color.NRGBA{rgb565(c0), rgb565(c1)}
	if c0 > c1 || mode != 0 {
		palette[2], palette[3] = mix(palette[0], palette[1], 2, 1, 3), mix(palette[0], palette[1], 1, 2, 3)
	} else {
		palette[2] = mix(palette[0], palette[1], 1, 1, 2)
		palette[3] = color.NRGBA{0, 0, 0, 255}
		if hasAlpha {
			palette[3].A = 0
		}
	}
	indices := le.Uint32(colors[4:])
	var alphas [8]byte
	var alphaIndices uint64
	if mode == 7 {
		alphas[0], alphas[1] = block[0], block[1]
		if alphas[0] > alphas[1] {
			for i := 2; i < 8; i++ {
				alphas[i] = byte(((8-i)*int(alphas[0]) + (i-1)*int(alphas[1])) / 7)
			}
		} else {
			for i := 2; i < 6; i++ {
				alphas[i] = byte(((6-i)*int(alphas[0]) + (i-1)*int(alphas[1])) / 5)
			}
			alphas[6], alphas[7] = 0, 255
		}
		for i := 0; i < 6; i++ {
			alphaIndices |= uint64(block[2+i]) << uint(i*8)
		}
	}
	for i := 0; i < 16; i++ {
		c := palette[(indices>>uint(i*2))&3]
		if mode == 1 {
			c.A = ((block[i/2] >> uint(i%2*4)) & 15) * 17
		}
		if mode == 7 {
			c.A = alphas[(alphaIndices>>uint(i*3))&7]
		}
		if x+i%4 < img.Rect.Dx() && y+i/4 < img.Rect.Dy() {
			img.SetNRGBA(x+i%4, y+i/4, c)
		}
	}
}
