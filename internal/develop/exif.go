package develop

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"strings"

	"github.com/shaumik/qk-photo-viewer/internal/tiff"
)

// Exported photos keep their shooting data. Losing the camera, the lens,
// the date and the place a shot was taken is the kind of small damage that
// only shows up years later, so export carries it across.
//
// Where that data comes from depends on the file. A JPEG already has an
// EXIF block to copy. A RAW is a TIFF whose tags hold the same values but
// in a structure no JPEG reader can follow, so those get rebuilt into a
// proper EXIF block. Either way the orientation is reset to "normal",
// because develop bakes the rotation into the pixels — leaving the tag
// alone would rotate the picture a second time.

// ExifFor returns an EXIF APP1 segment describing path, ready to splice
// into an encoded JPEG. It returns nil when the file has no metadata worth
// carrying, which is not an error.
func ExifFor(path string, outW, outH int) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	var magic [2]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil {
		return nil
	}
	if magic == [2]byte{0xFF, 0xD8} {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if seg := copyAPP1(data); seg != nil {
			return seg
		}
		return nil
	}
	t, err := tiff.Parse(f, st.Size())
	if err != nil {
		return nil
	}
	return buildAPP1(t, outW, outH)
}

// copyAPP1 lifts an existing EXIF segment out of a JPEG and neutralises
// its orientation tag.
func copyAPP1(data []byte) []byte {
	pos := 2
	for pos+4 <= len(data) {
		if data[pos] != 0xFF {
			return nil
		}
		marker := data[pos+1]
		if marker == 0xDA || marker == 0xD9 {
			return nil
		}
		segLen := int(binary.BigEndian.Uint16(data[pos+2 : pos+4]))
		if segLen < 2 || pos+2+segLen > len(data) {
			return nil
		}
		if marker == 0xE1 && segLen >= 16 &&
			string(data[pos+4:pos+10]) == "Exif\x00\x00" {
			seg := append([]byte(nil), data[pos:pos+2+segLen]...)
			clearOrientation(seg[10:]) // past marker, length and "Exif\0\0"
			return seg
		}
		pos += 2 + segLen
	}
	return nil
}

// clearOrientation rewrites every Orientation tag in a TIFF block to 1.
// It walks the directory chain by hand rather than through the parser
// because it needs the byte offsets, not the values.
func clearOrientation(t []byte) {
	if len(t) < 8 {
		return
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return
	}
	off := int(bo.Uint32(t[4:8]))
	for guard := 0; off > 0 && off+2 <= len(t) && guard < 8; guard++ {
		n := int(bo.Uint16(t[off:]))
		if n <= 0 || off+2+n*12+4 > len(t) {
			return
		}
		for i := 0; i < n; i++ {
			e := off + 2 + i*12
			if bo.Uint16(t[e:]) == tiff.TagOrientation {
				bo.PutUint16(t[e+8:], 1)
			}
		}
		off = int(bo.Uint32(t[off+2+n*12:]))
	}
}

// EXIF tags written on export.
const (
	tagSoftware          = 0x0131
	tagDateTime          = 0x0132
	tagExposureTime      = 0x829A
	tagFNumber           = 0x829D
	tagISO               = 0x8827
	tagExifVersion       = 0x9000
	tagDateTimeOriginal  = 0x9003
	tagDateTimeDigitized = 0x9004
	tagFocalLength       = 0x920A
	tagGPSVersion        = 0x0000
	tagGPSLatRef         = 0x0001
	tagGPSLat            = 0x0002
	tagGPSLngRef         = 0x0003
	tagGPSLng            = 0x0004
	tagPixelXDimension   = 0xA002
	tagPixelYDimension   = 0xA003
)

