#!/usr/bin/env bash
# Regenerates the PDF test files. Needs Ghostscript, qpdf, LibreOffice and
# Chrome; see ../README.md for what each file tests.
set -euo pipefail
cd "$(dirname "$0")"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# basic.ps with Ghostscript; qpdf turns page two by 90 degrees. Ghostscript
# writes a cross-reference stream and object streams, which qpdf keeps.
gs -q -dSAFER -dBATCH -dNOPAUSE -dAutoRotatePages=/None -sDEVICE=pdfwrite -o "$tmp/gs.pdf" basic.ps
qpdf --deterministic-id --rotate=+90:2 "$tmp/gs.pdf" basic.pdf

# The same document in other structures.
qpdf --deterministic-id --object-streams=disable basic.pdf basic-table.pdf
qpdf --deterministic-id --object-streams=generate basic.pdf basic-objstm.pdf
qpdf --deterministic-id --linearize basic.pdf basic-linearized.pdf

# Encrypted at every revision of the standard security handler. The user
# password, when there is one, is "user"; the owner password is "owner".
# (qpdf cannot make encrypted files deterministic: their ID is random.)
q() { qpdf --allow-weak-crypto "$@"; }
q --encrypt "" owner 40 -- basic-table.pdf enc-r2-rc4-40.pdf
q --encrypt user owner 128 --use-aes=n -- basic-table.pdf enc-r3-rc4-128-user.pdf
q --encrypt "" owner 128 --use-aes=y -- basic.pdf enc-r4-aes-128.pdf
q --encrypt user owner 256 --force-R5 -- basic.pdf enc-r5-aes-256-user.pdf
q --encrypt user owner 256 -- basic.pdf enc-r6-aes-256-user.pdf
q --encrypt "" owner 256 --cleartext-metadata -- basic.pdf enc-r6-aes-256-cleartext-metadata.pdf

# forms.html through LibreOffice: form fields (with appearances), bookmarks
# from the headings, links, tags (a structure tree) and, as an attachment,
# the source document.
lo='{"ExportFormFields":{"type":"boolean","value":"true"},"UseTaggedPDF":{"type":"boolean","value":"true"},"ExportBookmarks":{"type":"boolean","value":"true"},"IsAddStream":{"type":"boolean","value":"true"}}'
soffice --headless "-env:UserInstallation=file://$tmp/lo" --convert-to "pdf:writer_web_pdf_Export:$lo" \
	--outdir "$tmp" forms.html >/dev/null 2>&1
cp "$tmp/forms.pdf" lo-forms.pdf

# The same page printed by Chrome, with a document outline and tags.
chrome="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
command -v google-chrome >/dev/null && chrome=google-chrome
"$chrome" --headless --disable-gpu --no-first-run --user-data-dir="$tmp/chrome" --no-pdf-header-footer \
	--generate-pdf-document-outline --print-to-pdf="$tmp/chrome.pdf" "file://$PWD/forms.html" >/dev/null 2>&1
cp "$tmp/chrome.pdf" chrome.pdf
