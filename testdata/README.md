# Test data

Every file here is small, and every one can be made again from what is in this
directory.

## images/

`quadrants.ps` draws the test picture: red, green, blue and white quadrants,
from top left to bottom right, 64×48 points. It is asymmetric in both
directions, so a wrong rotation or mirroring shows in a test.

`make.sh` renders it with Ghostscript and turns it into the JPEGs below with
other programs. It needs Ghostscript, libjpeg-turbo (`cjpeg`, `jpegtran`) and,
for `cmyk-sips.jpg`, macOS:

```sh
brew install ghostscript jpeg-turbo
testdata/images/make.sh
```

| File | Made by | What it tests |
|---|---|---|
| `cmyk-gs.jpg` | Ghostscript `jpegcmyk`, then `jpegtran -copy none` | CMYK stored inverted, with an Adobe APP14 marker; the 180 KB output profile Ghostscript embeds is removed |
| `cmyk-sips.jpg` | macOS `sips` with Generic CMYK, then `jpegtran -copy none` | CMYK from Apple's ImageIO, also inverted with an Adobe marker |
| `progressive.jpg` | `cjpeg -progressive` | progressive JPEG |
| `rgb.jpg` | `cjpeg -rgb` | RGB samples without the YCbCr transform, said by an Adobe marker |
| `gray-cjpeg.jpg` | `cjpeg -grayscale` | one component |
| `arithmetic.jpg` | `cjpeg -arithmetic` | arithmetic coding, which must be rejected as unsupported |
| `12bit.jpg` | `cjpeg -precision 12` | 12-bit samples, which PDF cannot carry: rejected |
| `lossless.jpg` | `cjpeg -lossless 1` | lossless JPEG: rejected |

Other test images are made by the tests themselves, from code in
`internal/pdftest`: JPEGs with EXIF orientations, colour profiles and
metadata spliced in, and PNGs of every colour type and bit depth, including
combinations `image/png` never writes, such as gray with alpha.

## golden/

The expected output of `FromImages` for a few inputs and options, compared
byte for byte by `TestGolden`. Output is deterministic, so a difference means
the behaviour changed. When the change is intended:

```sh
go test -run TestGolden -update
```

then check the new files, for example with `qpdf --check` and by opening them.
The PNG cases depend on the output of `compress/flate`, so a Go release that
changes it changes them too.
