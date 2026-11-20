// Package pdftest checks and renders PDF files with external programs, for
// tests only. It runs programs and writes temporary files, which the library
// itself never does, so nothing outside _test.go files may import it.
//
// When a program is not installed, the check it would make is skipped with
// a message rather than failed: the tests must still pass on a machine that
// has only Go.
package pdftest

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var (
	lookOnce sync.Once
	found    map[string]bool
)

// Have reports whether the named program is installed.
func Have(name string) bool {
	lookOnce.Do(func() {
		found = map[string]bool{}
		for _, p := range []string{"qpdf", "mutool", "gs"} {
			_, err := exec.LookPath(p)
			found[p] = err == nil
		}
	})
	return found[name]
}

// Check validates pdf with every checker that is installed: qpdf --check,
// mutool info and Ghostscript, each of which reports different defects. Any
// warning fails the test, because a viewer that tolerates a defect today is
// no guarantee that the next one will.
func Check(t testing.TB, name string, pdf []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "check.pdf")
	if err := os.WriteFile(path, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	ran := false
	if Have("qpdf") {
		ran = true
		out, err := exec.Command("qpdf", "--check", path).CombinedOutput()
		if err != nil || bytes.Contains(bytes.ToLower(out), []byte("warning")) {
			t.Errorf("%s: qpdf --check: %v\n%s", name, err, out)
		}
	}
	if Have("mutool") {
		ran = true
		cmd := exec.Command("mutool", "info", path)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil || stderr.Len() > 0 {
			t.Errorf("%s: mutool info: %v\n%s%s", name, err, stderr.Bytes(), out)
		}
	}
	if Have("gs") {
		ran = true
		out, err := exec.Command("gs", "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER",
			"-sDEVICE=nullpage", path).CombinedOutput()
		if err != nil || len(bytes.TrimSpace(out)) > 0 {
			t.Errorf("%s: Ghostscript: %v\n%s", name, err, out)
		}
	}
	if !ran {
		t.Logf("%s: not validated: none of qpdf, mutool or gs is installed", name)
	}
}

// Render draws one page (numbered from 1) of pdf at the given resolution with
// mutool, and skips the test when mutool is not installed.
func Render(t testing.TB, pdf []byte, page, dpi int) *image.NRGBA {
	t.Helper()
	if !Have("mutool") {
		t.Skip("mutool is not installed; rendering checks skipped")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.pdf")
	out := filepath.Join(dir, "out.png")
	if err := os.WriteFile(in, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("mutool", "draw", "-q", "-r", strconv.Itoa(dpi), "-o", out, in, strconv.Itoa(page))
	if msg, err := cmd.CombinedOutput(); err != nil || len(bytes.TrimSpace(msg)) > 0 {
		t.Fatalf("mutool draw: %v\n%s", err, msg)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	rgba := image.NewNRGBA(img.Bounds())
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	return rgba
}

// QPDFJSON returns qpdf's JSON description of pdf, which tests use to look
// inside files before this module has a reader of its own. It skips the
// test when qpdf is not installed.
func QPDFJSON(t testing.TB, pdf []byte, args ...string) []byte {
	t.Helper()
	if !Have("qpdf") {
		t.Skip("qpdf is not installed")
	}
	path := filepath.Join(t.TempDir(), "in.pdf")
	if err := os.WriteFile(path, pdf, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("qpdf", append([]string{"--json", "--json-stream-data=none"}, append(args, path)...)...).Output()
	if err != nil {
		t.Fatalf("qpdf --json: %v", err)
	}
	return out
}

// Text runs mutool to extract a short description of a file's objects, for
// error messages.
func Text(pdf []byte) string {
	var b strings.Builder
	b.Write(pdf[:min(len(pdf), 400)])
	return b.String()
}
