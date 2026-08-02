// Package previewtest builds tiny synthetic camera files for tests: valid
// TIFF/ARW-shaped containers with embedded JPEG blobs at known offsets.
package previewtest

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
)

// JPEGBlob returns a fake JPEG of exactly n bytes (n >= 4): SOI marker,
// fill bytes, EOI marker. Distinct fills let tests tell blobs apart.
func JPEGBlob(n int, fill byte) []byte {
	b := make([]byte, n)
	b[0], b[1] = 0xFF, 0xD8
	for i := 2; i < n-2; i++ {
		b[i] = fill
	}
	b[n-2], b[n-1] = 0xFF, 0xD9
	return b
}

// RealJPEG encodes an actual decodable JPEG (a hue ramp with an index-tinted
// stripe) — for fixtures that a browser has to render, e.g. demo shoots.
func RealJPEG(w, h, idx int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(40 + 180*x/max(w, 1)),
				G: uint8(30 + (idx*47)%180),
				B: uint8(200 - 150*y/max(h, 1)),
				A: 255,
			})
		}
	}
	for y := h / 3; y < h/3+max(h/10, 2) && y < h; y++ { // per-index stripe
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8((idx * 83) % 255), G: 240, B: 90, A: 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 82})
	return b.Bytes()
}

// ARWBytes builds a little-endian TIFF resembling an ARW: IFD0 carries the
// thumbnail via JPEGInterchangeFormat and points at a SubIFD carrying the
// large preview the same way.
func ARWBytes(thumb, preview []byte) []byte {
	const (
		hdrSize  = 8
		ifd0Off  = int64(hdrSize)
		ifd0Size = 2 + 3*12 + 4 // 3 entries + next pointer
		subOff   = ifd0Off + ifd0Size
		subSize  = 2 + 2*12 + 4
		thumbOff = subOff + subSize
	)
	prevOff := thumbOff + int64(len(thumb))

	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("II")
	binary.Write(&b, le, uint16(42))
	binary.Write(&b, le, uint32(ifd0Off))

	entry := func(tag uint16, val int64) {
		binary.Write(&b, le, tag)
		binary.Write(&b, le, uint16(4)) // LONG
		binary.Write(&b, le, uint32(1))
		binary.Write(&b, le, uint32(val))
	}

	binary.Write(&b, le, uint16(3)) // IFD0: 3 entries
	entry(0x014A, subOff)           // SubIFDs -> preview IFD
	entry(0x0201, thumbOff)         // thumbnail offset
	entry(0x0202, int64(len(thumb)))
	binary.Write(&b, le, uint32(0)) // no chained IFD

	binary.Write(&b, le, uint16(2)) // SubIFD: 2 entries
	entry(0x0201, prevOff)
	entry(0x0202, int64(len(preview)))
	binary.Write(&b, le, uint32(0))

	b.Write(thumb)
	b.Write(preview)
	return b.Bytes()
}

// WriteARW writes a synthetic ARW to path.
func WriteARW(path string, thumb, preview []byte) error {
	return os.WriteFile(path, ARWBytes(thumb, preview), 0o644)
}

// JPEGWithExifThumb builds a JPEG shot whose APP1 EXIF block embeds a
// thumbnail via an IFD, followed by fake image data.
func JPEGWithExifThumb(thumb []byte, body int) []byte {
	// EXIF payload: TIFF header + one IFD + the thumb blob.
	const (
		tiffHdr  = 8
		ifdOff   = int64(tiffHdr)
		ifdSize  = 2 + 2*12 + 4
		thumbOff = ifdOff + ifdSize
	)
	var tiff bytes.Buffer
	le := binary.LittleEndian
	tiff.WriteString("II")
	binary.Write(&tiff, le, uint16(42))
	binary.Write(&tiff, le, uint32(ifdOff))
	entry := func(tag uint16, val int64) {
		binary.Write(&tiff, le, tag)
		binary.Write(&tiff, le, uint16(4))
		binary.Write(&tiff, le, uint32(1))
		binary.Write(&tiff, le, uint32(val))
	}
	binary.Write(&tiff, le, uint16(2))
	entry(0x0201, thumbOff)
	entry(0x0202, int64(len(thumb)))
	binary.Write(&tiff, le, uint32(0))
	tiff.Write(thumb)

	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8}) // SOI
	payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	b.Write([]byte{0xFF, 0xE1}) // APP1
	binary.Write(&b, binary.BigEndian, uint16(len(payload)+2))
	b.Write(payload)
	b.Write([]byte{0xFF, 0xDA, 0x00, 0x02}) // SOS: image data "begins"
	for i := 0; i < body; i++ {
		b.WriteByte(0x55)
	}
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}
