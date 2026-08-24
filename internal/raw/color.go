package raw

import (
	"strings"

	"github.com/shaumik/qk-photo-viewer/internal/tiff"
)

// A sensor's red, green and blue filters are not sRGB's primaries — they
// are whatever transmits usefully through silicon. Turning sensor readings
// into colour a screen can show means a 3x3 change of basis, and the
// matrix is a property of the camera model.
//
// Files that carry a DNG colour matrix tell us theirs; the table below
// covers Sony bodies that do not. An unlisted body falls back to a generic
// modern-Sony matrix and is flagged Approximate, because being slightly
// wrong quietly is worse than being slightly wrong out loud.

// xyzFromSRGB converts linear sRGB to CIE XYZ (D65).
var xyzFromSRGB = [9]float64{
	0.412453, 0.357580, 0.180423,
	0.212671, 0.715160, 0.072169,
	0.019334, 0.119193, 0.950227,
}

// camFromXYZ entries map CIE XYZ to camera RGB, scaled by 1e4 — the same
// convention DNG's ColorMatrix tag and dcraw's table use. Keys match the
// EXIF Model string exactly.
var camFromXYZ = map[string][9]float64{
	"ILCE-7":    {5271, -712, -347, -6153, 13653, 2763, -1601, 2366, 7242},
	"ILCE-7M2":  {5271, -712, -347, -6153, 13653, 2763, -1601, 2366, 7242},
	"ILCE-7M3":  {7374, -2389, -551, -5435, 13162, 2519, -1006, 1795, 6552},
	"ILCE-7C":   {7374, -2389, -551, -5435, 13162, 2519, -1006, 1795, 6552},
	"ILCE-7R":   {4913, -541, -202, -6130, 13513, 2906, -1564, 2151, 7183},
	"ILCE-7RM2": {6629, -1900, -483, -4618, 12349, 2550, -622, 1381, 6514},
	"ILCE-7RM3": {6640, -1847, -503, -5238, 13010, 2474, -993, 1673, 6527},
	"ILCE-7RM4": {7662, -2686, -660, -5240, 12965, 2530, -796, 1508, 6167},
	"ILCE-7S":   {5838, -1430, -246, -3497, 11477, 2297, -748, 1885, 5778},
	"ILCE-7SM2": {5838, -1430, -246, -3497, 11477, 2297, -748, 1885, 5778},
	"ILCE-9":    {6389, -1703, -378, -4562, 12265, 2587, -670, 1489, 6550},
	"ILCE-6000": {5991, -1456, -455, -4764, 12135, 2980, -707, 1425, 6701},
	"ILCE-6300": {5973, -1695, -419, -3826, 11797, 2293, -639, 1398, 5789},
	"ILCE-6500": {5973, -1695, -419, -3826, 11797, 2293, -639, 1398, 5789},
	"ILCE-6400": {6446, -1358, -815, -4126, 12026, 2352, -543, 1146, 7398},
}

// genericSony stands in for bodies the table does not list. It is a modern
// full-frame Sony matrix: close enough that colour looks right, not exact.
var genericSony = [9]float64{7374, -2389, -551, -5435, 13162, 2519, -1006, 1795, 6552}

// colorMatrix returns the camera-RGB to linear-sRGB matrix, and whether it
// is a generic stand-in rather than this body's own.
func colorMatrix(t *tiff.File, model string) ([9]float64, bool) {
	// The file's own matrix beats any table.
	for _, tag := range []uint16{tiff.TagDNGColorMatrix2, tiff.TagDNGColorMatrix1} {
		if v := t.AnyFloats(tag); len(v) >= 9 {
			var m [9]float64
			copy(m[:], v[:9])
			if inv, ok := srgbFromCamera(m); ok {
				return inv, false
			}
		}
	}
	key := strings.TrimSpace(model)
	if m, ok := camFromXYZ[key]; ok {
		if inv, ok := srgbFromCamera(scale1e4(m)); ok {
			return inv, false
		}
	}
	if inv, ok := srgbFromCamera(scale1e4(genericSony)); ok {
		return inv, true
	}
	return [9]float64{1, 0, 0, 0, 1, 0, 0, 0, 1}, true
}

func scale1e4(m [9]float64) [9]float64 {
	var out [9]float64
	for i, v := range m {
		out[i] = v / 10000
	}
	return out
}

// srgbFromCamera turns an XYZ-to-camera matrix into the camera-to-sRGB one
// the pipeline wants. Rows are normalised to sum to 1 first, which is what
// makes a neutral camera reading come out neutral on screen; the white
// balance multipliers handle the illuminant separately.
func srgbFromCamera(camXYZ [9]float64) ([9]float64, bool) {
	var camRGB [9]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			s := 0.0
			for k := 0; k < 3; k++ {
				s += camXYZ[i*3+k] * xyzFromSRGB[k*3+j]
			}
			camRGB[i*3+j] = s
		}
	}
	for i := 0; i < 3; i++ {
		sum := camRGB[i*3] + camRGB[i*3+1] + camRGB[i*3+2]
		if sum == 0 {
			return [9]float64{}, false
		}
		for j := 0; j < 3; j++ {
			camRGB[i*3+j] /= sum
		}
	}
	return invert3(camRGB)
}

func invert3(m [9]float64) ([9]float64, bool) {
	a, b, c := m[0], m[1], m[2]
	d, e, f := m[3], m[4], m[5]
	g, h, i := m[6], m[7], m[8]
	det := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	if det > -1e-12 && det < 1e-12 {
		return [9]float64{}, false
	}
	return [9]float64{
		(e*i - f*h) / det, (c*h - b*i) / det, (b*f - c*e) / det,
		(f*g - d*i) / det, (a*i - c*g) / det, (c*d - a*f) / det,
		(d*h - e*g) / det, (b*g - a*h) / det, (a*e - b*d) / det,
	}, true
}
