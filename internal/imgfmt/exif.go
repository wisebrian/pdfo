package imgfmt

import "encoding/binary"

// exif is what this package reads from EXIF data: nothing else in it affects
// how an image should be placed on a page.
type exif struct {
	orientation int
	dpiX, dpiY  float64
}

// EXIF tags (EXIF 2.3, table 4) and field types.
const (
	tagOrientation    = 0x0112
	tagXResolution    = 0x011A
	tagYResolution    = 0x011B
	tagResolutionUnit = 0x0128
	typeShort         = 3
	typeLong          = 4
	typeRational      = 5
)

// parseEXIF reads the first image file directory of a TIFF structure, as
// found in a JPEG APP1 segment after "Exif\0\0" or in a PNG eXIf chunk. Bad
// data is ignored rather than reported: a photo with a broken EXIF block is
// still a photo, and every viewer shows it.
func parseEXIF(t []byte) exif {
	e := exif{orientation: 1}
	if len(t) < 8 {
		return e
	}
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return e
	}
	if bo.Uint16(t[2:]) != 42 {
		return e
	}
	ifd := uint64(bo.Uint32(t[4:]))
	if ifd+2 > uint64(len(t)) {
		return e
	}
	count := uint64(bo.Uint16(t[ifd:]))
	// A directory that claims more entries than fit is read as far as the
	// data goes.
	count = min(count, (uint64(len(t))-ifd-2)/12)
	var xres, yres float64
	unit := 2 // inches, the EXIF default
	for i := range count {
		ent := t[ifd+2+12*i:][:12]
		tag, typ := bo.Uint16(ent), bo.Uint16(ent[2:])
		n := bo.Uint32(ent[4:])
		val := ent[8:12]
		if n < 1 {
			continue
		}
		switch tag {
		case tagOrientation:
			var v uint32
			switch typ {
			case typeShort:
				v = uint32(bo.Uint16(val))
			case typeLong:
				v = bo.Uint32(val)
			default:
				continue
			}
			if v >= 1 && v <= 8 {
				e.orientation = int(v)
			}
		case tagXResolution, tagYResolution:
			if typ != typeRational {
				continue
			}
			off := uint64(bo.Uint32(val))
			if off+8 > uint64(len(t)) {
				continue
			}
			num, den := bo.Uint32(t[off:]), bo.Uint32(t[off+4:])
			if den == 0 {
				continue
			}
			if tag == tagXResolution {
				xres = float64(num) / float64(den)
			} else {
				yres = float64(num) / float64(den)
			}
		case tagResolutionUnit:
			if typ == typeShort {
				unit = int(bo.Uint16(val))
			}
		}
	}
	switch unit {
	case 2:
		e.dpiX, e.dpiY = xres, yres
	case 3: // centimetres
		e.dpiX, e.dpiY = xres*2.54, yres*2.54
	}
	return e
}
