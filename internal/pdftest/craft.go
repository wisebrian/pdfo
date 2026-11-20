package pdftest

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strconv"
	"strings"
)

// Doc builds a PDF by hand, for files no program would write: broken
// cross-reference data, loops, odd structures. Objects are numbered from 1
// in order; offsets and the cross-reference table are computed, and tests
// then damage the bytes as they need.
type Doc struct {
	// Header replaces "%PDF-1.7".
	Header string
	// Objects are the bodies of objects 1, 2, …: what goes between
	// "N 0 obj" and "endobj".
	Objects []string
	// Trailer holds extra trailer entries; Size and Root (object 1) are
	// added unless Trailer sets Root itself.
	Trailer string
	// NoXRef leaves out the table and startxref.
	NoXRef bool
}

// Bytes writes the file.
func (d Doc) Bytes() []byte {
	var b bytes.Buffer
	h := d.Header
	if h == "" {
		h = "%PDF-1.7"
	}
	b.WriteString(h + "\n%\xE2\xE3\xCF\xD3\n")
	offs := make([]int, len(d.Objects))
	for i, o := range d.Objects {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	if d.NoXRef {
		return b.Bytes()
	}
	start := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f\r\n", len(d.Objects)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n\r\n", o)
	}
	trailer := d.Trailer
	if !strings.Contains(trailer, "/Root") {
		trailer += " /Root 1 0 R"
	}
	fmt.Fprintf(&b, "trailer\n<</Size %d %s>>\nstartxref\n%d\n%%%%EOF\n", len(d.Objects)+1, trailer, start)
	return b.Bytes()
}

// Update appends an incremental update to pdf: new values for the objects
// in objs, keyed by number, and a table and trailer whose Prev points to
// the previous table.
func Update(pdf []byte, objs map[int]string, size int, trailer string) []byte {
	prev := LastStartXRef(pdf)
	var b bytes.Buffer
	b.Write(pdf)
	nums := make([]int, 0, len(objs))
	for n := range objs {
		nums = append(nums, n)
	}
	sortInts(nums)
	offs := map[int]int{}
	for _, n := range nums {
		offs[n] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", n, objs[n])
	}
	start := b.Len()
	b.WriteString("xref\n")
	for _, n := range nums {
		fmt.Fprintf(&b, "%d 1\n%010d 00000 n\r\n", n, offs[n])
	}
	if !strings.Contains(trailer, "/Root") {
		trailer += " /Root 1 0 R"
	}
	fmt.Fprintf(&b, "trailer\n<</Size %d /Prev %d %s>>\nstartxref\n%d\n%%%%EOF\n", size, prev, trailer, start)
	return b.Bytes()
}

// LastStartXRef returns the offset the last startxref gives.
func LastStartXRef(pdf []byte) int {
	i := bytes.LastIndex(pdf, []byte("startxref"))
	if i < 0 {
		return -1
	}
	f := strings.Fields(string(pdf[i+len("startxref") : min(len(pdf), i+40)]))
	if len(f) == 0 {
		return -1
	}
	n, _ := strconv.Atoi(f[0])
	return n
}

// Stream returns a stream object body holding data, compressed with Flate
// when flate is set.
func Stream(dict string, data []byte, flate bool) string {
	if flate {
		var b bytes.Buffer
		zw := zlib.NewWriter(&b)
		zw.Write(data)
		zw.Close()
		data = b.Bytes()
		dict += "/Filter/FlateDecode"
	}
	return fmt.Sprintf("<<%s/Length %d>>stream\n%s\nendstream", dict, len(data), data)
}

// Page is the body of a page object with the given extra entries, whose
// parent is object 2.
func Page(extra string) string { return "<</Type/Page/Parent 2 0 R " + extra + ">>" }

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}
