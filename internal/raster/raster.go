// Package raster turns the samples of an image into rows, scales them down
// with an area-averaging (box) filter and encodes them again, as JPEG or
// Flate. The standard library has no image scaler and golang.org/x/image is
// not the standard library, hence the filter here.
package raster

import (
	"bytes"
	"compress/zlib"
	"errors"
	"image"
	"image/color"
	"image/jpeg"

	"github.com/wisebrian/pdfo/internal/filter"
)

// ErrCMYK is returned for CMYK JPEGs, which image/jpeg decodes but cannot
// encode, so they cannot be re-encoded.
var ErrCMYK = errors.New("CMYK JPEG")

// ErrTooLarge is returned for images with more pixels than allowed.
var ErrTooLarge = errors.New("image too large to decode")

// Source gives the rows of an image: Comps 8-bit samples per pixel, gray
// (1) or RGB (3).
type Source interface {
	Size() (w, h int)
	Comps() int
	// Row fills dst, of w*Comps bytes, with row y.
	Row(y int, dst []byte)
}

// Image is an image held in memory.
type Image struct {
	W, H, C int    // size in pixels, samples per pixel
	Pix     []byte // rows of W*C bytes
}

// Size returns the image's size.
func (m *Image) Size() (int, int) { return m.W, m.H }

// Comps returns the samples per pixel.
func (m *Image) Comps() int { return m.C }

// Row copies row y into dst.
func (m *Image) Row(y int, dst []byte) { copy(dst, m.Pix[y*m.W*m.C:(y+1)*m.W*m.C]) }

// Samples wraps raw 8-bit samples, as a Flate image decodes to. Data
// shorter than the image is an error: the image is damaged, and scaling
// it would invent pixels.
func Samples(data []byte, w, h, comps int) (*Image, error) {
	if w <= 0 || h <= 0 || (comps != 1 && comps != 3) {
		return nil, errors.New("bad image geometry")
	}
	if int64(len(data)) < int64(w)*int64(h)*int64(comps) {
		return nil, errors.New("image data shorter than its size")
	}
	return &Image{W: w, H: h, C: comps, Pix: data}, nil
}

// jpegSource reads the rows of a decoded JPEG without converting the whole
// image at once.
type jpegSource struct {
	img   image.Image
	w, h  int
	comps int
}

func (s *jpegSource) Size() (int, int) { return s.w, s.h }
func (s *jpegSource) Comps() int       { return s.comps }
func (s *jpegSource) Row(y int, dst []byte) {
	b := s.img.Bounds()
	y += b.Min.Y
	switch m := s.img.(type) {
	case *image.Gray:
		copy(dst, m.Pix[m.PixOffset(b.Min.X, y):])
	case *image.YCbCr:
		for x := range s.w {
			yi := m.YOffset(b.Min.X+x, y)
			ci := m.COffset(b.Min.X+x, y)
			r, g, bl := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
			dst[3*x], dst[3*x+1], dst[3*x+2] = r, g, bl
		}
	case *image.RGBA:
		off := m.PixOffset(b.Min.X, y)
		for x := range s.w {
			dst[3*x], dst[3*x+1], dst[3*x+2] = m.Pix[off+4*x], m.Pix[off+4*x+1], m.Pix[off+4*x+2]
		}
	default:
		for x := range s.w {
			r, g, bl, _ := s.img.At(b.Min.X+x, y).RGBA()
			dst[3*x], dst[3*x+1], dst[3*x+2] = byte(r>>8), byte(g>>8), byte(bl>>8)
		}
	}
}

