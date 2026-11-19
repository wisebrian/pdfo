package imgfmt

import (
	"encoding/binary"
	"strconv"
)

// CheckICC checks that p is an ICC profile a PDF 1.7 reader can use for an
// image with n colour components (ICC.1:2010, section 7). It returns the
// profile trimmed to the length its header declares, or the reason it cannot
// be used.
//
// The checks are structural, not colorimetric, and they matter: Acrobat
// reports an error on any page whose profile is malformed, while an image
// whose profile is left out still displays, in the device colour space.
func CheckICC(p []byte, n int) ([]byte, string) {
	if len(p) < 132 {
		return nil, "colour profile too short"
	}
	size := uint64(binary.BigEndian.Uint32(p))
	if size < 132 || size > uint64(len(p)) {
		return nil, "colour profile length does not match its header"
	}
	p = p[:size]
	if string(p[36:40]) != "acsp" {
		return nil, "not an ICC colour profile"
	}
	// PDF 1.7 refers to ICC.1:2004-10, version 4.2; version 5 (iccMAX) is a
	// different format.
	if major := p[8]; major != 2 && major != 4 {
		return nil, "ICC version " + strconv.Itoa(int(major)) + " colour profile"
	}
	switch class := string(p[12:16]); class {
	case "scnr", "mntr", "prtr", "spac":
	default:
		return nil, "a " + strconv.Quote(class) + " colour profile cannot describe an image"
	}
	want := map[int]string{1: "GRAY", 3: "RGB ", 4: "CMYK"}[n]
	if space := string(p[16:20]); space != want {
		return nil, "colour profile is for " + strconv.Quote(space) + ", not " + strconv.Itoa(n) + " components"
	}
	switch pcs := string(p[20:24]); pcs {
	case "XYZ ", "Lab ":
	default:
		return nil, "colour profile has an unknown connection space"
	}
	tags := uint64(binary.BigEndian.Uint32(p[128:]))
	if 132+12*tags > size {
		return nil, "colour profile tag table runs past its end"
	}
	for i := range tags {
		e := p[132+12*i:]
		off := uint64(binary.BigEndian.Uint32(e[4:]))
		n := uint64(binary.BigEndian.Uint32(e[8:]))
		if off < 132 || off+n > size {
			return nil, "colour profile tag runs past its end"
		}
	}
	return p, ""
}