// buildAPP1 rebuilds a RAW file's shooting data as a JPEG EXIF segment.
func buildAPP1(t *tiff.File, outW, outH int) []byte {
	ifd0 := &exifIFD{}
	exif := &exifIFD{}
	gps := &exifIFD{}

	if v := t.AnyStr(tiff.TagMake); v != "" {
		ifd0.ascii(tiff.TagMake, v)
	}
	if v := t.AnyStr(tiff.TagModel); v != "" {
		ifd0.ascii(tiff.TagModel, v)
	}
	ifd0.short(tiff.TagOrientation, 1) // the rotation is in the pixels now
	ifd0.ascii(tagSoftware, "QK")
	shotAt := t.AnyStr(tagDateTimeOriginal)
	if shotAt == "" {
		shotAt = t.AnyStr(tagDateTime)
	}
	if shotAt != "" {
		ifd0.ascii(tagDateTime, shotAt)
		exif.ascii(tagDateTimeOriginal, shotAt)
		exif.ascii(tagDateTimeDigitized, shotAt)
	}

	exif.undefined(tagExifVersion, []byte("0231"))
	if v := t.AnyFloats(tagExposureTime); len(v) > 0 && v[0] > 0 {
		exif.rational(tagExposureTime, timeFraction(v[0]))
	}
	if v := t.AnyFloats(tagFNumber); len(v) > 0 && v[0] > 0 {
		exif.rational(tagFNumber, [2]uint32{uint32(math.Round(v[0] * 10)), 10})
	}
	if v, ok := t.AnyInt(tagISO); ok && v > 0 {
		exif.short(tagISO, uint16(min(int(v), 65535)))
	}
	if v := t.AnyFloats(tagFocalLength); len(v) > 0 && v[0] > 0 {
		exif.rational(tagFocalLength, [2]uint32{uint32(math.Round(v[0] * 10)), 10})
	}
	if outW > 0 && outH > 0 {
		exif.long(tagPixelXDimension, uint32(outW))
		exif.long(tagPixelYDimension, uint32(outH))
	}

	if d, ok := t.Lookup(tagGPSLat); ok {
		lat, lng := d.Floats(tagGPSLat), d.Floats(tagGPSLng)
		latRef, lngRef := d.Str(tagGPSLatRef), d.Str(tagGPSLngRef)
		if len(lat) >= 3 && len(lng) >= 3 && latRef != "" && lngRef != "" {
			gps.bytes(tagGPSVersion, []byte{2, 3, 0, 0})
			gps.ascii(tagGPSLatRef, latRef)
			gps.rationals(tagGPSLat, dms(lat))
			gps.ascii(tagGPSLngRef, lngRef)
			gps.rationals(tagGPSLng, dms(lng))
		}
	}

	payload := assembleTIFF(ifd0, exif, gps)
	if payload == nil {
		return nil
	}
	seg := make([]byte, 0, len(payload)+12)
	seg = append(seg, 0xFF, 0xE1)
	seg = binary.BigEndian.AppendUint16(seg, uint16(len(payload)+8))
	seg = append(seg, "Exif\x00\x00"...)
	seg = append(seg, payload...)
	return seg
}

// timeFraction expresses a shutter speed the way a camera does: 1/2000
// rather than 0.0005.
func timeFraction(v float64) [2]uint32 {
	if v < 1 {
		den := math.Round(1 / v)
		if den > 0 && den < 1e9 {
			return [2]uint32{1, uint32(den)}
		}
	}
	return [2]uint32{uint32(math.Round(v * 10)), 10}
}

func dms(v []float64) [][2]uint32 {
	out := make([][2]uint32, 3)
	for i := 0; i < 3; i++ {
		out[i] = [2]uint32{uint32(math.Round(v[i] * 1000)), 1000}
	}
	return out
}

/* ---------- a very small TIFF writer ---------- */

type exifEntry struct {
	tag  uint16
	typ  uint16
	cnt  uint32
	data []byte
}

type exifIFD struct{ entries []exifEntry }

func (d *exifIFD) add(e exifEntry) { d.entries = append(d.entries, e) }

func (d *exifIFD) ascii(tag uint16, s string) {
	b := append([]byte(strings.TrimRight(s, "\x00")), 0)
	d.add(exifEntry{tag, tiff.TASCII, uint32(len(b)), b})
}

func (d *exifIFD) short(tag uint16, v uint16) {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, v)
	d.add(exifEntry{tag, tiff.TShort, 1, b})
}

func (d *exifIFD) long(tag uint16, v uint32) {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	d.add(exifEntry{tag, tiff.TLong, 1, b})
}

func (d *exifIFD) rational(tag uint16, v [2]uint32) {
	d.rationals(tag, [][2]uint32{v})
}

func (d *exifIFD) rationals(tag uint16, vs [][2]uint32) {
	b := make([]byte, 8*len(vs))
	for i, v := range vs {
		binary.LittleEndian.PutUint32(b[i*8:], v[0])
		binary.LittleEndian.PutUint32(b[i*8+4:], v[1])
	}
	d.add(exifEntry{tag, tiff.TRational, uint32(len(vs)), b})
}

func (d *exifIFD) undefined(tag uint16, v []byte) {
	d.add(exifEntry{tag, tiff.TUndefined, uint32(len(v)), append([]byte(nil), v...)})
}

