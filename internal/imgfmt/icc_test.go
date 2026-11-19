package imgfmt

import (
	"encoding/binary"
	"testing"

	"github.com/wisebrian/pdfo/internal/pdftest"
)

func TestCheckICC(t *testing.T) {
	rgb := pdftest.ICCProfile("RGB ")
	if p, problem := CheckICC(rgb, 3); problem != "" || len(p) != len(rgb) {
		t.Fatalf("valid profile rejected: %s", problem)
	}
	// Padding after the declared length is trimmed, not rejected.
	if p, problem := CheckICC(append(append([]byte(nil), rgb...), 0, 0, 0), 3); problem != "" || len(p) != len(rgb) {
		t.Errorf("padded profile: %d bytes, %q", len(p), problem)
	}
	mutate := func(f func(p []byte)) []byte {
		p := append([]byte(nil), rgb...)
		f(p)
		return p
	}
	bad := map[string][]byte{
		"short":            rgb[:100],
		"length too big":   mutate(func(p []byte) { binary.BigEndian.PutUint32(p, uint32(len(p)+1)) }),
		"no signature":     mutate(func(p []byte) { copy(p[36:], "xxxx") }),
		"version 5":        mutate(func(p []byte) { p[8] = 5 }),
		"device link":      mutate(func(p []byte) { copy(p[12:], "link") }),
		"bad PCS":          mutate(func(p []byte) { copy(p[20:], "RGB ") }),
		"tag count":        mutate(func(p []byte) { binary.BigEndian.PutUint32(p[128:], 1<<30) }),
		"tag past the end": mutate(func(p []byte) { binary.BigEndian.PutUint32(p[132+4:], uint32(len(p))) }),
	}
	for name, p := range bad {
		if _, problem := CheckICC(p, 3); problem == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, problem := CheckICC(rgb, 1); problem == "" {
		t.Error("RGB profile accepted for a gray image")
	}
	if _, problem := CheckICC(pdftest.ICCProfile("GRAY"), 1); problem != "" {
		t.Errorf("gray profile rejected: %s", problem)
	}
}
