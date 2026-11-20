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

## pdf/

`make.sh` makes every file from `basic.ps` and `forms.html`. It needs
Ghostscript, qpdf, LibreOffice and Chrome:

```sh
brew install ghostscript qpdf && brew install --cask libreoffice
testdata/pdf/make.sh
```

| File | Made by | What it tests |
|---|---|---|
| `basic.pdf` | Ghostscript from `basic.ps`, page 2 turned by qpdf | three page sizes, a rotated page, bookmarks, internal and external links, a named destination, a note, an attachment, an opening script; a cross-reference stream and object streams |
| `basic-table.pdf` | `qpdf --object-streams=disable` | the same with a classic cross-reference table |
| `basic-objstm.pdf` | `qpdf --object-streams=generate` | the same, packed by qpdf |
| `basic-linearized.pdf` | `qpdf --linearize` | linearized ("fast web view") structure |
| `enc-r2-rc4-40.pdf` | `qpdf --encrypt "" owner 40` | revision 2, RC4 40-bit, empty user password |
| `enc-r3-rc4-128-user.pdf` | `qpdf --encrypt user owner 128 --use-aes=n` | revision 3, RC4 128-bit, user password "user" |
| `enc-r4-aes-128.pdf` | `qpdf --encrypt "" owner 128 --use-aes=y` | revision 4, AES-128 |
| `enc-r5-aes-256-user.pdf` | `qpdf --encrypt user owner 256 --force-R5` | revision 5 (Acrobat 9's AES-256) |
| `enc-r6-aes-256-user.pdf` | `qpdf --encrypt user owner 256` | revision 6 (PDF 2.0's AES-256) |
| `enc-r6-aes-256-cleartext-metadata.pdf` | `... --cleartext-metadata` | EncryptMetadata false |
| `lo-forms.pdf` | LibreOffice from `forms.html` | form fields with appearances, a bookmark, links, tags (a structure tree), language, the source document attached |
| `chrome.pdf` | Chrome's `--print-to-pdf` from `forms.html` | old-style named destinations (`/Dests`), tags, an outline |

Encrypted files differ on every run: qpdf gives them a random identifier.
Files no program would write — broken cross-reference data, loops in page
trees and `/Prev` chains, objects that refer to themselves, incremental
updates, hybrid files, decompression bombs — are built by the tests with
`internal/pdftest`.