func (d *exifIFD) bytes(tag uint16, v []byte) {
	d.add(exifEntry{tag, tiff.TByte, uint32(len(v)), append([]byte(nil), v...)})
}

func (d *exifIFD) size() int { return 2 + 12*len(d.entries) + 4 }

// assembleTIFF lays out the directories and their payloads, wiring up the
// pointers from IFD0 to the EXIF and GPS directories.
func assembleTIFF(ifd0, exif, gps *exifIFD) []byte {
	if len(exif.entries) > 0 {
		ifd0.long(tiff.TagExifIFD, 0) // patched below
	}
	if len(gps.entries) > 0 {
		ifd0.long(tiff.TagGPSIFD, 0)
	}
	if len(ifd0.entries) == 0 {
		return nil
	}
	sortEntries(ifd0)
	sortEntries(exif)
	sortEntries(gps)

	off := 8
	ifd0Off := off
	off += ifd0.size()
	exifOff := off
	if len(exif.entries) > 0 {
		off += exif.size()
	}
	gpsOff := off
	if len(gps.entries) > 0 {
		off += gps.size()
	}
	// Out-of-line payloads follow the directories, in the same order.
	dataOff := off
	for _, d := range []*exifIFD{ifd0, exif, gps} {
		for _, e := range d.entries {
			if len(e.data) > 4 {
				off += len(e.data) + len(e.data)&1
			}
		}
	}

	if len(exif.entries) > 0 {
		patchLong(ifd0, tiff.TagExifIFD, uint32(exifOff))
	}
	if len(gps.entries) > 0 {
		patchLong(ifd0, tiff.TagGPSIFD, uint32(gpsOff))
	}

	var out bytes.Buffer
	out.WriteString("II")
	binary.Write(&out, binary.LittleEndian, uint16(42))
	binary.Write(&out, binary.LittleEndian, uint32(ifd0Off))

	cursor := dataOff
	var data bytes.Buffer
	writeIFD := func(d *exifIFD) {
		if len(d.entries) == 0 {
			return
		}
		binary.Write(&out, binary.LittleEndian, uint16(len(d.entries)))
		for _, e := range d.entries {
			binary.Write(&out, binary.LittleEndian, e.tag)
			binary.Write(&out, binary.LittleEndian, e.typ)
			binary.Write(&out, binary.LittleEndian, e.cnt)
			if len(e.data) > 4 {
				binary.Write(&out, binary.LittleEndian, uint32(cursor))
				data.Write(e.data)
				cursor += len(e.data)
				if len(e.data)&1 == 1 {
					data.WriteByte(0)
					cursor++
				}
			} else {
				var inline [4]byte
				copy(inline[:], e.data)
				out.Write(inline[:])
			}
		}
		binary.Write(&out, binary.LittleEndian, uint32(0))
	}
	writeIFD(ifd0)
	writeIFD(exif)
	writeIFD(gps)
	out.Write(data.Bytes())
	return out.Bytes()
}

// sortEntries puts a directory's tags in ascending order, as the spec
// requires and as strict readers assume.
func sortEntries(d *exifIFD) {
	for i := 1; i < len(d.entries); i++ {
		for j := i; j > 0 && d.entries[j].tag < d.entries[j-1].tag; j-- {
			d.entries[j], d.entries[j-1] = d.entries[j-1], d.entries[j]
		}
	}
}

func patchLong(d *exifIFD, tag uint16, v uint32) {
	for i := range d.entries {
		if d.entries[i].tag == tag {
			binary.LittleEndian.PutUint32(d.entries[i].data, v)
			return
		}
	}
}

/* ---------- encoding ---------- */

// DefaultQuality is what export writes at: visually indistinguishable from
// the source at a size people can actually send.
const DefaultQuality = 92

// EncodeJPEG writes img as a JPEG, splicing an EXIF segment in directly
// after the start-of-image marker if one is supplied.
func EncodeJPEG(img image.Image, quality int, exifAPP1 []byte) ([]byte, error) {
	var body bytes.Buffer
	if err := jpeg.Encode(&body, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("develop: encode JPEG: %w", err)
	}
	b := body.Bytes()
	if len(exifAPP1) == 0 || len(b) < 2 {
		return b, nil
	}
	out := make([]byte, 0, len(b)+len(exifAPP1))
	out = append(out, b[:2]...) // SOI
	out = append(out, exifAPP1...)
	out = append(out, b[2:]...)
	return out, nil
}
