// Package filter implements PDF stream filters (ISO 32000-2, section 7.4).
package filter

import (
	"compress/zlib"
	"errors"
	"io"

	"github.com/wisebrian/pdfo/internal/syntax"
)

// RowWriter compresses image samples row by row with zlib (FlateDecode). With
// prediction on, each row first goes through the PNG predictor that makes it
// smallest, chosen per row as /Predictor 15 allows: neighbouring samples
// in photographs and screenshots are close, so their differences compress
// far better than the samples themselves.
//
// Rows are written one at a time so that an image never has to exist twice
// in memory, once decoded and once as samples ready for compression.
type RowWriter struct {
	zw      *zlib.Writer
	rowLen  int
	bpp     int
	predict bool
	prev    []byte
	out     []byte // the row under the chosen filter
	tag     [1]byte
}

// NewRowWriter returns a RowWriter for rows of rowLen bytes, with bpp bytes
// per pixel (at least 1), writing the compressed stream to w at the given
// zlib level.
func NewRowWriter(w io.Writer, level, rowLen, bpp int, predict bool) (*RowWriter, error) {
	if rowLen <= 0 || bpp <= 0 {
		return nil, errors.New("filter: bad row geometry")
	}
	zw, err := zlib.NewWriterLevel(w, level)
	if err != nil {
		return nil, err
	}
	r := &RowWriter{zw: zw, rowLen: rowLen, bpp: bpp, predict: predict}
	if predict {
		r.prev = make([]byte, rowLen)
		r.out = make([]byte, rowLen)
	}
	return r, nil
}

// WriteRow compresses one row of exactly rowLen bytes.
func (r *RowWriter) WriteRow(row []byte) error {
	if len(row) != r.rowLen {
		return errors.New("filter: row of the wrong length")
	}
	if !r.predict {
		_, err := r.zw.Write(row)
		return err
	}
	prev, bpp := r.prev, r.bpp
	// The heuristic libpng uses: the filter whose output, read as signed
	// bytes, has the smallest sum of magnitudes. One pass computes the
	// five sums; only the winner's output is then produced.
	var sums [5]int
	for i, x := range row {
		var a, c byte
		if i >= bpp {
			a, c = row[i-bpp], prev[i-bpp]
		}
		b := prev[i]
		sums[0] += mag(x)
		sums[1] += mag(x - a)
		sums[2] += mag(x - b)
		sums[3] += mag(x - byte((int(a)+int(b))/2))
		sums[4] += mag(x - paethPredictor(a, b, c))
	}
	// Ties go to the lower filter type, which keeps the choice
	// deterministic.
	best := 0
	for f := 1; f < len(sums); f++ {
		if sums[f] < sums[best] {
			best = f
		}
	}
	out := row
	if best != 0 {
		out = r.out
		for i, x := range row {
			var a, c byte
			if i >= bpp {
				a, c = row[i-bpp], prev[i-bpp]
			}
			b := prev[i]
			switch best {
			case 1:
				out[i] = x - a
			case 2:
				out[i] = x - b
			case 3:
				out[i] = x - byte((int(a)+int(b))/2)
			case 4:
				out[i] = x - paethPredictor(a, b, c)
			}
		}
	}
	r.tag[0] = byte(best)
	if _, err := r.zw.Write(r.tag[:]); err != nil {
		return err
	}
	if _, err := r.zw.Write(out); err != nil {
		return err
	}
	copy(prev, row)
	return nil
}

// mag returns the magnitude of b read as a signed byte.
func mag(b byte) int {
	v := int(int8(b))
	m := v >> 63
	return (v ^ m) - m
}

// Close flushes the compressed stream. It does not close the underlying
// writer.
func (r *RowWriter) Close() error { return r.zw.Close() }

// PredictorParms returns the DecodeParms for samples written with
// prediction on: the reader needs the row geometry to undo the filters.
func PredictorParms(colors, bitsPerComponent, columns int) *syntax.Dict {
	d := new(syntax.Dict)
	d.Set("Predictor", syntax.Int(15))
	d.Set("Colors", syntax.Int(colors))
	d.Set("BitsPerComponent", syntax.Int(bitsPerComponent))
	d.Set("Columns", syntax.Int(columns))
	return d
}

// paethPredictor is the predictor of the PNG specification, section 9.4.
func paethPredictor(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	switch {
	case pa <= pb && pa <= pc:
		return a
	case pb <= pc:
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
