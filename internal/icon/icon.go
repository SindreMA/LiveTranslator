package icon

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
)

// GenerateICO creates a simple 32x32 BMP-based ICO with a speech-bubble icon.
// Uses classic BMP format inside ICO (not PNG) for maximum compatibility.
func GenerateICO() []byte {
	size := 32
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	teal := color.RGBA{R: 0, G: 180, B: 180, A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}

	// Draw rounded rectangle (speech bubble).
	for y := 4; y < 24; y++ {
		for x := 3; x < 29; x++ {
			corner := false
			if (x < 6 || x > 25) && (y < 7 || y > 20) {
				dx, dy := 0, 0
				if x < 6 {
					dx = 6 - x
				}
				if x > 25 {
					dx = x - 25
				}
				if y < 7 {
					dy = 7 - y
				}
				if y > 20 {
					dy = y - 20
				}
				if dx*dx+dy*dy > 9 {
					corner = true
				}
			}
			if !corner {
				img.SetRGBA(x, y, teal)
			}
		}
	}

	// Draw speech bubble tail.
	for i := 0; i < 5; i++ {
		for x := 10 - i; x <= 10+i; x++ {
			if x >= 8 && x <= 14 {
				img.SetRGBA(x, 24+i, teal)
			}
		}
	}

	// Draw "T" letter.
	for x := 10; x < 23; x++ {
		img.SetRGBA(x, 8, white)
		img.SetRGBA(x, 9, white)
	}
	for y := 8; y < 21; y++ {
		img.SetRGBA(15, y, white)
		img.SetRGBA(16, y, white)
	}

	return bmpICO(img)
}

// bmpICO creates a proper ICO file with BMP pixel data (not PNG).
// This is the classic ICO format compatible with CreateIconFromResourceEx.
func bmpICO(img *image.RGBA) []byte {
	bounds := img.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	// ICO BMP uses height = 2*h (XOR mask + AND mask).
	bmpInfoHeaderSize := 40
	pixelDataSize := w * h * 4             // 32-bit BGRA
	andMaskRowBytes := ((w + 31) / 32) * 4 // 1-bit AND mask, padded to 4 bytes
	andMaskSize := andMaskRowBytes * h
	imageDataSize := bmpInfoHeaderSize + pixelDataSize + andMaskSize
	dataOffset := 6 + 16 // ICO header (6) + 1 directory entry (16)

	var buf bytes.Buffer

	// === ICO Header ===
	binary.Write(&buf, binary.LittleEndian, uint16(0)) // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // type: icon
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // image count

	// === ICO Directory Entry ===
	buf.WriteByte(byte(w))  // width (0 means 256)
	buf.WriteByte(byte(h))  // height
	buf.WriteByte(0)        // color palette count
	buf.WriteByte(0)        // reserved
	binary.Write(&buf, binary.LittleEndian, uint16(1))                  // color planes
	binary.Write(&buf, binary.LittleEndian, uint16(32))                 // bits per pixel
	binary.Write(&buf, binary.LittleEndian, uint32(imageDataSize))      // image data size
	binary.Write(&buf, binary.LittleEndian, uint32(dataOffset))         // offset to image data

	// === BITMAPINFOHEADER ===
	binary.Write(&buf, binary.LittleEndian, uint32(bmpInfoHeaderSize)) // header size
	binary.Write(&buf, binary.LittleEndian, int32(w))                  // width
	binary.Write(&buf, binary.LittleEndian, int32(h*2))                // height (XOR + AND)
	binary.Write(&buf, binary.LittleEndian, uint16(1))                 // planes
	binary.Write(&buf, binary.LittleEndian, uint16(32))                // bits per pixel
	binary.Write(&buf, binary.LittleEndian, uint32(0))                 // compression (none)
	binary.Write(&buf, binary.LittleEndian, uint32(pixelDataSize+andMaskSize)) // image size
	binary.Write(&buf, binary.LittleEndian, int32(0))                  // X pixels per meter
	binary.Write(&buf, binary.LittleEndian, int32(0))                  // Y pixels per meter
	binary.Write(&buf, binary.LittleEndian, uint32(0))                 // colors used
	binary.Write(&buf, binary.LittleEndian, uint32(0))                 // important colors

	// === XOR Pixel Data (BGRA, bottom-up) ===
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			buf.WriteByte(byte(b >> 8)) // B
			buf.WriteByte(byte(g >> 8)) // G
			buf.WriteByte(byte(r >> 8)) // R
			buf.WriteByte(byte(a >> 8)) // A
		}
	}

	// === AND Mask (1-bit, bottom-up) — all 0 since alpha channel handles transparency ===
	for y := 0; y < h; y++ {
		for x := 0; x < andMaskRowBytes; x++ {
			buf.WriteByte(0x00)
		}
	}

	return buf.Bytes()
}
