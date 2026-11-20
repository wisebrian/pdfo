#!/usr/bin/env bash
# Regenerates the JPEG test files from quadrants.ps with Ghostscript,
# libjpeg-turbo (cjpeg) and, on macOS, sips. See ../README.md.
set -euo pipefail
cd "$(dirname "$0")"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

gs=(gs -q -dSAFER -dBATCH -dNOPAUSE -r72)
"${gs[@]}" -sDEVICE=ppmraw -o "$tmp/quadrants.ppm" quadrants.ps
"${gs[@]}" -sDEVICE=png16m -o "$tmp/quadrants.png" quadrants.ps

# CMYK as Ghostscript writes it: inverted, with an Adobe APP14 marker. The
# 180 KB output profile Ghostscript embeds is removed with jpegtran, which
# copies the compressed data losslessly; the inverted samples stay.
"${gs[@]}" -sDEVICE=jpegcmyk -dJPEGQ=90 -o "$tmp/cmyk-gs.jpg" quadrants.ps
jpegtran -copy none -outfile cmyk-gs.jpg "$tmp/cmyk-gs.jpg"

cjpeg -quality 90 -progressive -outfile progressive.jpg "$tmp/quadrants.ppm"
cjpeg -quality 90 -rgb -outfile rgb.jpg "$tmp/quadrants.ppm"
cjpeg -quality 90 -grayscale -outfile gray-cjpeg.jpg "$tmp/quadrants.ppm"
# Valid JPEGs that a PDF cannot carry, which must be rejected clearly.
cjpeg -quality 90 -arithmetic -outfile arithmetic.jpg "$tmp/quadrants.ppm"
cjpeg -quality 90 -precision 12 -outfile 12bit.jpg "$tmp/quadrants.ppm"
cjpeg -lossless 1 -outfile lossless.jpg "$tmp/quadrants.ppm"

# CMYK as Apple's ImageIO writes it. The profile sips embeds is removed so
# the file stays small; the colour data is unchanged.
if command -v sips >/dev/null; then
	sips -s format jpeg -m "/System/Library/ColorSync/Profiles/Generic CMYK Profile.icc" \
		"$tmp/quadrants.png" --out "$tmp/cmyk-sips.jpg" >/dev/null
	jpegtran -copy none -outfile cmyk-sips.jpg "$tmp/cmyk-sips.jpg"
fi
