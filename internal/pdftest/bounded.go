package pdftest

import (
	"os"
	"path/filepath"
	"runtime/metrics"
	"testing"
	"time"
)

// Bounded runs f and fails the test when it takes longer than maxTime or
// allocates more than 64 bytes per input byte plus 64 MiB. Fuzz targets wrap
// every call in it: no input, however hostile, may hang the code or make it
// allocate without bound.
func Bounded(t testing.TB, inputLen int, f func()) {
	t.Helper()
	const maxTime = 2 * time.Second
	sample := []metrics.Sample{{Name: "/gc/heap/allocs:bytes"}}
	metrics.Read(sample)
	before := sample[0].Value.Uint64()
	start := time.Now()
	f()
	elapsed := time.Since(start)
	metrics.Read(sample)
	allocated := sample[0].Value.Uint64() - before
	if limit := uint64(64*inputLen) + 64<<20; allocated > limit {
		t.Fatalf("allocated %d bytes for a %d-byte input (limit %d)", allocated, inputLen, limit)
	}
	if elapsed > maxTime {
		t.Fatalf("took %v for a %d-byte input", elapsed, inputLen)
	}
}

// Seeds adds every file matching pattern under the module's testdata
// directory to a fuzz corpus. dir is the path from the calling package to
// the module root.
func Seeds(f *testing.F, dir, pattern string) {
	paths, err := filepath.Glob(filepath.Join(dir, "testdata", pattern))
	if err != nil {
		f.Fatal(err)
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
}
