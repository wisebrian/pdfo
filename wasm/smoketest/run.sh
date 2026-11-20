#!/usr/bin/env bash
# Builds the WebAssembly module and runs worker.js against it in headless
# Chrome, inside a Web Worker as the site will. Each check prints PASS or
# FAIL; the exit status is 1 if any failed or no result arrived.
#
#   wasm/smoketest/run.sh
#
# With "memory" and a JPEG, it instead feeds about 200 MB of copies of that
# JPEG to imagesToPDF and prints how the WebAssembly memory grew:
#
#   wasm/smoketest/run.sh memory photo.jpg
#
# With "memory-pdf" and a large PDF, it rotates and extracts its pages:
#
#   wasm/smoketest/run.sh memory-pdf big.pdf
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
dir=$(mktemp -d)
# Stop Chrome and the server by their own PIDs, wait until they are gone so
# Chrome no longer writes to its profile, then remove the directory.
cleanup() {
	kill $(jobs -p) 2>/dev/null || true
	wait 2>/dev/null || true
	rm -rf "$dir" || true
}
trap cleanup EXIT

(cd "$root" && GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o "$dir/pdfo.wasm" ./wasm)
cp "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$here/index.html" "$here/worker.js" \
	"$here/memory.html" "$here/memory.js" "$here/memory-pdf.html" "$here/memory-pdf.js" "$dir/"
cp "$root/testdata/images/progressive.jpg" "$dir/photo.jpg"
cp "$root/testdata/pdf/basic.pdf" "$root/testdata/pdf/lo-forms.pdf" "$dir/"
cp "$root/testdata/pdf/enc-r6-aes-256-user.pdf" "$dir/locked.pdf"
page=index.html
case "${1:-}" in
memory)
	cp "$2" "$dir/big.jpg"
	page=memory.html
	;;
memory-pdf)
	cp "$2" "$dir/big.pdf"
	page=memory-pdf.html
	;;
esac

chrome=""
for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" google-chrome chromium chromium-browser; do
	if command -v "$c" >/dev/null 2>&1 || [ -x "$c" ]; then chrome=$c; break; fi
done
[ -n "$chrome" ] || { echo "Chrome or Chromium is not installed"; exit 1; }

port=$((20000 + RANDOM % 20000))
(cd "$dir" && python3 -m http.server "$port" --bind 127.0.0.1 >"$dir/server.log" 2>&1) &
sleep 1
"$chrome" --headless --disable-gpu --no-first-run --user-data-dir="$dir/profile" \
	"http://127.0.0.1:$port/$page" >/dev/null 2>&1 &
for _ in $(seq 1 180); do
	grep -q 'GET /result?' "$dir/server.log" && break
	sleep 1
done
grep -o 'GET /result?[^ ]*' "$dir/server.log" | head -1 | sed 's|GET /result?||' | python3 -c '
import json, sys, urllib.parse
s = sys.stdin.read().strip()
if not s:
    print("FAIL no result from the page"); sys.exit(1)
failed = False
for r in json.loads(urllib.parse.unquote(s)):
    detail = "" if r["ok"] and r["name"] != "memory" else "  " + json.dumps(r.get("detail"))[:600]
    print(("PASS " if r["ok"] else "FAIL ") + r["name"] + detail)
    failed |= not r["ok"]
sys.exit(1 if failed else 0)
'
