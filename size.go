package pdfo

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Size is a page size in points (1/72 inch), the unit of every PDF
// coordinate.
type Size struct {
	// Width and Height are as the page is defined, before any rotation
	// a viewer applies.
	Width, Height float64
}

// ISO sizes are defined in millimetres and are not whole numbers of points,
// so they are computed rather than rounded.
var (
	A3     = Size{MM(297), MM(420)}
	A4     = Size{MM(210), MM(297)}
	A5     = Size{MM(148), MM(210)}
	Letter = Size{612, 792}
	Legal  = Size{612, 1008}
)

// paperSizes are the sizes ParseSize accepts by name and SizeName reports:
// the ones offered as options, plus the others a user is likely to meet.
var paperSizes = []struct {
	name string
	size Size
}{
	{"A0", Size{MM(841), MM(1189)}},
	{"A1", Size{MM(594), MM(841)}},
	{"A2", Size{MM(420), MM(594)}},
	{"A3", A3},
	{"A4", A4},
	{"A5", A5},
	{"A6", Size{MM(105), MM(148)}},
	{"B4", Size{MM(250), MM(353)}},
	{"B5", Size{MM(176), MM(250)}},
	{"Letter", Letter},
	{"Legal", Legal},
	{"Tabloid", Size{792, 1224}},
}

// Acrobat's limits on page size (ISO 32000-1, annex C): a page whose side is
// outside them is refused or clamped, so this package never writes one.
const (
	minPage = 3.0
	maxPage = 14400.0
)

// MM converts millimetres to points.
func MM(mm float64) float64 { return mm * 72 / 25.4 }

// Inches converts inches to points.
func Inches(in float64) float64 { return in * 72 }

// ToMM converts points to millimetres, for display.
func ToMM(pt float64) float64 { return pt * 25.4 / 72 }

// SizeName returns the name of the paper size s is within 1 mm of, in either
// orientation, or "". The tolerance is needed because producers round: the
// same A4 page arrives as 595×842, 595.28×841.89 or 595.276×841.89.
func SizeName(s Size) string {
	tol := MM(1)
	near := func(a, b float64) bool { return math.Abs(a-b) <= tol }
	for _, p := range paperSizes {
		w, h := p.size.Width, p.size.Height
		if (near(s.Width, w) && near(s.Height, h)) || (near(s.Width, h) && near(s.Height, w)) {
			return p.name
		}
	}
	return ""
}

// ParseSize reads a size the way a user types it: a name ("A4", "letter")
// or width × height with a unit ("210x297mm", "8.5 x 11 in", "612x792pt",
// "21 x 29,7 cm"). A unit after only the second number applies to both.
func ParseSize(s string) (Size, error) {
	t := strings.TrimSpace(s)
	for _, p := range paperSizes {
		if strings.EqualFold(t, p.name) {
			return p.size, nil
		}
	}
	sep := strings.IndexAny(t, "xX×*")
	if sep < 0 {
		return Size{}, errors.New("pdfo: size " + strconv.Quote(s) + ": want a name such as A4 or width x height with a unit, e.g. 210x297mm")
	}
	_, sepLen := utf8.DecodeRuneInString(t[sep:])
	a, b := strings.TrimSpace(t[:sep]), strings.TrimSpace(t[sep+sepLen:])
	bv, bunit, err := splitLength(b)
	if err != nil {
		return Size{}, errors.New("pdfo: size " + strconv.Quote(s) + ": " + err.Error())
	}
	av, aunit, err := splitLength(a)
	if err != nil {
		return Size{}, errors.New("pdfo: size " + strconv.Quote(s) + ": " + err.Error())
	}
	if aunit == "" {
		aunit = bunit
	}
	if bunit == "" {
		return Size{}, errors.New("pdfo: size " + strconv.Quote(s) + ": missing unit (mm, cm, in or pt)")
	}
	w, h := toPoints(av, aunit), toPoints(bv, bunit)
	if !(w > 0 && h > 0) || math.IsInf(w, 0) || math.IsInf(h, 0) {
		return Size{}, errors.New("pdfo: size " + strconv.Quote(s) + ": both sides must be greater than zero")
	}
	return Size{w, h}, nil
}

// ParseLength reads a length with a unit, such as "10 mm", "0.5in" or
// "12pt", and returns it in points.
func ParseLength(s string) (float64, error) {
	v, unit, err := splitLength(strings.TrimSpace(s))
	if err == nil && unit == "" {
		err = errors.New("missing unit (mm, cm, in or pt)")
	}
	if err != nil {
		return 0, errors.New("pdfo: length " + strconv.Quote(s) + ": " + err.Error())
	}
	return toPoints(v, unit), nil
}

// splitLength splits "8,5 in" into 8.5 and "in". A decimal comma is
// accepted because that is how much of the world types numbers.
func splitLength(s string) (float64, string, error) {
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.' || s[i] == ',' || (i == 0 && (s[i] == '+' || s[i] == '-'))) {
		i++
	}
	num := strings.ReplaceAll(s[:i], ",", ".")
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || math.IsNaN(v) {
		return 0, "", errors.New("not a number")
	}
	unit := strings.ToLower(strings.TrimSpace(s[i:]))
	switch unit {
	case "", "mm", "cm", "pt":
	case "in", "inch", "inches", `"`:
		unit = "in"
	default:
		return 0, "", errors.New("unknown unit " + strconv.Quote(unit) + " (want mm, cm, in or pt)")
	}
	return v, unit, nil
}

func toPoints(v float64, unit string) float64 {
	switch unit {
	case "mm":
		return MM(v)
	case "cm":
		return MM(10 * v)
	case "in":
		return Inches(v)
	}
	return v
}

// Orientation turns a target size to portrait or landscape.
type Orientation int

const (
	// Auto follows the source, so a landscape photo or page gets a
	// landscape sheet without the user having to say so.
	Auto Orientation = iota
	// Portrait makes the sheet taller than it is wide.
	Portrait
	// Landscape makes the sheet wider than it is tall.
	Landscape
)

// String returns the name the JavaScript API uses for o.
func (o Orientation) String() string {
	switch o {
	case Auto:
		return "auto"
	case Portrait:
		return "portrait"
	case Landscape:
		return "landscape"
	}
	return "Orientation(" + strconv.Itoa(int(o)) + ")"
}
