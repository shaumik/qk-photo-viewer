package raw

import "fmt"

// Sony's lossy compressed layout ("ARW 2.x") is a fixed-rate scheme: every
// 16 bytes hold 16 pixels of a single colour — every other column, since
// the CFA alternates — as an 11-bit minimum and maximum plus fourteen
// 7-bit values interpolated between them.
//
// Each 128-bit group reads:
//
//	bits  0..10  max value
//	bits 11..21  min value
//	bits 22..25  which of the 16 pixels is the max
//	bits 26..29  which is the min
//	bits 30..     fourteen 7-bit deltas, one per remaining pixel
//
// The deltas are shifted left by however much the group's range needs, so
// a flat patch keeps its precision and a high-contrast one spends it on
// range. Reconstructed samples run back through the camera's tone curve to
// land in the sensor's original 14-bit domain.
const (
	arw2GroupBytes = 16 // bytes per group
	arw2GroupPix   = 16 // pixels per group
	arw2DeltaStart = 30 // first bit of the 7-bit deltas
	arw2Max        = 0x7FF
)

func decodeARW2(buf []byte, w, h int, curve *[4096]uint16) ([]uint16, error) {
	stride := w // the packed layout averages one byte per pixel
	if len(buf) < stride*h {
		return nil, fmt.Errorf("%w: %d bytes is too little for a %dx%d ARW2 frame",
			ErrUnsupported, len(buf), w, h)
	}
	out := make([]uint16, w*h)
	// The 7-bit reader peeks two bytes at a bit offset that can start in a
	// group's final byte, so rows get scratch padding rather than a bounds
	// check in the innermost loop.
	row := make([]byte, stride+2)
	for y := 0; y < h; y++ {
		copy(row, buf[y*stride:y*stride+stride])
		row[stride], row[stride+1] = 0, 0
		decodeARW2Row(row, out[y*w:y*w+w], w, curve)
	}
	return out, nil
}

func decodeARW2Row(row []byte, out []uint16, w int, curve *[4096]uint16) {
	var pix [arw2GroupPix]int
	col, dp := 0, 0
	// Sony leaves the last few columns of each row outside the coded data;
	// they are masked border pixels the crop discards.
	for col < w-30 && dp+arw2GroupBytes <= len(row)-2 {
		v := uint32(row[dp]) | uint32(row[dp+1])<<8 | uint32(row[dp+2])<<16 | uint32(row[dp+3])<<24
		maxv := int(v & arw2Max)
		minv := int(v >> 11 & arw2Max)
		imax := int(v >> 22 & 0x0F)
		imin := int(v >> 26 & 0x0F)

		sh := 0
		for sh < 4 && (0x80<<uint(sh)) <= maxv-minv {
			sh++
		}
		bit := arw2DeltaStart
		for i := 0; i < arw2GroupPix; i++ {
			switch i {
			case imax:
				pix[i] = maxv
			case imin:
				pix[i] = minv
			default:
				b := dp + bit>>3
				raw := (int(row[b]) | int(row[b+1])<<8) >> uint(bit&7) & 0x7F
				p := raw<<uint(sh) + minv
				if p > arw2Max {
					p = arw2Max
				}
				pix[i] = p
				bit += 7
			}
		}
		for i := 0; i < arw2GroupPix; i++ {
			out[col] = curve[pix[i]<<1]
			col += 2
		}
		// Groups alternate between the even and odd columns of a 32-wide
		// span: after 32 even columns, back up to start the odd ones.
		if col&1 == 1 {
			col--
		} else {
			col -= 31
		}
		dp += arw2GroupBytes
	}
}