// DecodeJPEG decodes JPEG data with image/jpeg, refusing images of more than
// maxPixels before allocating them.
func DecodeJPEG(data []byte, maxPixels int64) (Source, error) {
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxPixels {
		return nil, ErrTooLarge
	}
	if cfg.ColorModel == color.CMYKModel {
		return nil, ErrCMYK
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	comps := 3
	if _, ok := img.(*image.Gray); ok {
		comps = 1
	}
	if _, ok := img.(*image.CMYK); ok {
		return nil, ErrCMYK
	}
	return &jpegSource{img: img, w: b.Dx(), h: b.Dy(), comps: comps}, nil
}

// contrib is one source pixel's share of an output pixel.
type contrib struct {
	i int
	w float32
}

// weights returns, for each of nn output positions, the source positions of
// the n it covers and the fraction of each that falls inside it: an area
// average, so every source pixel counts in proportion to what it
// contributes, whatever the ratio.
func weights(n, nn int) [][]contrib {
	scale := float64(n) / float64(nn)
	out := make([][]contrib, nn)
	for j := range nn {
		start, end := float64(j)*scale, float64(j+1)*scale
		for i := int(start); i < n && float64(i) < end; i++ {
			lo, hi := max(start, float64(i)), min(end, float64(i+1))
			if hi > lo {
				out[j] = append(out[j], contrib{i, float32((hi - lo) / scale)})
			}
		}
	}
	return out
}

// Resize scales src down to nw×nh with a box filter. Rows are read one at a
// time and output rows finish in order, so beyond the result it holds two
// rows of accumulators: a large image never exists twice in memory.
func Resize(src Source, nw, nh int) *Image {
	w, h := src.Size()
	c := src.Comps()
	nw, nh = max(1, min(nw, w)), max(1, min(nh, h))
	hw := weights(w, nw)
	vw := weights(h, nh)
	// For each source row, the output rows it adds to.
	type part struct {
		j int
		w float32
	}
	rows := make([][]part, h)
	for j, cs := range vw {
		for _, k := range cs {
			rows[k.i] = append(rows[k.i], part{j, k.w})
		}
	}
	out := &Image{W: nw, H: nh, C: c, Pix: make([]byte, nw*nh*c)}
	line := make([]byte, w*c)
	tmp := make([]float32, nw*c)
	acc := map[int][]float32{}
	done := 0
	finish := func(j int) {
		a := acc[j]
		dst := out.Pix[j*nw*c : (j+1)*nw*c]
		for k, v := range a {
			v += 0.5
			switch {
			case v < 0:
				dst[k] = 0
			case v > 255:
				dst[k] = 255
			default:
				dst[k] = byte(v)
			}
		}
		delete(acc, j)
	}
	for y := range h {
		if len(rows[y]) == 0 {
			continue
		}
		// Output rows before the first this row adds to are complete.
		for ; done < rows[y][0].j; done++ {
			if _, ok := acc[done]; ok {
				finish(done)
			}
		}
		src.Row(y, line)
		for j, cs := range hw {
			for ch := range c {
				var s float32
				for _, k := range cs {
					s += k.w * float32(line[k.i*c+ch])
				}
				tmp[j*c+ch] = s
			}
		}
		for _, p := range rows[y] {
			a, ok := acc[p.j]
			if !ok {
				a = make([]float32, nw*c)
				acc[p.j] = a
			}
			for k, v := range tmp {
				a[k] += p.w * v
			}
		}
	}
	for ; done < nh; done++ {
		if _, ok := acc[done]; ok {
			finish(done)
		}
	}
	return out
}

// EncodeJPEG encodes src at the given quality. Colour images go to
// image/jpeg as YCbCr 4:2:0, its fast path, which also needs half the
// memory of RGBA.
func EncodeJPEG(src Source, quality int) ([]byte, error) {
	w, h := src.Size()
	var img image.Image
	if js, ok := src.(*jpegSource); ok {
		img = js.img // not resized: encode the decoded image as it is
	} else if src.Comps() == 1 {
		g := image.NewGray(image.Rect(0, 0, w, h))
		for y := range h {
			src.Row(y, g.Pix[y*g.Stride:y*g.Stride+w])
		}
		img = g
	} else {
		m := image.NewYCbCr(image.Rect(0, 0, w, h), image.YCbCrSubsampleRatio420)
		row := make([]byte, 3*w)
		for y := range h {
			src.Row(y, row)
			for x := range w {
				yy, cb, cr := color.RGBToYCbCr(row[3*x], row[3*x+1], row[3*x+2])
				m.Y[m.YOffset(x, y)] = yy
				// The chroma of a 2×2 block: its top-left pixel. Averaging
				// would be marginally smoother; the eye does not see chroma
				// at this scale.
				if x%2 == 0 && y%2 == 0 {
					ci := m.COffset(x, y)
					m.Cb[ci], m.Cr[ci] = cb, cr
				}
			}
		}
		img = m
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EncodeFlate encodes src losslessly with Flate and PNG predictors.
func EncodeFlate(src Source) ([]byte, error) {
	w, h := src.Size()
	c := src.Comps()
	var buf bytes.Buffer
	rw, err := filter.NewRowWriter(&buf, zlib.BestCompression, w*c, c, true)
	if err != nil {
		return nil, err
	}
	row := make([]byte, w*c)
	for y := range h {
		src.Row(y, row)
		if err := rw.WriteRow(row); err != nil {
			return nil, err
		}
	}
	if err := rw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
